# DEVIL'S ADVOCATE REVIEW #3 — qb-context Full Codebase Audit

> **Scope:** Every `.go` file, `go.mod`, all tests, compared against the Architectural Blueprint.
> **Reviewed by:** 6 parallel Opus agents + orchestrator synthesis.
> **Date:** 2026-03-31
> **Prior reviews:** REVIEW.md (25 issues, all fixed), REVIEW-2.md (27 issues, 22 fixed)

---

## Verdict

**The system remains structurally broken in ways that Reviews 1 and 2 identified but were never truly fixed — they were papered over.** The three pillars of the blueprint — Tree-sitter parsing, neural embeddings, and sqlite-vec vector search — are still absent or non-functional in the default build. Worse, the "fixes" from Review 2 introduced new bugs: the FK removal from `edges` now means orphan edges accumulate without cleanup, the ONNX embedder uses the wrong model/library/architecture from what the blueprint specifies, and critical race conditions exist throughout the event processing pipeline. The graph is still fundamentally disconnected because every cross-file edge points to non-existent node IDs.

**Total issues found: 97** (18 CRITICAL, 24 HIGH, 32 MEDIUM, 23 LOW)

---

## CRITICAL — System Cannot Deliver Its Promise (18 issues)

### C1. All Cross-File Edges Are Orphans — The Graph Is Disconnected
**Files:** `parser.go:94-97, 383-395, 570-574`

Every import edge's `SourceID` is `GenerateNodeID(relPath, relPath)` — e.g., `SHA256("app.js:app.js")`. **No node with this ID exists.** The parser creates nodes for functions/classes/methods, never "file nodes." Every import edge in the entire system has a dangling source ID.

Every import edge's `TargetID` is `GenerateNodeID(relPath, importPath)` — e.g., `SHA256("app.js:./database")`. The actual target node has ID `SHA256("database.js:someFunction")`. **These will never match.**

Similarly, cross-file call edges use `GenerateNodeID(sourceFile, "pkg.Function")` as the target, but the actual node is `GenerateNodeID(targetFile, "Function")`. Every cross-file call edge is a dangling pointer.

**Impact:** The graph is a collection of disconnected single-file subgraphs. PPR, betweenness, blast radius, community detection, TraceCallPath — all produce meaningless results because there is no inter-file connectivity. This is the core value proposition of the tool and it is broken.

**Review 2 identified C4 (FK removal)** as a fix for silently dropped edges. The FK was removed, but the underlying problem — that edge endpoints reference non-existent node IDs — was never fixed. Removing FKs just means the orphan edges are now *stored* instead of *dropped*, but they still connect to nothing.

---

### C2. No Tree-sitter — Architecture Blueprint Violation (Persists from Review 2)
**Files:** `parser.go` (entire), `parser/queries/*.scm` (dead code)

The blueprint mandates `gotreesitter` (pure Go). The `.scm` query files exist but are unused. The parser uses regex with a hand-rolled state machine. This cannot reliably parse nested structures, JSX, template literals with interpolation, PHP heredocs, or TypeScript-specific constructs.

**Status:** Review 2 C1 identified this. Marked "future." Still missing.

---

### C3. Wrong ONNX Library — CGO Instead of Pure Go
**Files:** `go.mod:10`, `onnx.go:11`

Blueprint mandates `github.com/shota3506/onnxruntime-purego` (pure Go, no CGO). Implementation uses `github.com/yalue/onnxruntime_go` which requires CGO. Combined with `mattn/go-sqlite3`, the binary requires a C toolchain and cannot be cross-compiled as specified.

---

### C4. Wrong Model — Qwen2 Instead of all-MiniLM-L6-v2
**Files:** `model/embed.go:4-9`, `onnx.go:25-27,135`

Blueprint specifies `all-MiniLM-L6-v2` (384-dim, encoder, mean pooling, ~22MB INT8). Implementation uses `qwen2-code-embedding` (896-dim, decoder-only causal LM, last-token pooling, 473MB). These are entirely different architectures with incompatible tokenization (WordPiece vs BPE), different pooling, and different embedding spaces.

---

### C5. Model NOT Embedded in Binary
**Files:** `model/embed.go:4-6`

Blueprint mandates `//go:embed` for single-binary deployment. The code explicitly states: "The ONNX model and tokenizer are loaded from disk at runtime via --onnx-model flag." Zero `//go:embed` usage in the codebase. Cannot achieve single-binary deployment.

---

### C6. sqlite-vec Not in go.mod — Semantic Search Is Dead Code
**File:** `go.mod`

`github.com/asg017/sqlite-vec-go-bindings` is not a dependency. The `vec0` table creation always fails. `UpsertEmbedding()` is always a no-op. `SearchSemantic()` always returns nil. The 0.15 semantic weight in composite scoring is always zero. Every embedding computation is wasted CPU.

**Status:** Review 2 C5. Marked "future." Still missing.

---

### C7. `UpsertNode` Is NOT Atomic — Silent FTS Desync
**File:** `sqlite.go:86-110`

Three separate SQL statements (INSERT node, DELETE FTS, INSERT FTS) execute without a transaction. If the process crashes between node insert and FTS insert, the node exists but is invisible to lexical search. `UpsertNodes` (batch) uses a transaction; `UpsertNode` (single) does not. Inconsistent and dangerous.

---

### C8. `RawQuery` Missing `readfile()` from Blocklist — Arbitrary File Read
**Files:** `sqlite.go:548-551`, `tools.go:475`

The dangerous patterns list blocks `load_extension`, `writefile`, `fts3_tokenizer`, `attach` — but NOT `readfile()`. An LLM agent can execute:
```sql
SELECT readfile('/etc/passwd')
```
This bypasses all path traversal protections in `read_symbol` and reads any file on the filesystem.

---

### C9. `index` Tool Has No Path Validation — Arbitrary Filesystem Traversal
**File:** `tools.go:564-572`

The `index` handler passes `p.Path` directly to `indexFn` with zero validation. No path traversal check, no repo-root confinement, no `..` filtering. An LLM agent can trigger indexing of `/etc`, `/home`, or any sensitive directory.

---

### C10. Hardcoded Embedding Dimension in Migration Ignores Configuration
**File:** `migrations.go:166-169`

The `vec0` table creation hardcodes `float[384]`:
```go
CREATE VIRTUAL TABLE IF NOT EXISTS node_embeddings USING vec0(
    node_id TEXT PRIMARY KEY, embedding float[384] distance_metric=cosine
)
```
But `NewStore` accepts a configurable `embeddingDim`. If ONNX uses dim=256, the table is still created with 384. All embeddings would fail at insert time.

---

### C11. PHP Parser Creates Duplicate Nodes
**File:** `parser.go:414, 491-508`

`phpFuncDeclRe` matches `function getUser(...)` inside class bodies because it doesn't exclude lines with visibility keywords. A class method gets added both as `UserService.getUser` (from `phpMethodDeclRe`) AND as `getUser` (from `phpFuncDeclRe`), creating duplicate nodes with different IDs. No deduplication `seen` map exists for PHP (unlike JS).

---

### C12. FTS5 Keyword Injection via Unsanitized Boolean Operators
**File:** `hybrid.go:76, 268`

The FTS5 sanitizer strips `[":(){}^+\-*]` but does NOT handle FTS5 boolean operators `OR`, `AND`, `NOT`, `NEAR`. A query like `"NOT database"` produces `NOT* OR database*`, inverting match semantics. `"NEAR/5 database"` is also unhandled. The `/` character is not stripped.

---

### C13. Semantic Scores Can Be Negative, Breaking Normalization
**File:** `hybrid.go:130-139, 300-323`

Cosine distance ranges 0-2, so `Score = 1.0 - distance` ranges -1 to 1. When all semantic scores are negative, `normalizeScores` returns them unnormalized (line 315: `if maxScore <= 0 { return scores }`). These negative scores get multiplied by 0.15 and *subtracted* from the composite score, penalizing nodes that appeared in semantic results vs. nodes that didn't. Appearing in results is worse than not appearing.

---

### C14. Memory Monitor Goroutine Leaks — Never Terminates
**File:** `main.go:114-126`

The memory monitoring goroutine runs `for range ticker.C` with no stop channel, no context, no termination mechanism. If the MCP server exits normally, the goroutine leaks.

---

### C15. Signal Handler Race — Double `Stop()` Panics
**Files:** `main.go:153-162`, `watcher.go:132-146`

Signal handler calls `w.Stop()`, `cleanup()`, `os.Exit(0)`. Deferred `w.Stop()` is also registered. If the server exits normally (not via signal), deferred path runs `w.Stop()`. The signal handler goroutine is still alive and could fire concurrently. `Watcher.Stop()` calls `close(w.stopCh)` without `sync.Once` — double close panics.

---

### C16. `handleFileEvents` Has No Index Mutex — Races with MCP Index Tool
**File:** `main.go:548-659, 129-144`

`handleFileEvents` goroutine calls `store.DeleteByFile()`, `store.UpsertNodes()`, `store.UpsertEdges()` concurrently. The MCP `index` tool uses `indexMu` mutex. But `handleFileEvents` does NOT acquire `indexMu`. Concurrent file events + MCP re-index = data corruption.

---

### C17. `go 1.25.0` in go.mod Does Not Exist
**File:** `go.mod:3`

Go 1.25 does not exist as of March 2026. Users cannot build with standard Go toolchains. `go mod tidy` and `go build` may reject this version.

---

### C18. PPR Max-Value Normalization Produces Incomparable Scores
**File:** `hybrid.go:168-169`

`normalizeMap(rawPPR)` divides all PPR values by the maximum. The highest-PPR node always gets 1.0 regardless of actual significance. If only one node has nonzero PPR, it gets `ppr = 1.0` and a composite boost of `0.35 * 1.0 = 0.35`, overwhelming BM25. This makes PPR incomparable across queries.

---

## HIGH — Significant Bugs and Deviations (24 issues)

### H1. InDegree Cache Returns Live Reference on Miss
**File:** `graph.go:341-379`

Cache miss path stores `result` in `g.inDegreeCache` AND returns the same map reference. Caller mutations corrupt the cache. Cache hit path correctly returns a copy. Inconsistent.

### H2. Go Call Edges Are File-Local Only
**File:** `parser.go:140-145, 222-235`

`extractGoCalls` creates `TargetID = GenerateNodeID(relPath, "fmt.Println")`. The actual `fmt.Println` node (if indexed) would be `GenerateNodeID("fmt/print.go", "Println")`. Every cross-package call edge is dangling.

### H3. JS/TS Call Edges Are Also File-Local
**File:** `parser.go:360-377`

Same problem as H2. `TargetID: GenerateNodeID(relPath, target)` uses source file path.

### H4. `findBlockEnd` Breaks on Template Literal Interpolation
**File:** `parser.go:657-665`

Code comments acknowledge `${...}` in template literals is not handled. Braces inside `${obj.map(x => { return x; })}` would miscount, producing incorrect `EndByte` values.

### H5. PHP Methods Without Visibility Keywords Lose Class Association
**File:** `parser.go:413`

`phpMethodDeclRe` requires `(?:public|protected|private)`. PHP allows methods without visibility (defaults to public). `function bar() {}` inside a class is caught by `phpFuncDeclRe` as standalone function `bar`, losing class association.

### H6. `DeleteByFile` Doesn't Explicitly Delete `node_scores`
**File:** `sqlite.go:205-240`

Relies on FK CASCADE from `nodes`. If FKs are ever removed (as was done for `edges` in migration v2), orphan scores accumulate silently. Inconsistent with explicit deletes for edges, embeddings, and FTS.

### H7. `UpdateFTS` Silently Swallows DELETE Error
**File:** `sqlite.go:266-277`

FTS DELETE error ignored on line 271. Failed delete + successful insert = duplicate FTS entries = double-counted search results.

### H8. `UpsertEmbedding` DELETE Error Silently Swallowed
**File:** `sqlite.go:257`

`_, _ = s.db.Exec(DELETE FROM node_embeddings ...)`. If delete fails, subsequent INSERT may fail or produce corrupt state.

### H9. `SearchNodesByName` Has LIKE Wildcard Injection
**File:** `sqlite.go:748`

User input containing `%` or `_` is not escaped. Search for `a_b` matches `aXb`, `a1b`, etc. Not SQL injection, but semantic bug.

### H10. Race Condition on `gitignores` Slice
**File:** `watcher.go:112, 163, 289-304`

`gitignores` appended during `Start()` without mutex, read in `isExcluded()` without mutex, modified in `reloadGitignore()` under mutex. Inconsistent locking = data race.

### H11. `EmbeddingDim` Is a Mutable Global — Not Thread-Safe
**File:** `engine.go:14`

`var EmbeddingDim = 384` is written in `main.go:63` and read by embedders concurrently. Classic data race.

### H12. `handleFileEvents` Async Betweenness Races with Event Loop
**File:** `main.go:630-658`

Background goroutine calls `graphEngine.ComputeBetweenness()` and `graphEngine.PageRank()` while the main event loop concurrently calls `graphEngine.RemoveNode()` and `graphEngine.AddEdge()`.

### H13. All `RegisterSDKTool` Errors Silently Discarded
**File:** `tools.go:236, 366, 463, 510, 553, 601, ...`

Every `RegisterSDKTool` call is `_ =`. Failed registrations create silent divergence between CLI and protocol modes.

### H14. `context` Tool CLI InputSchema Missing `active_files`
**File:** `tools.go:208-232`

`ContextParams` has `ActiveFiles []string` but the CLI InputSchema doesn't declare it. MCP clients can't discover or use the feature.

### H15. `readSymbolHandler` Off-by-One — Exclusive vs Inclusive EndByte Untested
**File:** `tools.go:412-423`

Code assumes `EndByte` is exclusive. No test validates byte-range correctness against real file content. Test data uses placeholder ranges (0-100, 100-200) that don't match actual source.

### H16. `searchCodeHandler` Leaks File Descriptors
**File:** `tools.go:905-912`

Manual `f.Close()` without `defer` in a loop. If `scanner.Scan()` panics, the file descriptor leaks. Long-running daemon accumulates leaked FDs.

### H17. `understand` and `get_key_symbols` Recompute PageRank Every Call
**Files:** `tools.go:1539, 1545, 732`

Every call triggers full PageRank O(V * iterations) + community detection. Not cached. Unacceptable at scale.

### H18. BPE Tokenizer Silently Drops Unknown Tokens
**File:** `tokenizer.go:152-157`

Byte fallback not enabled. Rare Unicode characters are silently lost, producing degraded embeddings without any warning.

### H19. `buildFTSQuery` Fallback Passes Unsanitized Query
**File:** `hybrid.go:121-127`

If FTS5 query fails, the fallback at line 124 passes the **original unsanitized** `query` to `SearchLexical`, bypassing all FTS5 sanitization.

### H20. `indexPath` Doesn't Respect `.gitignore`
**File:** `main.go:466-483`

Uses plain `filepath.Walk` with only `parser.IsSupported()` check. No gitignore filtering. Targeted re-index can index files the full indexer would skip.

### H21. Go Parser Ignores Type Aliases and Named Types
**File:** `parser.go:160-165`

`switch typeSpec.Type.(type)` only handles `*ast.StructType` and `*ast.InterfaceType`. Type aliases (`type Handler func(...)`) and named types (`type UserID string`) are completely ignored.

### H22. No `NodeTypeInterface` — Go Interfaces Misclassified as Class
**File:** `parser.go:164`, `types.go:11-16`

`nodeType = types.NodeTypeClass // Use Class for interfaces`. No `NodeTypeInterface` in the enum. Downstream consumers cannot distinguish structs from interfaces.

### H23. `detect_changes` Reports All Symbols in Modified Files as "Changed"
**File:** `tools.go:1042-1049`

When a file appears in git diff, ALL stored symbols for that file are reported as changed. No content-level diffing. Produces massive false positives.

### H24. ONNX Session Tensor Leak on Error Path
**File:** `onnx.go:114-121`

If `session.Run` fails, `outputs[0]` may be partially allocated but never destroyed. Memory leak in long-running daemon.

---

## MEDIUM — Notable Gaps (32 issues)

### M1. `UpsertNode` Uses Mutex Instead of Transaction for Atomicity
`sqlite.go:83-84` — Go mutex doesn't provide DB-level atomicity across multiple processes.

### M2. `RawQuery` LIMIT Check Is Substring-Based
`sqlite.go:556-558` — `"LIMIT"` in `"LIMITED"` bypasses the default LIMIT injection.

### M3. `RawQuery` Semicolon Check Blocks Legitimate Queries
`sqlite.go:543-545` — Rejects `WHERE symbol_name = 'foo;bar'`.

### M4. Dangerous Pattern Blocklist Missing `readfile`
`sqlite.go:548-551` — See C8 above. Also missing: `edit()`.

### M5. `DB()` Method Exposes Raw Connection Bypassing All Safety
`sqlite.go:76-78` — Any caller can execute arbitrary writes.

### M6. FTS5 Query Not Sanitized at Storage Layer
`sqlite.go:460` — Direct callers of `SearchLexical` bypass the sanitizer in `hybrid.go`.

### M7. Connection Pool Size Not Configured for SQLite
`sqlite.go:38` — `sql.Open` with unlimited connections. SQLite has limited concurrency.

### M8. `UpsertEmbedding` Not Transactional
`sqlite.go:245-263` — DELETE then INSERT as separate operations.

### M9. Call Edge Deduplication Missing (Go/JS/PHP)
`parser.go:214-235, 359-377, 510-565` — `fmt.Println` called 5 times = 5 identical edges. Skews PageRank and betweenness.

### M10. `findBlockEnd` Fallback Returns Arbitrary `startPos + 5000`
`parser.go:690-695` — When no matching brace found, returns an arbitrary 5000-byte window.

### M11. JS Arrow Function Regex Misses Common Patterns
`parser.go:241` — Default exports, destructured params, multi-line params, object property arrows not matched.

### M12. `looksLikeRegex` Doesn't Check Keywords
`parser.go:701-719` — `return /regex/` treated as division. Miscount of braces.

### M13. `ContentSum` in Go Parser Is Just the Name
`parser.go:119-133` — No parameter types, no return types. Less useful for search.

### M14. `buildContentSum` Can Capture Wrong Doc Blocks
`parser.go:724-739` — Adjacent declarations with no blank line: doc block from previous function attached to the next one.

### M15. `GenerateNodeID` Separator Collision
`types.go:95` — `GenerateNodeID("file:a", "b")` = `GenerateNodeID("file", "a:b")`. Both hash `"file:a:b"`.

### M16. `WalkSourceFiles` Gitignore Discovery Order Bug
`watcher.go:399-482` — Loads nested `.gitignore` before checking if the directory itself is excluded.

### M17. `indexPath` Doesn't Recompute Betweenness/PageRank
`main.go:450-539` — After targeted re-index, `node_scores` become stale.

### M18. `DecodeTokenIDs` Rebuilds Reverse Vocab on Every Call
`tokenizer.go:336-340` — O(150K) allocation per decode. Should be cached.

### M19. ONNX `EmbedBatch` Is Sequential, Not Batched
`onnx.go:154-164` — Loops through texts calling `Embed` one at a time. No SIMD/batch benefit.

### M20. No Config Validation for ONNX Settings
`config.go:61-88` — `--onnx-model` without `--onnx-lib` gives confusing ONNX runtime error.

### M21. `detect_changes` Git Ref Regex Allows `@{}`
`tools.go:976-979` — `@{upstream}` could trigger network access.

### M22. `server_test.go` Data Race on `bytes.Buffer`
`server_test.go:184-203` — Server writes, test reads concurrently. `bytes.Buffer` is not thread-safe.

### M23. `server_test.go` `done` Channel Never Read
`server_test.go:237-239, 293-295` — Server crash during test goes unobserved.

### M24. `contextHandler` Doesn't Validate Empty Query
`tools.go:134-137` — Empty query to FTS5 = unpredictable behavior.

### M25. `impactHandler` Classifies Depth-0 Nodes as LOW Risk
`tools.go:300-310` — Source node itself falls through to `default` case = LOW.

### M26. `searchCodeHandler` Only Searches Indexed Files
`tools.go:844` — Non-indexed files (new, config, docs) invisible.

### M27. Trigram/Bigram on Bytes, Not Runes
`engine.go:89-117` — Multi-byte UTF-8 characters produce invalid trigrams.

### M28. `GetConnectors` Uses Potentially Stale Community Data
`graph.go:873-941` — Reads `g.communities` without checking `g.communityValid`.

### M29. `ComputeSearchSignals` Takes Write Lock for Read Operation
`graph.go:453-470` — Blocks all concurrent readers for entire PPR duration.

### M30. PPR Seeds Not Deduplicated
`hybrid.go:157-166` — Node in both lexical top-10 and activeFiles gets double weight. Teleportation vector doesn't sum to 1.0.

### M31. Hardcoded ONNX Hidden Dimension
`onnx.go:135` — `hiddenDim := 896` hardcoded. Wrong model = garbage embeddings.

### M32. Migration v2 Table Swap Non-Atomic on Old SQLite
`migrations.go:81-92` — DROP + RENAME in transaction requires SQLite 3.25.0+.

---

## LOW — Minor Issues (23 issues)

### L1. No Tests for 5 Core Blueprint Tools
`tools_test.go` — `context`, `impact`, `read_symbol`, `query`, `index` have zero handler-level tests.

### L2. No Cross-File Edge Tests
`tests/` — Zero tests verify cross-file or cross-language edge resolution.

### L3. Integration Test Never Verifies Graph Connectivity
`integration_test.go:157-164` — Checks `NodeCount() > 0` but never checks the graph is connected.

### L4. `TestBuildFTSQuery_StopWords` Is a No-Op
`hybrid_test.go:349-372` — Empty loop body: `_ = stopWord`. Never asserts stop words are absent.

### L5. BlastRadius Test Only Tests One Edge
`integration_test.go:196-204` — `break` after first edge. No assertions on result.

### L6. ONNX Tests Hardcode macOS-Specific Library Path
`onnx_test.go:12` — `/Library/Frameworks/Python.framework/...`. Won't run in CI or on any other machine.

### L7. `TestBPETokenizer` Hardcodes Absolute Path
`tokenizer_test.go:8` — `/Users/naman/Documents/coindex/quantized_model`. Machine-specific.

### L8. No Benchmark Tests
All test files — Zero `Benchmark*` functions across the entire codebase.

### L9. `TestGenerateNodeID_KnownValue` Tests Nothing
`types_test.go:52-66` — `expected` assigned then `_ = expected`. Dead test.

### L10. Test Data Uses Fake Byte Ranges
`tools_test.go:60-80` — Byte ranges (0-100, 100-200, 200-300) don't match source content.

### L11. No Error Recovery Tests
All test files — No tests for disk full, mid-parse delete, SQLite corruption, partial migration failure.

### L12. No Large-File Tests
All test files — All fixtures have 2-5 functions. No 50+ function file tests.

### L13. Stale/Vulnerable Indirect Dependencies
`go.sum` — `golang.org/x/crypto` from July 2021, `golang.org/x/net` from Feb 2021, `gin v1.8.1` with known CVEs.

### L14. `toToolResponse` No Size Cap
`tools.go:1666-1679` — `json.MarshalIndent` on full result, no limit.

### L15. `FileFilter` Has Inconsistent Semantics Across Tools
`tools.go:74 vs 864` — `get_key_symbols` uses prefix match, `search_code` uses glob match.

### L16. `TraceCallPath` Bidirectional BFS Never Records Multiple Parents
`graph.go:586-628` — `parentFwd` typed as `map[int64][]int64` but only ever has length-1 slices.

### L17. TypeScript-Specific Constructs Not Parsed
`parser.go:55-56` — `.ts/.tsx` routed to JS parser. `interface`, `enum`, `type`, `namespace`, `abstract class` all missed.

### L18. ADR Changes Not Detected by Watcher
`watcher.go:189-197` — `.md` not in `isWatchableFile`. Architecture doc changes invisible until full re-index.

### L19. `Watcher.Stop()` / `close` Not Protected by `sync.Once`
`watcher.go:132-146` — Double call panics on `close(w.stopCh)`.

### L20. `Parser` Struct Is Stateless
`parser.go:17-20` — Empty struct, no fields. Could be package functions.

### L21. `camelCaseRe` Drops Digits
`hybrid.go:73` — `Base64Encode` splits to `["Base", "Encode"]`, losing `"64"`.

### L22. `RawQuery` Has No Test for Write Rejection
`sqlite_test.go` — No test verifying INSERT/UPDATE/DELETE/DROP rejection.

### L23. Duplicate Tool Name Registration Not Prevented
`server.go:63-68` — `RegisterTool` appends to `s.tools` without duplicate check.

---

## Summary Scorecard

| Severity | Count | Key Themes |
|----------|-------|------------|
| **CRITICAL** | 18 | Disconnected graph, missing core tech (tree-sitter/ONNX/sqlite-vec), security bypasses, data corruption, race conditions |
| **HIGH** | 24 | File-local-only edges, cache bugs, error swallowing, thread safety, performance traps |
| **MEDIUM** | 32 | Missing validation, incorrect normalization, deduplication gaps, test defects |
| **LOW** | 23 | Dead tests, missing coverage, hardcoded paths, minor inconsistencies |

---

## The Fundamental Problem

The system has two compounding failures that invalidate everything built on top:

1. **The graph is disconnected.** Import edges and cross-file call edges all reference non-existent node IDs. The graph is a collection of isolated per-file subgraphs. Every algorithm that operates on graph structure — PPR, betweenness, blast radius, community detection, TraceCallPath — produces meaningless results because the graph has no inter-file connectivity.

2. **Semantic search is non-functional.** sqlite-vec is not installed, so embeddings are computed and thrown away. The semantic weight in composite scoring is always zero. And even if sqlite-vec were installed, the ONNX embedder uses the wrong model, wrong library, wrong architecture from what the blueprint specifies.

These two failures mean the system degrades to: **FTS5 keyword search with BM25 scoring, on single-file-scoped symbol names.** That's grep with extra steps.

### What Must Be Fixed Before Anything Else Matters

1. **Fix cross-file edges.** Either create file-level nodes for import edge anchoring, or implement cross-file symbol resolution during indexing. Without this, the graph is useless.

2. **Install sqlite-vec.** Add it to `go.mod`, call `sqlite_vec.Auto()`, use the configured embedding dimension in the migration.

3. **Fix security holes.** Add `readfile` to the RawQuery blocklist. Add path validation to the `index` tool.

4. **Fix data integrity.** Wrap `UpsertNode` in a transaction. Stop swallowing DELETE errors.

5. **Fix race conditions.** Add `indexMu` to `handleFileEvents`. Make `Watcher.Stop()` idempotent. Protect `gitignores` with consistent locking.

Tree-sitter and ONNX model changes are larger efforts that can be deferred, but the above 5 items are prerequisite for the system to function at all.

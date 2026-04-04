# qb-context — Code Review (Current State, 2026-04-03)

> Validated findings only. Every issue below is confirmed against the current codebase. Stale and fixed findings from prior drafts have been removed and are listed in the appendix.
>
> **Baseline:** `go build -tags "fts5" ./...` ✅ | `go test -tags "fts5" ./... -count=1` ✅ (14 ok packages, 1 no-test-files) | `go vet -tags "fts5" ./...` ✅
>
> `github.com/asg017/sqlite-vec-go-bindings/cgo` still emits two macOS deprecation warnings for `sqlite3_auto_extension` / `sqlite3_cancel_auto_extension`. These are non-fatal dependency warnings and do not affect build, test, or vet.
>
> **Review round 13** (2026-04-03): Full-repo Opus review added 12 new findings (1 HIGH, 6 MEDIUM, 5 LOW). M1 partially fixed note added.
>
> **Review round 14** (verification pass): Removed 4 stale/fixed findings (H1, M2, M3, M4). Rewrote M1, M13, L15 to match current code. Updated counts, action priorities, and appendix. Harness-first MCP work is complete through Phase 1 only.

---

## Summary

| Severity | Count |
|----------|-------|
| HIGH     | 3     |
| MEDIUM   | 17    |
| LOW      | 37    |
| **Total**| **57**|

> **Note:** Issue IDs are stable across revisions; gaps indicate findings retired in a previous round. Retired IDs and reasons are listed in the appendix.

---

## HIGH Issues

### H2. `DetectCommunities` caches stale communities with wrong node-to-hash mappings *(NEW)*

**File:** `internal/graph/graph.go` (`DetectCommunities`, lines ~1163–1210)

`DetectCommunities` snapshots the undirected graph under a read lock, releases it, runs Louvain on the snapshot, then reacquires a write lock to publish. If `BuildFromEdges` runs between the snapshot and the publish step, the int64 node IDs in the snapshot correspond to the **old** graph, but `g.reverseMap` now maps those same int64 IDs to **different** hash IDs in the new graph (because `simple.NewDirectedGraph()` resets its ID allocator to 0). The publish step at line ~1203 uses the **new** `g.reverseMap` to look up **old** IDs, producing communities with entirely wrong node memberships. These corrupted communities are cached as valid and served until the next `BuildFromEdges`.

**Impact:** Community detection returns silently wrong results after concurrent rebuilds, corrupting architecture queries and connector detection.

**Fix:** Snapshot `reverseMap` alongside the graph in `snapshotUndirectedGraph` and use the snapshotted copy when converting gonum IDs back to hash IDs. Alternatively, add a generation counter that `BuildFromEdges` increments; discard and recompute if it changed.

---

### H3. Symlink directories not watched at runtime due to `os.Lstat` *(NEW)*

**File:** `internal/watcher/watcher.go` (line ~316, `handleRawEvent`)

When a new directory (or symlink to a directory) is created at runtime, `handleRawEvent` uses `os.Lstat(path)` to check `info.IsDir()`. However, `Lstat` does NOT follow symlinks — it reports the symlink itself, and `info.IsDir()` returns `false` for a symlink pointing to a directory. The code comment explicitly states the intent to handle symlinks to directories, but the implementation contradicts this.

**Impact:** Symlinked directories created after the watcher starts are silently not watched; new files within them are never indexed.

**Fix:** Use `os.Stat(path)` instead of `os.Lstat(path)`, consistent with how `symlinkWalk` uses `os.Stat` elsewhere.

---

### H4. Git metadata storage methods have no mutex protection — race condition *(NEW — R13)*

**Files:** `internal/storage/sqlite.go` (lines 1058–1263)

Every other method in `Store` (42 methods, lines 112–1053) acquires either `s.mu.Lock()` or `s.mu.RLock()`. The 8 git metadata methods (`UpsertRepoSnapshot`, `GetRepoSnapshot`, `UpsertGitCommits`, `UpsertFileHistory`, `UpsertFileIntents`, `GetFileIntent`, `GetFileIntentsByPaths`, `GetLatestStoredCommitHash`) have **zero** lock calls. These methods are called from background goroutines (git extraction after file events) while MCP handlers concurrently read intents via `GetFileIntent`/`GetFileIntentsByPaths`.

**Impact:** Concurrent write+read on the store without mutex protection violates the store's synchronization contract. While SQLite WAL mode handles concurrent access at the database level, the Go-level mutex is the documented synchronization mechanism — callers assume mutual exclusion. Torn reads of partially-written intent data are possible under load.

**Fix:** Add `s.mu.Lock()/defer s.mu.Unlock()` to write methods (`UpsertRepoSnapshot`, `UpsertGitCommits`, `UpsertFileHistory`, `UpsertFileIntents`). Add `s.mu.RLock()/defer s.mu.RUnlock()` to read methods (`GetRepoSnapshot`, `GetFileIntent`, `GetFileIntentsByPaths`, `GetLatestStoredCommitHash`).

---

## MEDIUM Issues

### M1. `searchCodeHandler` returns best-effort results on scanner/I/O errors without prominent failure signal

**File:** `internal/mcp/tools.go` (lines ~1099, 1120–1124, 1137–1139)

`searchCodeHandler` uses a 1 MB scanner buffer and surfaces scanner errors via `warnings` plus a `truncated: true` flag in the response metadata. However, the handler still returns a successful response containing partial matches when scanner or I/O errors occur. Callers must inspect response metadata (`warnings`, `truncated`) to detect incompleteness — there is no top-level error or status code indicating degraded results.

**Fix:** Consider returning a non-success status or a top-level `error` field when scanner/I/O errors occur, so callers do not need to inspect metadata to detect incomplete results.

---

### M5. `HybridSearch.Search` error return is always nil, masking all internal failures *(NEW)*

**File:** `internal/search/hybrid.go` (lines ~124–272)

`Search` returns `([]types.SearchResult, error)` but **never** returns a non-nil error. All internal failures — FTS5 query errors, semantic embedding failures, database errors, betweenness loading errors — are silently swallowed. Callers in `tools.go` check this error and would return `"search failed"`, but that error path is dead code. If the database has an I/O error or the FTS5 index is corrupted, the user gets empty results with no indication anything went wrong.

**Fix:** Propagate errors when **all** signal sources fail. Individual signal failures can remain best-effort, but total failure should be surfaced.

---

### M6. `toToolResponse` JSON truncation can exceed the stated 1MB limit *(NEW)*

**File:** `internal/mcp/tools.go` (lines ~1871–1887)

The truncation logic slices raw text to a `budget` in bytes, then JSON-escapes it with `json.Marshal`. JSON escaping expands the text (`\` → `\\`, `"` → `\"`, control chars → `\uXXXX`), so the final assembled string can significantly exceed `maxResponseSize`. In the worst case (text full of backslashes or quotes), the escaped string is nearly 2× the budget.

**Fix:** Truncate the *escaped* string rather than the raw input, or use a conservative budget that accounts for worst-case expansion, or add a post-escaping size check.

---

### M7. `indexHandler` path validation resolves relative paths against CWD, not RepoRoot *(NEW)*

**File:** `internal/mcp/tools.go` (line ~633)

`indexHandler` uses `filepath.Abs(p.Path)` to resolve the user-supplied path, which resolves relative to the process's CWD. It then checks whether this path is inside `repoRoot`. If CWD ≠ RepoRoot, a valid repo-relative path like `"src/main.go"` resolves to `{CWD}/src/main.go`, falls outside RepoRoot, and is incorrectly rejected as a path traversal attempt. This is inconsistent with `readSymbolHandler` which correctly uses `filepath.Join(deps.RepoRoot, ...)` first.

**Fix:** Change to `filepath.Abs(filepath.Join(repoRoot, p.Path))`, matching the pattern used by `readSymbolHandler`.

---

### M8. `indexPath` uses `filepath.Walk` instead of `symlinkWalk` and ignores `.gitignore` *(NEW)*

**File:** `cmd/qb-context/main.go` (line ~662)

The `indexPath` function (targeted re-index via MCP) uses `filepath.Walk` which does NOT follow symlinks and has no cycle detection. Every other walk function in the codebase (`indexRepo`, `WalkSourceFiles`, `WalkExisting`, `Start`) uses the custom `symlinkWalk` with cycle detection. Additionally, `indexPath` only checks `ExcludedDirs` but does not apply `.gitignore` rules.

**Impact:** Files reachable via symlinks are silently skipped during targeted re-indexing. Gitignored files can be indexed. Files visible through symlinks during full index vanish after a targeted re-index.

**Fix:** Use `watcher.WalkSourceFiles` or `symlinkWalk` and apply gitignore rules consistently.

---

### M9. Windows symlink cycle detection completely disabled *(NEW)*

**File:** `internal/watcher/symlink_windows.go` (lines 12–14)

On Windows, `platformDeviceInode` always returns an error, so in `symlinkWalkImpl` the `visited` map is never populated. The inode-based cycle detection is entirely disabled. While `filepath.EvalSymlinks` provides some protection for direct cycles, it does not prevent re-entering the same directory through different symlink paths in the same walk.

**Fix:** On Windows, use the `filepath.EvalSymlinks` resolved path as a string key for the visited map (path-based deduplication) as a fallback.

---

### M10. `RawQuery` LIMIT cap bypass via string literal decoy *(NEW)*

**File:** `internal/storage/sqlite.go` (lines ~702–710)

The LIMIT clamping regex (`\bLIMIT\s+(\d+)\b`) matches inside SQL string literals. A crafted query can embed a small LIMIT inside a string comparison to prevent clamping of the real outer LIMIT: `SELECT * FROM nodes WHERE 'limit 1' = 'limit 1' LIMIT 10000`. `FindStringSubmatch` finds the leftmost match (inside the string literal), sees 1 ≤ 500, does nothing. The outer `LIMIT 10000` survives unclamped.

**Fix:** Parse from the end of the query string to find the outermost LIMIT, or use `FindAllStringSubmatch` and clamp the *last* match.

---

### M11. `RawQuery` `ReplaceAllString` inflates small outer LIMITs to 500 *(NEW)*

**File:** `internal/storage/sqlite.go` (line ~706)

When a query contains multiple LIMIT clauses and any one exceeds 500, `ReplaceAllString` replaces *all* of them with `LIMIT 500` — including LIMITs that were intentionally smaller. Example: `SELECT * FROM (SELECT id FROM nodes LIMIT 1000) AS sub LIMIT 5` → both become `LIMIT 500`. The outer LIMIT goes from 5 → 500, returning 100× more rows than requested.

**Fix:** Use `ReplaceAllStringFunc` with per-match logic to only replace matches that exceed 500, or replace only the last/outermost LIMIT.

---

### M12. `projectToken` panics on zero-dimension vector *(NEW)*

**File:** `internal/embedding/engine.go` (lines ~180–190)

`NewTFIDFEmbedder(0)` is accepted without validation, but calling `Embed()` on any non-empty string triggers `runtime error: integer divide by zero` in `projectToken` at `hash % embDim` when `embDim` is 0.

**Fix:** Validate `dim > 0` in `NewTFIDFEmbedder`, returning an error or panicking early with a descriptive message.

---

### M13. `filepath.Abs` failure silently ignored in config *(NEW)*

**File:** `internal/config/config.go`

If `filepath.Abs(cfg.RepoRoot)` fails (e.g., working directory deleted), `cfg.RepoRoot` silently remains a relative path. The subsequent `DBPath` join then also produces a relative path. Downstream code (storage, watcher, MCP path validation) assumes `RepoRoot` is absolute.

**Fix:** Return an error if `filepath.Abs` fails — the daemon cannot function correctly with a relative `RepoRoot`.

---

### M14. Benchmark test unreachable empty-results assertion (false positive) *(NEW)*

**File:** `tests/benchmark_queries_test.go` (line ~130)

The assertion `if len(results) == 0 { t.Error("expected non-empty results") }` at line 130 is dead code — it sits inside a block guarded by `len(results) > 0` at line 106. If a benchmark handler returns an empty array, `len(results) > 0` is false, execution falls to the `else` branch, `json.Unmarshal([]byte("[]"), &single)` fails, and the test only logs `"Raw result: 2 bytes"` with no error. **All 6 benchmark queries silently pass with zero results.**

**Fix:** Move the empty-results check before the array/single-object branching logic.

---

### M15. `GetConnectors` accesses `reverseMap` without existence check *(NEW — R13)*

**File:** `internal/graph/graph.go` (lines ~1685, ~1692)

In `GetConnectors`, two map accesses read from `g.reverseMap` without checking if the key exists:

```go
succHash := g.reverseMap[succs.Node().ID()]  // No ok check
predHash := g.reverseMap[preds.Node().ID()]  // No ok check
```

Every other `reverseMap` access in the file (lines 1735, 1755, 1589, 1617, etc.) uses `if h, ok := g.reverseMap[id]; ok`. These two don't. If a node is orphaned (present in graph but missing from `reverseMap` — possible during concurrent rebuild per H2), the zero-value empty string is used as a hash ID, silently undercounting `CommunitiesSpan`.

**Fix:** Use `if succHash, ok := g.reverseMap[succs.Node().ID()]; ok { ... }` (same for preds).

---

### M16. Silent `time.Parse` errors on database timestamps *(NEW — R13)*

**File:** `internal/storage/sqlite.go` (lines 1094, 1201, 1243)

Three `time.Parse(time.RFC3339, ...)` calls discard errors via `_`:
```go
snap.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)       // line 1094
fi.LastUpdatedAt, _ = time.Parse(time.RFC3339, lastUpdated)    // line 1201
fi.LastUpdatedAt, _ = time.Parse(time.RFC3339, lastUpdated)    // line 1243
```

If stored timestamps are malformed (migration issues, data corruption), callers silently receive `time.Time{}` (zero time — year 0001). Code that checks `LastUpdatedAt` for freshness would treat corrupted records as infinitely old.

**Fix:** Return an error when timestamp parsing fails, or at minimum log a warning.

---

### M17. `GetLatestStoredCommitHash` orders by RFC3339 text column — fragile implicit invariant *(NEW — R13)*

**File:** `internal/storage/sqlite.go` (line 1258)

```sql
SELECT commit_hash FROM git_commits ORDER BY author_time DESC LIMIT 1
```

`author_time` is stored as RFC3339 text. Lexicographic sorting is only correct when all timestamps share the same timezone offset. `UpsertGitCommits` (line 1119) stores `c.AuthorTime.UTC().Format(time.RFC3339)` — currently always UTC. But this is an implicit invariant with no enforcement: if any caller ever stores a non-UTC timestamp, the ordering silently breaks. No index exists on `author_time` either, making this a full table scan on large histories.

**Fix:** Either (a) add a comment documenting the UTC invariant and add an index on `author_time`, or (b) store a Unix epoch integer column for reliable sorting.

---

### M18. ADR `readDoc` has a symlink TOCTOU window *(NEW — R13)*

**File:** `internal/adr/adr.go` (lines 129–143)

`EvalSymlinks` resolves the path at line 129, the boundary check passes at line 139, then `os.ReadFile` reads at line 143. Between the boundary check and the read, the symlink target could change (symlink swap). In practice this requires a local attacker, but the daemon runs as a long-lived background process with filesystem access.

**Fix:** Open the file first, stat the open file descriptor to verify it's within the repo boundary, then read from the open descriptor. This eliminates the TOCTOU window.

---

### M19. `gitmeta.NewExtractor` performs no config validation *(NEW — R13)*

**File:** `internal/gitmeta/gitmeta.go` (lines 83–95)

`Config{HistoryDepth: 0, MaxMessageBytes: -1}` is silently accepted. Negative `MaxMessageBytes` causes `truncateBytes` to always truncate (since `len(s) <= maxBytes` is false for negative maxBytes), producing empty strings for all commit messages. Zero `HistoryDepth` causes the iterator to immediately stop.

**Fix:** Validate config fields > 0 in `NewExtractor`, return error for invalid values.

---

### M20. `indexFn` closure has inconsistent path resolution (duplicate of M7 class) *(NEW — R13)*

**File:** `cmd/qb-context/main.go` (lines ~176–180)

The `indexFn` callback resolves relative paths with `filepath.Abs()` (which uses process CWD), the same class of bug as M7 but in a different code path — the closure used for programmatic re-indexing from the watcher. If CWD != RepoRoot, valid repo-relative paths resolve incorrectly.

**Fix:** Use `filepath.Join(cfg.RepoRoot, path)` before `Abs()`, matching the `readSymbolHandler` pattern.

---

## LOW Issues

### Dead Code / Unused Exports

**L1.** `findBlockEnd`, `looksLikeRegex` (test-only), and `skipLeadingNewline` (unused) in `internal/parser/parser.go` have no production callers.

**L2.** 12 JS/TS/PHP declaration regexes in `internal/parser/parser.go` (`jsFuncDeclRe`, `jsArrowFuncRe`, `jsClassDeclRe`, `jsMethodDeclRe`, `jsImportFromRe`, `jsRequireRe`, `tsInterfaceDeclRe`, `tsEnumDeclRe`, `tsTypeDeclRe`, `phpClassDeclRe`, `phpMethodDeclRe`, `phpFuncDeclRe`) are dead now that declaration extraction uses tree-sitter. Compiled at package init but never referenced.

**L3.** `nodeTypeFromString` and `edgeTypeFromString` in `internal/types/types.go` are unused.

**L4.** `normalize()` in `internal/embedding/engine.go` and `normalizeVec()` in `internal/embedding/onnx.go` duplicate the same L2 normalization logic.

**L5.** `WalkExisting` in `internal/watcher/watcher.go` is exported but only used in tests.

**L16.** `DeleteFTSByFile` in `internal/storage/sqlite.go` is dead code — no production caller invokes it.

**L17.** The explicit `DELETE FROM node_scores WHERE ...` in the delete path is redundant because `node_scores` has a foreign-key cascade from `nodes`.

**L18.** Exported embedder constructors and types (`NewTFIDFEmbedder`, `NewHashEmbedder`, `TFIDFEmbedder`, `HashEmbedder`) are unused outside tests.

### Runtime / API Quality

**L6.** The impact/test-detection heuristic uses a naive `strings.Contains(name, "test")` in `internal/mcp/tools.go`, which misclassifies names like `attestation` or `getTestimonials`.

**L7.** `Server.Serve()` in `internal/mcp/server.go` mutates the global logger with `log.SetOutput(os.Stderr)`. Harmless in the standalone binary, but surprising if the package is embedded.

**L8.** `NewONNXEmbedder` returns different concrete types depending on build tags (`*ONNXEmbedder` vs `*ONNXEmbedderStub`). The API works but presents a confusing contract. Both files should define the same struct name.

**L9.** `parseJavaScript` comment in `internal/parser/parser.go` still claims regex fallback, but the implementation returns only a file-level node on parse failure. The comment is stale.

**L19.** `RawQuery` LIMIT clamping only rewrites the first / outermost `LIMIT` clause. A subquery with a higher `LIMIT` would pass unclamped.

**L20.** Unreachable `else` branch in `HybridSearch.Search` for the in-degree fallback path — the preceding condition is always true at that point.

**L21.** TOCTOU window: subgraph PPR snapshot and the later in-degree recomputation can observe different graph states if a rebuild occurs between them.

**L22.** `GetAllBetweenness` error is silently dropped in the search path (`internal/search/hybrid.go`).

**L23.** `BuildFromEdges` does not reset `changeCount`, so the staleness counter accumulates across rebuilds.

**L24.** `GetConnectors` takes a write lock even when the cache is valid and a read lock would suffice.

**L25.** `json.MarshalIndent` error is ignored in the graph stats resource handler.

**L28.** `RawQuery` compiles 9 regexes (8 dangerous-pattern + 1 LIMIT) on every call. The `recursiveCTERe` is correctly pre-compiled at package level; the rest should be too. *(NEW)*

**L29.** `TFIDFEmbedder` tests compare output vector length against `GetEmbeddingDim()` (global atomic, default 384), but `TFIDFEmbedder.Embed` uses its own instance field `e.dim`. Tests pass by coincidence because both happen to be 384. *(NEW)*

### Test Quality

**L10.** `TestRace_GraphRebuildDuringSearch` in `tests/concurrent_test.go` has post-race assertions but they remain shallow — only `NodeCount() > 0` and non-nil search results. No edge-consistency or score-sanity checks.

**L11.** `TestWatcher_GitignoreHotReload` in `internal/watcher/watcher_test.go` tests with `.log` and `.tmp` files, which are already rejected by `isWatchableFile` before gitignore rules are consulted. The test passes regardless of whether hot-reload works.

**L12.** `TestFullPipeline` in `tests/integration_test.go` packs ~11 scenarios into one function without `t.Run()` subtests, making failures hard to localize.

**L26.** `TestWatcher_FileRenamed` assertions are loose — they verify event receipt but do not validate that the graph correctly reflects the rename (old node removed, new node added).

**L30.** `TestConcurrent_MultipleFileChanges` and `TestConcurrent_IndexAndDeleteSimultaneous` only assert `len(nodeIDs) != 0`, passing even if a single node survives regardless of how many were expected. The comments describe expected minimums but assertions don't enforce them. *(NEW)*

**L31.** Unreachable `if len(runs) == 0 { t.Skip("no search runs") }` in `tests/concurrent_test.go` (line ~338) — `runs` is always `make([]searchRun, 5)`. Dead code gives false impression of graceful handling. *(NEW)*

**L32.** `os.WriteFile` error unchecked at `tests/integration_test.go:254`. Every other `os.WriteFile` call in the test files checks the error. *(NEW)*

**L33.** `snapshotFullGraphData` silently skips nodes missing from `reverseMap` (`internal/graph/graph.go`, lines ~727–729). If a node exists in the directed graph but lacks a `reverseMap` entry (invariant violation), it's silently omitted from the snapshot, causing PPR to drop affected nodes with no error indication. Should log a warning. *(NEW — R13)*

**L34.** Parser missing `StartByte <= EndByte` bounds check (`internal/parser/parser.go`, lines 688–691, 1086–1089). Content slicing checks `EndByte <= len(content)` but not `StartByte <= EndByte`. If tree-sitter returns malformed byte ranges, the slice panics. *(NEW — R13)*

**L35.** `truncateBytes` break-point search is overly conservative (`internal/gitmeta/helpers.go`, lines 76–81). The `idx > len(truncated)/2` guard means newline/space break points in the first half of the string are ignored, causing mid-word truncation when meaningful breaks don't exist in the latter half. *(NEW — R13)*

**L36.** `extractTrailersJSON` silently drops malformed trailers (`internal/gitmeta/helpers.go`, lines 129–163). Keys with spaces are silently rejected (line 146) with no diagnostic. Debugging trailer extraction issues requires guessing which lines were rejected. *(NEW — R13)*

**L37.** `GetFileIntentsByPaths` returns `nil` instead of empty map for empty input (`internal/storage/sqlite.go`, lines 1206–1209). Returning `nil` forces all callers to nil-check. Conventional Go pattern returns an empty map. *(NEW — R13)*

### Documentation / Repo Hygiene

**L13.** No top-level `README.md`.

**L14.** No `CONTRIBUTING.md`.

**L15.** `.golangci.yml` targets Go 1.23 while `go.mod` specifies Go 1.25; CI does not run `golangci-lint`; `.gitignore` is sparse (only `.claude/worktrees/` and `.githooks/*.log`).

**L27.** `LICENSE` is future-dated (copyright 2026).

---

## Positive Findings

- ✅ Build, test, and `go vet` all pass with the `fts5` build tag (14 packages, 0 failures)
- ✅ All SQL in reviewed paths uses parameterized placeholders — no string-concatenated query construction found
- ✅ Multi-step writes consistently use transactions with rollback protection
- ✅ `RawQuery` maintains layered read-only defenses: prefix check, semicolon ban, function blocklist, limit clamping, and per-connection `PRAGMA query_only = ON`
- ✅ `SearchNodesByName` correctly escapes LIKE wildcards
- ✅ All 13 tools are registered through `RegisterSDKTool`
- ✅ CLI smoke tests exist in `cmd/qb-context/main_test.go`
- ✅ `ComputeInDegree` cache-hit path uses `RLock` with double-checked locking (correct pattern)
- ✅ Semantic-only queries now receive graph scoring (previously skipped PPR entirely)
- ✅ CI runs tests with `-race`
- ✅ `ComputeSearchSignals` / `ComputeSearchSignalsSubgraph` use correct snapshot-and-release pattern — deep-copy data under RLock, release, compute on snapshot without holding any lock
- ✅ `EvalSymlinks` errors in `readSymbolHandler` and `searchCodeHandler` now properly returned (previously silently discarded)
- ✅ `splitCamelCase` consolidated into single shared `tokenutil.SplitCamelCase` with correct acronym handling
- ✅ Phase 1 Inspectable contract implemented: `Inspectable`, `InspectableResponse`, `ScoreBreakdown` types in `types.go`; `context` and `get_architecture_summary` handlers return bounded output
- ✅ `searchCodeHandler` buffer increased to 1 MB with scan warnings surfaced in response via `truncated` flag and `warnings` field

---

## Action Priority

| # | Issue | Why it matters |
|---|-------|----------------|
| 1 | **H2** — `DetectCommunities` stale cache with wrong node mappings | Wrong community results after concurrent rebuilds; corrupts architecture queries |
| 2 | **H4** *(R13)* — git metadata methods missing mutex locks | 8 methods violate store's locking contract; race condition under concurrent access |
| 3 | **H3** — symlink directories not watched (`Lstat` bug) | Files in symlinked dirs silently not indexed at runtime |
| 4 | **M5** — `Search` error always nil | All search failures are invisible; impossible to diagnose production issues |
| 5 | **M10/M11** — `RawQuery` LIMIT clamping bypass/inflation | Defense-in-depth gap; crafted queries bypass limits |
| 6 | **M15** *(R13)* — `GetConnectors` reverseMap unchecked | Silent wrong results during concurrent operations |
| 7 | **M7/M20** — CWD-relative path resolution (two code paths) | Valid repo-relative paths rejected when CWD ≠ RepoRoot |
| 8 | **M8** — `indexPath` ignores symlinks/gitignore | Targeted re-index inconsistent with full index |
| 9 | **M6** — `toToolResponse` JSON truncation exceeds 1MB | Response size guarantees violated by JSON escaping expansion |
| 10 | **M16** *(R13)* — silent `time.Parse` errors | Corrupted timestamps silently become zero-time |
| 11 | **M19** *(R13)* — `NewExtractor` accepts invalid config | Negative MaxMessageBytes produces empty commit messages |
| 12 | **M12** — zero-dimension embedder panics | Unguarded division by zero crash |
| 13 | **M14** — benchmark tests always pass (unreachable assertion) | Zero test coverage for benchmark query correctness |

---

## Removed from Previous Draft

The following findings appeared in earlier revisions but have been fixed or are no longer current:

| Previous claim | Why removed |
|----------------|-------------|
| H1 (current) — incremental modification loses cross-file incoming edges | Fixed: `cmd/qb-context/main.go:1046–1162` now saves incoming cross-file edges before deletion and restores valid ones after reparsing |
| M2 (current) — `registerResources`/`registerPrompts` ignore SDK registration errors | Fixed: SDK registration errors now logged in `internal/mcp/tools.go:1975–1977` and `2006–2008`; remaining ignored `json.MarshalIndent` error belongs to L25 |
| M3 (current) — `DetectCommunities` holds write lock for full Louvain run | Fixed: now uses snapshot-and-release; residual stale-mapping bug remains as H2 |
| M4 (current) — in-degree normalization comment/behavior mismatch in hybrid search | Incorrect: `ComputeSearchSignalsSubgraph` does filter global in-degree to candidate IDs; comment in `hybrid.go:221–223` is accurate |
| H1 (older) — orphaned incoming edges / ghost nodes from `DeleteByFile` | Fixed: `DeleteByFile` deletes incoming + outgoing edges; `BuildFromEdges` filters unknown endpoints |
| H2 (older) — full re-index preserves deleted-file data | Fixed: `indexRepo` reconciles DB against filesystem |
| M1 (older) — `ComputeInDegree` write lock on cache hits | Fixed: RLock fast path added |
| M3 (older) — PPR skipped for semantic-only queries | Fixed: semantic fallback now seeds graph scoring |
| M4 (older) — stop-word list removes code-relevant terms | Fixed / no longer applies |
| M5 (older) — `EvalSymlinks(deps.RepoRoot)` errors silently discarded in MCP handlers | Fixed: `readSymbolHandler` (line 422) and `searchCodeHandler` (line 955) now return errors properly |
| M6 (older) — Duplicate `splitCamelCase` with different behavior | Fixed: consolidated into `internal/tokenutil/camelcase.go` with shared `SplitCamelCase` function; both `engine.go` and `hybrid.go` use it |
| M6 (oldest) — `RawQuery` silently discards PRAGMA reset errors | Fixed: logs error and closes connection |
| M7 (older) — `detect_changes` rejects slash branch names | Fixed / no longer applies |
| M8 (older) — `USAGE.md` materially out of date | Mostly addressed; not retained as a major finding |
| M9 (older) — `cmd/qb-context/main.go` has zero test coverage | Obsolete: `cmd/qb-context/main_test.go` now exists |
| `TestGetConnectors_BasicBridge` is effectively always-pass | Fixed: failure path calls `t.Errorf` |
| Watcher debounce writes `"v:"` at `i=10` | Fixed: uses `fmt.Sprintf("v%d", i)` |
| `SearchNodesByName` does not escape LIKE wildcards | Fixed: `escapeLIKE` helper and `ESCAPE '\'` in place |
| MCP / CLI truncation produces invalid JSON | Fixed: both paths wrap truncated JSON in a valid envelope |
| Only 5 tools registered with the SDK | Fixed: all 13 tools registered |
| `internal/config/` has no tests | Fixed: config tests exist and pass |
| `isHelperFile` / `applyPerFileCap` have no direct tests | Fixed: dedicated unit tests exist |
| `ComputeSearchSignals` holds a write lock through full PPR | Fixed: PPR runs on a snapshot without the lock |
| Both concurrency tests only assert "no panic" | No longer accurate: both gained assertions; residual concern is narrower (see L10) |

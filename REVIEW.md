# Devil's Advocate Review: qb-context

> **Reviewer:** Opus (DA Agent) | **Date:** 2026-03-30 | **Scope:** Full codebase vs architectural blueprint + C reference project

---

## Verdict

**The project has significant structural gaps between what was specified and what was built.**

The `knowledge.md` paints a picture of a nearly-complete system. The reality is different. Several foundational components specified in the blueprint are either missing entirely, stubbed out, or implemented with shortcuts that undermine the system's core value proposition.

---

## CRITICAL Issues (5)

### C1. No Tree-sitter — The Core Parsing Engine Is Missing

**Blueprint states:** *"The architecture utilizes github.com/odvcencio/gotreesitter, a pure-Go implementation of the Tree-sitter runtime"* with detailed S-expression queries for JS/TS, PHP, and Go.

**Reality:** Tree-sitter is completely absent. JS/TS and PHP use **regex-based extraction**. Go uses native `go/ast`. The `.scm` query files exist in `internal/parser/queries/` but are **decorative** — never imported, never used.

**Why this is critical:**

- `findBlockEnd()` uses naive brace counting that **breaks on braces inside strings, comments, regex literals, and template literals**. Example:
  ```javascript
  function process() {
      const fmt = "Expected: { type: 'value' }";
      return fmt;
  }
  ```
  This function's EndByte will be wrong — brace counter terminates early at the string literal.

- **JS/TS class methods are not extracted at all.** The regex catches `class Foo {}` but never extracts methods inside it. For React codebases (a primary target), this means component methods are invisible.

- **No import/dependency tracking for any language.** The blueprint's S-expressions explicitly capture `import`/`require`/`use` statements. The regex parser doesn't even attempt this. This means cross-file edges (the entire point of a structural graph) are severely limited.

- **Regex requires line-start anchoring** (`(?m)^`), so indented declarations are missed.

**Impact:** The parser — the foundation of the entire system — produces incomplete and sometimes incorrect AST data for 2 of 3 target languages. The graph is missing most cross-file edges.

**Location:** `internal/parser/parser.go`

---

### C2. No Semantic Embedding Engine — Hash Fallback Only

**Blueprint states:** *"all-MiniLM-L6-v2 via ONNX... quantized (INT8)... embedded directly into the Go binary using //go:embed... github.com/shota3506/onnxruntime-purego"*

**Reality:** The `HashEmbedder` uses SHA-256 hashing to generate pseudo-random 384-dim vectors. The comment in the code explicitly says: *"It should be replaced with a real ONNX-based embedder for production use."*

**Why this is critical:**

- Hash-based embeddings have **zero semantic understanding**. `"calculate total"` and `"compute sum"` produce completely unrelated vectors. The 15% semantic weight in composite scoring (`weightSemantic = 0.15`) is pure noise.
- The blueprint's hybrid search value proposition — *"results that are both conceptually aligned and syntactically relevant"* — is undelivered. The system can only do keyword matching.
- The `SearchSemantic()` storage method is implemented but will **crash at runtime** if sqlite-vec extension isn't loaded (no graceful fallback, no table existence check).

**Impact:** Semantic search, a headline feature, does not work. The system is keyword-search-only with graph boosting.

**Location:** `internal/embedding/engine.go`, `internal/storage/sqlite.go:442-475`

---

### C3. No MCP SDK — Custom JSON-RPC Instead

**Blueprint states:** *"The daemon uses the github.com/metoro-io/mcp-golang SDK... server := mcp_golang.NewServer(stdio.NewStdioServerTransport())"*

**Reality:** A custom JSON-RPC 2.0 implementation in `internal/mcp/server.go` — manual `bufio.Scanner` line reading, hand-rolled request/response types, custom dispatcher.

**Why this matters:**

- Custom protocol implementations are fragile. The MCP spec evolves (capability negotiation, resource subscriptions, prompt templates, sampling). A hand-rolled server won't track spec changes.
- No support for MCP resources, prompts, or sampling — only tools.
- No capability negotiation beyond hardcoded `"2024-11-05"` protocol version.
- The `go.mod` has no MCP SDK dependency at all.

**Location:** `internal/mcp/server.go`

---

### C4. PersonalizedPageRank Is Fake

**Blueprint states:** *"Personalized PageRank (PPR), the random walk 'teleportation' vector is dynamically seeded to the files the developer is currently editing"*

**Reality** (`graph.go:190-195`):

```go
// Run standard PageRank first
ranks := network.PageRankSparse(g.dg, 0.85, 1e-6)
// Then post-hoc blend with personalization
ranks[id] = ranks[id]*0.5 + w*0.5  // 50/50 blend
```

The code runs **standard PageRank** and then does a simple weighted average with a personalization vector. The code itself admits: *"This is an approximation — true PPR would modify the power iteration's teleportation vector, which gonum's PageRankSparse doesn't support."*

**Why this is critical:** PPR carries the highest weight in composite scoring (35%). A post-hoc 50/50 blend is mathematically different from true PPR. The seed nodes get a shallow boost, but the graph-structural propagation that makes PPR valuable doesn't happen. The function is named `PersonalizedPageRank` but doesn't implement it.

**Location:** `internal/graph/graph.go:157-206`

---

### C5. Massive Feature Gap vs C Reference (22 tools -> 5 tools)

The C project has **22 public MCP tools**. The Go port has **5**. Key missing capabilities:

| Missing Feature | C Project Has | Go Port |
|----------------|---------------|---------|
| **Cypher query language** | Full subset (MATCH, WHERE, RETURN, ORDER BY, aggregates, UNION, variable-length paths) | Raw SQL only |
| **trace_call_path** | Bidirectional BFS with configurable edge types, ranked results | Not implemented |
| **get_key_symbols** | Top-K by PageRank with degree stats | Not implemented |
| **Session management** | Query tracking, related untouched neighbors, session recovery | Not implemented |
| **Co-change analysis** | Git history mining, coupling scores, co-change edges | Not implemented |
| **explore mode** | Multi-search (name/QN/file matching) + dependency collection + hotspots | Not implemented |
| **understand** | 3-tier symbol resolution + callers/callees + PageRank | Not implemented |
| **prepare_change** | Pre-change review scope + risk assessment | Not implemented |
| **Change detection** | Git-based file/symbol change tracking | Not implemented |
| **Multi-language** | 66 languages via vendored tree-sitter grammars | 3 languages (Go/JS/PHP) |
| **Token budget management** | Intelligent truncation with `max_tokens` | Not implemented |
| **HTTP route mapping** | Cross-service request tracing | Not implemented |
| **Infrastructure indexing** | Dockerfile, K8s, Terraform | Not implemented |
| **search_code** | Regex/pattern-based code search | Not implemented |
| **detect_changes** | Git diff-based symbol change detection | Not implemented |
| **get_architecture_summary** | Louvain + entry points + connectivity hubs | Partial (Louvain only) |

The `knowledge.md` says features like `trace_call_path` are "covered by impact tool" and `get_key_symbols` is "achievable via query tool" — but this is misleading. The impact tool does BFS blast radius, not bidirectional call path tracing. The query tool requires raw SQL, not a simple API call.

---

## HIGH Issues (7)

### H1. No Import/Dependency Edge Tracking

None of the three parsers extract `import`, `require`, or `use` statements. This means:

- Go: no package import edges
- JS/TS: no `import`/`require` edges
- PHP: no `use` namespace edges

The graph is built only from call edges (Go), instantiation edges (PHP), and basic call regex (JS). Cross-file structural relationships — the system's primary value — are sparse. The C project builds dense cross-file graphs via tree-sitter; the Go port has mostly intra-file edges.

**Location:** `internal/parser/parser.go` — no import extraction in any of `parseGoFile`, `parseJSFile`, `parsePHPFile`

---

### H2. sqlite-vec Will Crash at Runtime

The migration creates the vec0 table with a try/catch that logs a warning if sqlite-vec isn't available. But `SearchSemantic()` has **no check for table existence**. If the extension failed to load at init (which it will in most builds since sqlite-vec bindings aren't in `go.mod`), calling `SearchSemantic()` triggers `"no such table: node_embeddings"` — an unrecoverable error in the search pipeline.

**Location:** `internal/storage/migrations.go:77-85`, `internal/storage/sqlite.go:442-475`

---

### H3. Full Graph Rebuild on Every File Change

In `handleFileEvents()`, every single file create/modify/delete triggers:

```go
edges, err := store.GetAllEdges()
graphEngine.BuildFromEdges(edges)
```

This loads ALL edges from SQLite and reconstructs the entire in-memory graph. For the target scale (1-2K files), this means O(E) work on every keystroke-triggered save. The C project uses incremental graph updates.

**Location:** `cmd/qb-context/main.go:404,447`

---

### H4. Betweenness Centrality Becomes Stale

Betweenness is computed only during `indexRepo()` (full re-index). Incremental file changes via the watcher update nodes and edges, but **never recompute betweenness**. The 15% betweenness weight in search scoring uses increasingly stale data as files are modified. The C project uses Brandes' with K=200 sampling for efficiency and recomputes on significant changes.

**Location:** `cmd/qb-context/main.go` (betweenness only computed in `indexRepo`), `internal/search/hybrid.go:137`

---

### H5. Search Signal Race Condition

During a single `HybridSearch.Search()` call, five signals are fetched under **separate lock acquisitions**:

1. FTS5 BM25 -> storage RLock
2. Semantic KNN -> storage RLock
3. PPR -> graph RLock
4. Betweenness -> storage RLock
5. InDegree -> graph RLock

If a concurrent `indexRepo()` rebuilds the graph between steps 3 and 5, the PPR scores and InDegree scores are computed against different graph states. The composite score mixes signals from two different graph snapshots.

**Location:** `internal/search/hybrid.go:72-179`

---

### H6. No Watcher/Incremental Tests

The test suite has **zero tests** for:

- Filesystem watcher debouncing behavior
- Incremental file update flow (modify -> delete old -> re-parse -> re-store -> rebuild graph)
- Concurrent file changes
- Race conditions between watcher events and search queries
- Graph consistency after incremental updates

The integration test only covers the initial index pipeline, not the ongoing operational mode that the daemon spends most of its time in.

**Location:** `tests/integration_test.go` (single test), no `internal/watcher/*_test.go`

---

### H7. Batch Embedding Errors Silently Drop Nodes

In `main.go` during initial indexing:

```go
vectors, err := embedder.EmbedBatch(texts)
if err != nil {
    log.Printf("Embedding batch error: %v", err)
    continue  // Entire batch lost!
}
```

If batch 2 of 10 fails, all nodes in that batch get no embeddings. No retry, no partial recovery. In the incremental path, embeddings are computed one-at-a-time (not batched), which is both slower and inconsistent with the initial index path.

**Location:** `cmd/qb-context/main.go:329-339` (batch), `cmd/qb-context/main.go:435-442` (incremental)

---

## MEDIUM Issues (8)

### M1. FTS5 Sanitization Misses `*` Wildcard

`fts5SpecialRe` regex `[":(){}^+\-]` does not include `*`, allowing unintended prefix expansion in user queries.

**Location:** `internal/search/hybrid.go:47-53`

---

### M2. InDegree Recomputed Every Search

`ComputeInDegree()` iterates all graph nodes (O(V)) on every search query instead of caching or precomputing at index time.

**Location:** `internal/search/hybrid.go:142`, `internal/graph/graph.go:253-291`

---

### M3. GetNodeByName Returns Arbitrary Match

`LIMIT 1` with no `ORDER BY` — if two files have `processOrder()`, you get whichever SQLite returns first. Non-deterministic behavior.

**Location:** `internal/storage/sqlite.go` (`GetNodeByName`)

---

### M4. Betweenness Normalization Is Data-Dependent

Divides by max value in the current graph, not by the graph-theoretic maximum `2/((n-1)*(n-2))`. Scores aren't comparable across different graph states or sizes.

**Location:** `internal/graph/graph.go:226-228`

---

### M5. PHP Has No Call Edge Extraction

Only instantiation edges (`new ClassName()`), no function/method call tracking. PHP function calls are invisible to the graph.

**Location:** `internal/parser/parser.go` (`parsePHPFile`)

---

### M6. FTS Sync Errors Silently Ignored

In `UpsertNodes()`, FTS delete/insert errors are not checked — FTS can silently desync from the nodes table:

```go
tx.Exec("DELETE FROM nodes_fts WHERE node_id = ?", node.ID)  // No error check
ftsStmt.Exec(node.SymbolName, node.ContentSum, node.ID)       // No error check
```

**Location:** `internal/storage/sqlite.go:147-155`

---

### M7. No Schema Versioning/Migrations

If types change between versions, the database has no migration path. No version tracking, no ALTER TABLE logic. Users upgrading would need to delete and re-index.

**Location:** `internal/storage/migrations.go`

---

### M8. `index` Tool Ignores `path` Parameter

Accepts `path` in input schema but always triggers full re-index regardless of the value passed.

**Location:** `internal/mcp/tools.go:350-377`

---

## LOW Issues (5)

### L1. `buildContentSum()` Only Captures One Line of Comments

Multi-line JSDoc/PHPDoc comments are not captured in the content summary used for FTS and embedding.

**Location:** `internal/parser/parser.go:445-451`

---

### L2. Stop Word List Is English-Only and Hardcoded

28 stop words hardcoded, not configurable or language-aware.

**Location:** `internal/search/hybrid.go:27-41`

---

### L3. Per-File Cap Hardcoded to 3

`maxPerFile = 3` is a magic constant, not configurable via tool parameters or config.

**Location:** `internal/search/hybrid.go:23`

---

### L4. No Health Check or Metrics Endpoint

No way to verify daemon responsiveness, query latency, or index completeness.

---

### L5. ADR Discoverer Doesn't Validate Symlinks Against Repo Boundary

Documented in `knowledge.md` but not fixed. Symlinked ADR files could read outside the repo root.

**Location:** `internal/adr/adr.go`

---

## Summary Scorecard

| Area | Blueprint Spec | Implementation | Gap |
|------|---------------|----------------|-----|
| **Parser** | Tree-sitter (gotreesitter, pure Go) | Regex (JS/PHP), native AST (Go) | **CRITICAL** |
| **Embeddings** | all-MiniLM-L6-v2 via ONNX Runtime | SHA-256 hash fallback | **CRITICAL** |
| **MCP SDK** | mcp-golang | Custom JSON-RPC 2.0 | **CRITICAL** |
| **PPR** | True Personalized PageRank | Post-hoc 50/50 blend | **CRITICAL** |
| **Tool count** | 16+ (C reference) | 5 | **CRITICAL** |
| **Cross-file edges** | Import/require/use tracking | None | HIGH |
| **sqlite-vec** | KNN cosine similarity | Crashes if extension missing | HIGH |
| **Incremental updates** | Efficient delta | Full graph rebuild per change | HIGH |
| **Betweenness freshness** | Recomputed on changes | Stale after incremental updates | HIGH |
| **Search consistency** | Atomic signal reads | Separate RLocks per signal | HIGH |
| **Test coverage** | Comprehensive | No watcher/incremental/concurrent tests | HIGH |
| **Error recovery** | Graceful degradation | Silent batch drops | HIGH |
| **BFS blast radius** | Correct direction | Correct (incoming edges) | OK |
| **Louvain communities** | Cached + invalidated | Correct implementation | OK |
| **FTS5 BM25** | Working | Working with sanitization | OK |
| **Security** | Path traversal, SQL injection | Well-protected | OK |
| **Composite scoring** | Weighted linear combination | Correctly implemented (formula) | OK |
| **Watcher debouncing** | Timer-based coalescing | Correctly implemented | OK |

---

## What Works Well

To be fair, several things are done right:

1. **Storage layer** — SQLite schema is well-designed with proper constraints, indexes, WAL mode, and security hardening (`trusted_schema OFF`, parameterized queries, read-only transactions for RawQuery)
2. **BFS blast radius** — Correct edge direction (incoming), proper cycle handling, depth tracking
3. **Louvain community detection** — Correct implementation with cache invalidation on all graph mutations, isolated node handling
4. **Betweenness centrality** — Correct Brandes' algorithm with proper normalization
5. **Path traversal protection** — Dual symlink resolution in `read_symbol` tool
6. **Watcher debouncing** — Clean timer-based implementation with proper event coalescing
7. **Code organization** — Clean separation of concerns across packages
8. **Concurrency** — Proper RWMutex usage, bounded goroutine semaphore on MCP server

---

## Recommended Priority Order for Fixes

1. **Integrate tree-sitter** (gotreesitter) — replace regex parsers, use existing `.scm` queries, add import edge extraction
2. **Integrate ONNX embedding** (onnxruntime-purego) — replace HashEmbedder with all-MiniLM-L6-v2
3. **Fix sqlite-vec graceful fallback** — check table existence before `SearchSemantic()`, degrade gracefully
4. **Implement true PPR** — custom power iteration with configurable teleportation vector, or rename to `BiasedPageRank`
5. **Add incremental graph updates** — `AddEdge`/`RemoveEdge` instead of full `BuildFromEdges` on every file change
6. **Add import/dependency edge extraction** for all three languages
7. **Add watcher and incremental update tests**
8. **Implement trace_call_path** — bidirectional BFS with edge type filtering
9. **Implement get_key_symbols** — top-K by PageRank with degree stats
10. **Consider adopting mcp-golang SDK** or at minimum adding resource/prompt support to custom implementation

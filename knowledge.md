# qb-context — Project Knowledge Base

> Living document for team reference. Last updated: 2026-03-30 (post-Phase 3 — DA Review Fix Sprint).

---

## What We're Building

**qb-context** is a local-first Go daemon that continuously indexes codebases by building a structural graph (AST-based) and semantic index (vector embeddings), exposed via Model Context Protocol (MCP) tools. Enables LLM coding agents to perform surgical, token-efficient code retrieval instead of brute-force grep/glob.

### Key Constraints
- Single statically-linked Go binary (no Docker, no Node.js, no external DBs)
- Memory: <2GB active, <200MB idle
- Zero cloud dependencies — fully local
- Target repos: Laravel/Node monorepos (~1-2K files), React monorepos (~1K files), Go microservices (~300 files)

### Reference Project
- **C predecessor**: `/Users/naman/Documents/QBApps/codebase-memory-mcp/` — 16 tools, Louvain communities, betweenness centrality, architecture analysis, ADR support, ranked search (BM25 + PPR + betweenness + HITS)
- **v0.8.0 composite ranking**: improved search from 30→123 on 15-case benchmark

---

## Architecture

### Project Structure
```
qb-context/
├── cmd/qb-context/main.go         — CLI entry + MCP daemon + CLI tool subcommand + indexPath
├── internal/
│   ├── config/config.go            — Config with CLI flags
│   ├── types/types.go              — ASTNode, ASTEdge, enums, RiskLevel, NodeScore, Community, ProjectSummary
│   ├── watcher/watcher.go          — Filesystem watcher (fsnotify + debounce + gitignore)
│   ├── watcher/watcher_test.go     — 11 watcher tests (create/modify/delete, debounce, gitignore)
│   ├── parser/parser.go            — Multi-language parser (Go native AST, improved regex for JS/TS/PHP)
│   ├── parser/queries/*.scm        — Tree-sitter query files (reference, for future use)
│   ├── storage/sqlite.go           — SQLite storage (WAL, FTS5, sqlite-vec graceful fallback, node_scores)
│   ├── storage/migrations.go       — Versioned schema migrations (schema_version table)
│   ├── embedding/engine.go         — Embedding engine (TFIDFEmbedder default, HashEmbedder fallback)
│   ├── embedding/model/embed.go    — Placeholder for ONNX model embedding
│   ├── graph/graph.go              — gonum directed graph (true PPR, BFS, Betweenness, Louvain, InDegree cache, TraceCallPath)
│   ├── search/hybrid.go            — Multi-signal composite search with snapshot-based consistency
│   ├── adr/adr.go                  — ADR discoverer (with symlink boundary validation)
│   └── mcp/
│       ├── server.go               — mcp-golang SDK server over stdio
│       ├── tools.go                — 13 MCP tools (context, impact, read_symbol, query, index, trace_call_path, get_key_symbols, search_code, detect_changes, get_architecture_summary, explore, understand, health)
│       └── tools_test.go           — 35 tool tests
├── tests/
│   ├── integration_test.go         — Full pipeline integration test
│   ├── incremental_test.go         — Incremental update pipeline tests
│   └── concurrent_test.go          — Concurrency and race condition tests
├── .golangci.yml                   — Linter configuration
├── go.mod / go.sum
└── knowledge.md                    — This file
```

### Core Types (`internal/types`)
- `NodeType` uint8: Function(1), Class(2), Struct(3), Method(4)
- `EdgeType` uint8: Calls(1), Imports(2), Implements(3), Instantiates(4)
- `ASTNode`: ID (SHA-256 of path:symbol), FilePath, SymbolName, NodeType, StartByte, EndByte, ContentSum
- `ASTEdge`: SourceID, TargetID, EdgeType
- `FileEvent`: Path, Action (Created/Modified/Deleted)
- `SearchResult`: Node, Score
- `RiskLevel` string: CRITICAL, HIGH, MEDIUM, LOW (hop-based impact classification)
- `NodeScore`: NodeID, PageRank, Betweenness (precomputed graph metrics)
- `Community`: ID, NodeIDs (Louvain cluster membership)
- `ProjectSummary`: Project, Summary, SourceHash (ADR/architecture documents)

### Storage (`internal/storage`)
- SQLite with WAL mode, foreign keys, busy timeout, trusted_schema OFF
- Tables: `nodes`, `edges` (with CASCADE), `nodes_fts` (FTS5 with porter tokenizer), `node_embeddings` (vec0, cosine, 384-dim), `node_scores` (betweenness/pagerank with CASCADE), `project_summaries` (ADR documents), `schema_version`
- Versioned migrations: `schema_version` table tracks current version, only runs new migrations
- `hasVecTable` flag: tracks whether sqlite-vec vec0 table was created successfully
- `SearchSemantic()` gracefully returns nil when vec table unavailable (no crash)
- `UpsertEmbedding()` is a no-op when vec table unavailable
- FTS sync errors now properly checked in UpsertNode/UpsertNodes
- `GetNodeByName` uses `ORDER BY file_path, id` for deterministic results
- `GetNodeIDsByFile`, `GetAllFilePaths`, `SearchNodesByName`, `GetNodesByFile`, `GetAllNodeScores` helpers added
- Uses `ON CONFLICT DO UPDATE` for nodes (not INSERT OR REPLACE which cascades deletes)
- Uses `INSERT OR IGNORE` for edges
- `RawQuery`: SELECT-only, rejects `;` (multi-statement), blocks load_extension/writefile/fts3_tokenizer/attach, uses `BeginTx(ReadOnly: true)` for proper transaction isolation
- **Build tag**: Requires `-tags "fts5"` for FTS5 support with go-sqlite3

### Parser (`internal/parser`)
- Go files: uses `go/parser` + `go/ast` (native, accurate), extracts import edges
- JS/TS files: improved regex extraction (functions, arrow functions, classes, class methods, calls, imports/require)
- PHP files: improved regex extraction (classes, methods, functions, instantiation, method calls, static calls, use statements)
- `findBlockEnd()`: state-machine tracking 7 states (code, single-quote, double-quote, backtick, line-comment, block-comment, regex) — no longer breaks on braces in strings/comments
- `buildContentSum()`: captures full multi-line JSDoc/PHPDoc blocks (walks backwards from declaration)
- Import/dependency edge extraction for all 3 languages (Go imports, JS import/require, PHP use)
- JS/TS class method extraction as `ClassName.methodName`
- Regex patterns no longer require line-start anchoring (indented declarations found)
- File size check: skips files > 5MB
- Tree-sitter .scm query files kept as reference for future integration

### Watcher (`internal/watcher`)
- fsnotify for OS-level events, recursive directory watching
- Debounce with configurable window (default 500ms)
- .gitignore support via go-gitignore (root .gitignore only)
- `stopped` flag to prevent race condition panics on channel close

### Embedding (`internal/embedding`)
- `Embedder` interface: Embed(text) → []float32, EmbedBatch, Close
- `TFIDFEmbedder` (default): word/subword tokenization with CamelCase splitting, TF-IDF weighting, random projection to 384-dim. Provides real semantic locality (similar code → similar vectors)
- `HashEmbedder`: deterministic hash-based pseudo-embeddings (last-resort fallback, stateless)
- `NewEmbedder()` factory returns TFIDFEmbedder by default
- Future: ONNX Runtime via purego for all-MiniLM-L6-v2 (INT8 quantized, ~22MB)
- Utility: SerializeFloat32, DeserializeFloat32, CosineSimilarity

### Graph (`internal/graph`)
- gonum v0.17.0 directed graph with string hash ID <-> int64 ID mapping
- `BlastRadius`: BFS traversing **incoming edges** (`g.dg.To()`) — finds dependents (who calls this node)
- `BlastRadiusWithDepth`: same as BlastRadius but returns `map[string]int` (hashID → hop depth)
- `PersonalizedPageRank`: **TRUE PPR** via custom power iteration with teleportation vector seeded to active nodes (damping 0.85, epsilon 1e-6, max 100 iterations). Dangling nodes distribute rank to teleportation vector.
- `PageRank`: standard (non-personalized) PageRank via power iteration
- `ComputeBetweenness`: Brandes' algorithm via `network.Betweenness()`, normalized using **graph-theoretic maximum** `(n-1)*(n-2)` for directed graphs (comparable across graph sizes)
- `DetectCommunities`: Louvain via `community.Modularize()`, cached with invalidation on any graph mutation
- `ComputeInDegree`: in-degree authority per node, normalized to [0,1], **cached with invalidation** on mutations
- `ComputeSearchSignals`: computes PPR + InDegree under **single lock** for search consistency (prevents race conditions)
- `TraceCallPath`: bidirectional BFS path finding between two symbols
- `GetEntryPoints`, `GetHubs`, `GetConnectors`: architecture analysis helpers
- `GetCallers`, `GetCallees`, `CollectDeps`: dependency traversal helpers
- `ChangeCount` / `ResetChangeCount`: mutation counter for async betweenness refresh
- Thread-safe with sync.RWMutex, community + InDegree caches invalidated on BuildFromEdges/AddEdge/RemoveEdge/RemoveNode

### ADR (`internal/adr`)
- `Discoverer.Discover()` walks repo for ARCHITECTURE.md, ADR.md, DESIGN.md, adr/ dirs
- Reads content (max 8000 bytes, UTF-8-safe truncation), computes SHA-256 hash
- Deduplicates directories on case-insensitive filesystems (macOS) via `os.SameFile`
- Called during `indexRepo()` after graph build, stored in `project_summaries` table

### Search (`internal/search`)
- Multi-signal composite scoring:
  ```
  composite = 0.35*PPR + 0.25*BM25 + 0.15*Betweenness + 0.10*InDegree + 0.15*SemanticSim
  ```
- All signals normalized to [0,1] before weighting
- **Snapshot-based signal fetching**: PPR + InDegree computed under single lock via `ComputeSearchSignals()` (no race conditions)
- PPR seeded from top 10 FTS results + active file nodes (query-time, not index-time)
- FTS5 enhancements: prefix matching (`term*`), CamelCase splitting, stop word filtering, FTS5 special char sanitization (includes `*` wildcard)
- Per-file cap: configurable via `max_per_file` parameter (default 3)
- Stop words: configurable via `SetStopWords()` function

### MCP Server (`internal/mcp`)
- **mcp-golang SDK** (github.com/metoro-io/mcp-golang v0.16.1) over stdio transport
- Full MCP capability negotiation (tools, resources, prompts)
- Dual registration: SDK typed handlers for MCP protocol + ToolHandler (json.RawMessage) for CLI mode
- `GetHandler(name)` and `GetTools()` for programmatic access (used by CLI tool)
- Supports: initialize, tools/list, tools/call, resources/list, resources/read, prompts/list, prompts/get, ping, notifications

### MCP Tools (`internal/mcp/tools.go`) — 13 tools
- `context` → HybridSearch.Search (query + limit + max_per_file + active files), supports `mode: "architecture"`, includes `architecture_context` from ADR documents
- `impact` → BlastRadiusWithDepth + risk levels (hop 1=CRITICAL, 2=HIGH, 3=MEDIUM, 4+=LOW), betweenness risk_score, affected_tests, structured summary
- `read_symbol` → Store.GetNode + byte-range file read (path traversal + symlink protected via EvalSymlinks)
- `query` → Store.RawQuery (SELECT-only, read-only transaction with BeginTx)
- `index` → full or targeted re-index (respects `path` parameter via `indexPath()`)
- `trace_call_path` → bidirectional BFS path finding between two symbols
- `get_key_symbols` → top-K symbols by PageRank with in/out degree stats, optional file filter
- `search_code` → regex-based code search across indexed files with path traversal protection
- `detect_changes` → git-based file/symbol change detection with git ref validation
- `get_architecture_summary` → enhanced Louvain + entry points + hubs + connectors
- `explore` → multi-search with dependency/dependent/hotspot analysis
- `understand` → 3-tier symbol resolution (exact → fuzzy → FTS) with callers/callees/PageRank/community
- `health` → daemon status (node count, edge count, version)

### CLI Tool (`cmd/qb-context/main.go`)
- `qb-context cli <tool_name> [json_args]` — direct tool invocation for testing/benchmarking
- `qb-context cli --list` — show available tools
- Boots full pipeline, indexes repo, calls handler directly, prints JSON to stdout
- Modeled after C project's `cli` subcommand

### Main Orchestrator (`cmd/qb-context/main.go`)
- Boot: config → storage → TFIDFEmbedder → parser → graph → initial index (+ betweenness + ADR) → watcher → MCP server
- Worker pool for parallel file parsing during initial index
- Betweenness centrality computed and stored in `node_scores` at index time
- ADR documents discovered and stored in `project_summaries` at index time
- **Incremental graph updates** via filesystem watcher: RemoveNode/AddEdge instead of full rebuild
- **Async betweenness refresh** after 20 incremental changes
- **Batch embedding recovery**: falls back to per-item embedding on batch failure
- `indexPath()` for targeted re-indexing of specific files/directories
- Graceful shutdown on SIGINT/SIGTERM with sync.Once cleanup

---

## Key Libraries

| Library | Purpose |
|---------|---------|
| github.com/mattn/go-sqlite3 | SQLite driver (CGO) |
| github.com/fsnotify/fsnotify | Filesystem events |
| github.com/crackcomm/go-gitignore | .gitignore matching |
| gonum.org/v1/gonum v0.17.0 | Graph engine, PageRank, Betweenness, Louvain community detection, InDegree |
| github.com/metoro-io/mcp-golang v0.16.1 | MCP SDK (stdio transport, tool/resource/prompt support) |
| (future) github.com/asg017/sqlite-vec-go-bindings | Vector search |
| (future) github.com/shota3506/onnxruntime-purego | ONNX inference |

---

## Completed Work — All 16 Commits

### Phase 1: Foundation (Commits 1-8)

| # | Hash | Description | Status |
|---|------|-------------|--------|
| 1 | `dbbdb37` | Project scaffold, core types, config | Done |
| 2 | `800a944` | SQLite storage, filesystem watcher, parser pipeline | Done |
| 3 | `42c6c81` | Embedding, graph, MCP engines + DA #1 critical fixes | Done |
| 4 | `679dc63` | Hybrid search (RRF) + MCP tool wiring | Done |
| 5 | `7707946` | Main orchestrator + DA #2 security fixes | Done |
| 6 | `3be9aee` | Unit tests + integration test suite | Done |
| 7 | `71b524a` | DA #3 fixes: shutdown safety, security hardening, test improvements | Done |
| 8 | `aef1421` | CLI subcommand for direct MCP tool invocation | Done |

### Phase 2: Advanced Analysis (Commits 9-16)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 9 | `e13ea57` | Betweenness centrality + risk-level impact analysis | Agent A (Sonnet) | Done |
| 10 | `b08d0f5` | Louvain community detection | Agent B (Sonnet) | Done |
| 11 | `1a118d9` | ADR (Architecture Decision Records) support | Agent C (Sonnet) | Done |
| 12 | `cf8ec9d` | Merge: Louvain into main (conflict resolution) | Orchestrator | Done |
| 13 | `3b793d0` | Ignore worktree directories | Orchestrator | Done |
| 14 | `f2bb59d` | Merge: ADR into main (conflict resolution) | Orchestrator | Done |
| 15 | `e7305e8` | Multi-signal ranked search fusion | Agent D (Sonnet) | Done |
| 16 | `e60d573` | DA #4 fixes: security hardening, cache invalidation, thread safety | 3 Sonnet agents | Done |

### Phase 3: DA Review Fix Sprint (Commits 17-23)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 17 | `cfdc0b5` | Parser overhaul — state-machine findBlockEnd, class methods, import edges, PHP calls | Opus (worktree) | Done |
| 18 | `6b6931a` | TFIDFEmbedder with semantic locality, sqlite-vec graceful fallback, batch recovery | Opus (worktree) | Done |
| 19 | `7b6c7f1` | True PPR, incremental graph updates, search signal consistency, InDegree caching | Opus (worktree) | Done |
| 20 | `81c1554` | Migrate MCP server to mcp-golang SDK with full protocol support | Opus (worktree) | Done |
| 21 | `28dd83e` | Add 7 new MCP tools (trace, key_symbols, search_code, detect, arch_summary, explore, understand) | Opus (worktree) | Done |
| 22 | `504b387` | Fix all medium/low bugs (M1,M3,M6,M7,M8,L2-L5) | Opus (worktree) | Done |
| 23 | `d7b99ae` | Add watcher, incremental pipeline, and concurrency test suites | Opus (worktree) | Done |

### Test Coverage (13 packages, all passing — 187 tests)
- `internal/types` — 10 tests (ID generation, enum values)
- `internal/storage` — 8 tests (CRUD, FTS5, search, raw query, cascade delete, node_scores, project_summaries, deterministic order, schema version)
- `internal/parser` — 10 tests (Go/JS/TS/PHP parsing, edge extraction, import edges, class methods, findBlockEnd states, docblocks)
- `internal/embedding` — 27 tests (hash embedder, TFIDF embedder, semantic locality, CamelCase similarity, tokenization, serialization)
- `internal/graph` — 37 tests (BFS, cycles, depth limits, true PPR, PPR personalization bias, DAG, betweenness theoretical normalization, blast radius depth, communities, in-degree caching, search signals, change counter, trace call path)
- `internal/search` — 24 tests (composite scoring, per-file cap, custom max_per_file, CamelCase, stop words, FTS sanitization, limits)
- `internal/mcp` — 35 tests (CLI handlers, SDK protocol, initialize, tools/list, tools/call, all 13 tools, concurrent registration)
- `internal/adr` — 20 tests (discover files, ADR directory, empty repo, max chars truncation, symlink boundary validation)
- `internal/watcher` — 11 tests (create/modify/delete events, debounce, gitignore, excluded dirs, stop safety, walk existing, subdirectory events)
- `tests/integration_test.go` — full pipeline (parse → store → embed → graph → search → delete)
- `tests/incremental_test.go` — 5 tests (add/modify/delete/consistency/full cycle)
- `tests/concurrent_test.go` — 5 tests (search during index, multi-file changes, search consistency, race conditions)

### Devil's Advocate Reviews (4 completed)
- **Review #1** (after Commits 1-3): 9 issues found — SQL injection, FTS5 schema, CASCADE deletes, watcher race, PHP bugs, file size check. All fixed.
- **Review #2** (after Commit 4): 7 issues found — BFS direction, path traversal, byte range, notification responses, marshal errors. All fixed.
- **Review #3** (after Commits 5-6): 23 issues found — double-close panic (sync.Once), goroutine limit (semaphore), fd leak (walker.Stop), SQL injection hardening (trusted_schema OFF, blocklist), symlink bypass (EvalSymlinks), test improvements (cycles, concurrent, empty string, DAG PageRank). All fixed.
- **Review #4** (after Phase 2): 16 issues found (3 CRITICAL, 5 HIGH, 4 MEDIUM, 4 LOW). 10 fixed in Phase 2.
- **Review #5 (full DA review)** (REVIEW.md): 25 issues found (5 CRITICAL, 7 HIGH, 8 MEDIUM, 5 LOW). **ALL 25 FIXED in Phase 3**:
  - CRITICAL: C1 parser overhaul (state-machine, class methods, imports), C2 TFIDFEmbedder, C3 mcp-golang SDK, C4 true PPR, C5 7 new tools (5→13 tools). All fixed.
  - HIGH: H1 import edges, H2 sqlite-vec graceful fallback, H3 incremental graph updates, H4 async betweenness refresh, H5 search signal snapshot, H6 20 new watcher/incremental/concurrent tests, H7 batch embedding recovery. All fixed.
  - MEDIUM: M1 FTS5 wildcard, M2 InDegree caching, M3 deterministic GetNodeByName, M4 graph-theoretic betweenness normalization, M5 PHP call edges, M6 FTS error checking, M7 schema versioning, M8 index path parameter. All fixed.
  - LOW: L1 multi-line docblock capture, L2 configurable stop words, L3 configurable per-file cap, L4 health tool, L5 ADR symlink validation. All fixed.

---

## Completed Features — From C Project

> **Original plan:** `.claude/plans/binary-baking-parasol.md`

### Feature 1: Louvain Community Detection (Done)
- `community.Modularize(undirected, 1.0, nil)` → communities + `community.Q()` for modularity
- In-memory cached on GraphEngine, invalidated on any graph mutation (BuildFromEdges, AddEdge, RemoveEdge, RemoveNode)
- Exposed via `context` tool `mode: "architecture"` — returns community clusters with node IDs + modularity score
- Includes isolated nodes (zero edges) in detection
- **Tests:** two-cluster detection, cache invalidation, empty graph

### Feature 2: Betweenness Centrality + Risk Levels (Done)
- `network.Betweenness(g)` — Brandes' algorithm, normalized to [0,1], stored in `node_scores` table at index time
- `BlastRadiusWithDepth()` returns `map[string]int` (hashID → hop depth)
- Impact tool restructured: hop 1=CRITICAL, 2=HIGH, 3=MEDIUM, 4+=LOW
- Returns: `{risk_score, affected_count, direct[], high_risk[], medium_risk[], low_risk[], affected_tests[], summary}`
- **Tests:** betweenness normalization, blast radius depths, node_scores CRUD, cascade delete, GetAllBetweenness

### Feature 3: ADR Support (Done)
- `internal/adr/adr.go` — discovers ARCHITECTURE.md, ADR.md, DESIGN.md, adr/ dirs
- Max 8000 bytes content (UTF-8-safe truncation), SHA-256 hash for change detection
- Case-insensitive filesystem deduplication via `os.SameFile`
- Surfaced as `architecture_context` field in `context` tool responses
- **Tests:** discover files, ADR directory, empty repo, max chars, CRUD roundtrip, update-existing

### Feature 4: Multi-Signal Ranked Search Fusion (Done)
- Composite scoring: `0.35*PPR + 0.25*BM25 + 0.15*Betweenness + 0.10*InDegree + 0.15*SemanticSim`
- All signals normalized to [0,1] before weighting
- Query-time PPR seeded from top 10 FTS results
- FTS5 enhancements: prefix matching (`term*`), CamelCase splitting, stop word filtering, special char sanitization
- Per-file cap: max 3 results per unique `file_path`
- `ComputeInDegree()` on GraphEngine — counts incoming edges, normalized to [0,1]
- **Tests:** composite scoring, per-file cap, CamelCase splitting, stop words

### Phase 3: DA Review Fix Sprint (Done)

### Feature 5: Parser Overhaul (Done)
- State-machine `findBlockEnd` with 7 states (code, strings, comments, regex, template literals)
- JS/TS class method extraction, import/require edge extraction, indented declaration support
- PHP call edges ($this->method, ClassName::static, plain functions), use statement edges
- Go import edge extraction
- Multi-line JSDoc/PHPDoc capture in `buildContentSum`
- **Tests:** 19 new parser tests (findBlockEnd states, class methods, imports, PHP calls, docblocks)

### Feature 6: TFIDFEmbedder (Done)
- Word/subword tokenization with CamelCase splitting and underscore splitting
- TF-IDF weighting with random projection to 384-dim vectors
- Real semantic locality: similar code identifiers produce similar vectors
- Graceful sqlite-vec degradation: `hasVecTable` flag, SearchSemantic returns nil when unavailable
- Batch embedding recovery: falls back to per-item on batch failure
- **Tests:** 12 new embedding tests (semantic locality, CamelCase similarity, tokenization)

### Feature 7: True PPR + Graph Improvements (Done)
- True Personalized PageRank via custom power iteration with teleportation vector
- Incremental graph updates (RemoveNode/AddEdge instead of full BuildFromEdges)
- InDegree caching with invalidation on mutations
- Snapshot-based search signal fetching (ComputeSearchSignals under single lock)
- Graph-theoretic betweenness normalization: (n-1)*(n-2)
- Async betweenness refresh after 20 incremental changes
- **Tests:** 7 new graph tests (PPR personalization bias, InDegree caching, search signals, change counter)

### Feature 8: MCP SDK Migration (Done)
- Replaced custom JSON-RPC 2.0 with `mcp-golang v0.16.1`
- Full MCP capability negotiation (tools, resources, prompts)
- Dual registration for CLI + MCP protocol compatibility
- **Tests:** 10 MCP tests (CLI handlers, SDK protocol integration)

### Feature 9: 7 New MCP Tools (Done)
- `trace_call_path`: bidirectional BFS path finding
- `get_key_symbols`: top-K by PageRank with degree stats
- `search_code`: regex-based code search with path traversal protection
- `detect_changes`: git-based file/symbol change detection
- `get_architecture_summary`: enhanced with entry points, hubs, connectors
- `explore`: multi-search with deps/dependents/hotspots
- `understand`: 3-tier symbol resolution with callers/callees/PageRank/community
- **Tests:** 31 new tool tests

### Feature 10: Comprehensive Test Coverage (Done)
- 10 watcher unit tests (events, debounce, gitignore, stop safety)
- 5 incremental pipeline integration tests (add/modify/delete/consistency/full cycle)
- 5 concurrency tests (search during index, multi-file changes, race conditions)

### Features Explicitly Skipped (for now)
- HTTP UI server / graph visualization
- Cypher query language
- Co-change frequency (requires git history integration)
- HITS authority/hub scores (C project uses, but InDegree covers similar ground)
- Tree-sitter integration (gotreesitter) — improved regex approach working well, tree-sitter for future
- ONNX Runtime embedding — TFIDFEmbedder provides real semantic locality as bridge

---

## Process & Workflow

### Agent Strategy
- **Sonnet** agents for all code implementation and fixes
- **Opus** Devil's Advocate agent reviews each commit batch
- Agents run in parallel within each commit phase
- Each commit is a clean, self-contained unit
- CTO/orchestrator pattern: Opus orchestrates, Sonnet implements

### Build & Test
```bash
go build -tags "fts5" ./...           # compilation check
go test -tags "fts5" ./... -count=1   # run all tests (FTS5 requires build tag)
golangci-lint run ./...               # lint (needs manual install)
qb-context cli --list                 # list available MCP tools
qb-context cli context '{"query":"payment","limit":5}'  # test a tool
```

### Known Limitations
- Parser uses improved regex for JS/TS/PHP (not tree-sitter) — state-machine findBlockEnd handles most cases but regex heuristics can still miss edge cases
- .gitignore only reads root file, not nested ones
- Go call edges only resolve within same file (cross-package calls create edges to non-existent nodes)
- TFIDFEmbedder provides semantic locality but not full sentence-level understanding (ONNX Runtime with all-MiniLM-L6-v2 would be better)
- sqlite-vec extension needed for vector KNN search — gracefully degrades to keyword-only when unavailable
- gonum Betweenness doesn't support sampling (O(V*E) for large graphs)
- No re-index serialization — concurrent `index` tool calls can race

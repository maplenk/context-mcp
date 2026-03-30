# qb-context — Project Knowledge Base

> Living document for team reference. Last updated: 2026-03-30 (post-Phase 2).

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
├── cmd/qb-context/main.go         — CLI entry + MCP daemon + CLI tool subcommand
├── internal/
│   ├── config/config.go            — Config with CLI flags
│   ├── types/types.go              — ASTNode, ASTEdge, enums, RiskLevel, NodeScore, Community, ProjectSummary
│   ├── watcher/watcher.go          — Filesystem watcher (fsnotify + debounce + gitignore)
│   ├── parser/parser.go            — Multi-language parser (Go native AST, regex for JS/TS/PHP)
│   ├── parser/queries/*.scm        — Tree-sitter query files (reference, for future use)
│   ├── storage/sqlite.go           — SQLite storage (WAL, FTS5, sqlite-vec, node_scores, project_summaries)
│   ├── storage/migrations.go       — Schema creation (6 tables + vec0)
│   ├── embedding/engine.go         — Embedding engine (hash fallback, ONNX interface)
│   ├── embedding/model/embed.go    — Placeholder for ONNX model embedding
│   ├── graph/graph.go              — gonum directed graph (BFS, PageRank, Betweenness, Louvain, InDegree)
│   ├── search/hybrid.go            — Multi-signal composite search (PPR+BM25+Betweenness+InDegree+Semantic)
│   ├── adr/adr.go                  — Architecture Decision Records discoverer
│   └── mcp/
│       ├── server.go               — JSON-RPC 2.0 MCP server over stdio
│       └── tools.go                — 5 MCP tool implementations (context w/ architecture mode, impact w/ risk levels)
├── tests/integration_test.go       — Full pipeline integration test
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
- Tables: `nodes`, `edges` (with CASCADE), `nodes_fts` (FTS5 with porter tokenizer), `node_embeddings` (vec0, cosine, 384-dim), `node_scores` (betweenness/pagerank with CASCADE), `project_summaries` (ADR documents)
- Uses `ON CONFLICT DO UPDATE` for nodes (not INSERT OR REPLACE which cascades deletes)
- Uses `INSERT OR IGNORE` for edges
- `RawQuery`: SELECT-only, rejects `;` (multi-statement), blocks load_extension/writefile/fts3_tokenizer/attach, uses `BeginTx(ReadOnly: true)` for proper transaction isolation
- `UpsertEmbedding` validates 384-dim, uses delete+insert for vec0 compatibility
- `UpsertNodeScores` / `GetNodeScore` / `GetAllBetweenness` for graph metric persistence
- `UpsertProjectSummary` / `GetProjectSummary` / `GetAllProjectSummaries` for ADR storage
- **Build tag**: Requires `-tags "fts5"` for FTS5 support with go-sqlite3

### Parser (`internal/parser`)
- Go files: uses `go/parser` + `go/ast` (native, accurate)
- JS/TS files: regex-based extraction (function decl, arrow functions, classes, calls)
- PHP files: regex-based extraction (classes, methods with class qualification, instantiation edges)
- File size check: skips files > 5MB
- Tree-sitter .scm query files kept as reference for future integration

### Watcher (`internal/watcher`)
- fsnotify for OS-level events, recursive directory watching
- Debounce with configurable window (default 500ms)
- .gitignore support via go-gitignore (root .gitignore only)
- `stopped` flag to prevent race condition panics on channel close

### Embedding (`internal/embedding`)
- `Embedder` interface: Embed(text) → []float32, EmbedBatch, Close
- `HashEmbedder`: deterministic hash-based pseudo-embeddings (fallback, stateless)
- Future: ONNX Runtime via purego for all-MiniLM-L6-v2 (INT8 quantized, ~22MB)
- Utility: SerializeFloat32, DeserializeFloat32, CosineSimilarity

### Graph (`internal/graph`)
- gonum v0.17.0 directed graph with string hash ID <-> int64 ID mapping
- `BlastRadius`: BFS traversing **incoming edges** (`g.dg.To()`) — finds dependents (who calls this node)
- `BlastRadiusWithDepth`: same as BlastRadius but returns `map[string]int` (hashID → hop depth)
- `PersonalizedPageRank`: PageRankSparse (0.85 damping) with post-hoc blending (approximation, not true PPR)
- `ComputeBetweenness`: Brandes' algorithm via `network.Betweenness()`, normalized to [0,1]
- `DetectCommunities`: Louvain via `community.Modularize()`, cached with invalidation on any graph mutation
- `ComputeInDegree`: in-degree authority per node, normalized to [0,1]
- Thread-safe with sync.RWMutex, community cache invalidated on BuildFromEdges/AddEdge/RemoveEdge/RemoveNode

### ADR (`internal/adr`)
- `Discoverer.Discover()` walks repo for ARCHITECTURE.md, ADR.md, DESIGN.md, adr/ dirs
- Reads content (max 8000 bytes, UTF-8-safe truncation), computes SHA-256 hash
- Deduplicates directories on case-insensitive filesystems (macOS) via `os.SameFile`
- Called during `indexRepo()` after graph build, stored in `project_summaries` table

### Search (`internal/search`)
- Multi-signal composite scoring replacing simple RRF:
  ```
  composite = 0.35*PPR + 0.25*BM25 + 0.15*Betweenness + 0.10*InDegree + 0.15*SemanticSim
  ```
- All signals normalized to [0,1] before weighting
- PPR seeded from top 10 FTS results (query-time, not index-time)
- FTS5 enhancements: prefix matching (`term*`), CamelCase splitting, stop word filtering, FTS5 special char sanitization
- Per-file cap: max 3 results per unique `file_path`

### MCP Server (`internal/mcp`)
- Custom JSON-RPC 2.0 over stdio (no third-party MCP SDK)
- Handles: initialize, tools/list, tools/call, ping, notifications/initialized
- Goroutine semaphore (max 10 concurrent handlers)
- `GetHandler(name)` and `GetTools()` for programmatic access (used by CLI tool)

### MCP Tools (`internal/mcp/tools.go`)
- `context` → HybridSearch.Search (query + limit + active files), supports `mode: "architecture"` for Louvain community detection, includes `architecture_context` from ADR documents when available
- `impact` → BlastRadiusWithDepth + risk levels (hop 1=CRITICAL, 2=HIGH, 3=MEDIUM, 4+=LOW), includes betweenness risk_score, affected_tests detection, structured summary
- `read_symbol` → Store.GetNode + byte-range file read (path traversal + symlink protected via EvalSymlinks)
- `query` → Store.RawQuery (SELECT-only, read-only transaction with BeginTx)
- `index` → full re-index pipeline callback

### CLI Tool (`cmd/qb-context/main.go`)
- `qb-context cli <tool_name> [json_args]` — direct tool invocation for testing/benchmarking
- `qb-context cli --list` — show available tools
- Boots full pipeline, indexes repo, calls handler directly, prints JSON to stdout
- Modeled after C project's `cli` subcommand

### Main Orchestrator (`cmd/qb-context/main.go`)
- Boot: config → storage → embedding → parser → graph → initial index (+ betweenness + ADR) → watcher → MCP server
- Worker pool for parallel file parsing during initial index
- Betweenness centrality computed and stored in `node_scores` at index time
- ADR documents discovered and stored in `project_summaries` at index time
- Incremental updates via filesystem watcher callbacks
- Graceful shutdown on SIGINT/SIGTERM with sync.Once cleanup

---

## Key Libraries

| Library | Purpose |
|---------|---------|
| github.com/mattn/go-sqlite3 | SQLite driver (CGO) |
| github.com/fsnotify/fsnotify | Filesystem events |
| github.com/crackcomm/go-gitignore | .gitignore matching |
| gonum.org/v1/gonum v0.17.0 | Graph engine, PageRank, Betweenness, Louvain community detection, InDegree |
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

### Test Coverage (12 packages, all passing)
- `internal/types` — 6 tests (ID generation, enum values)
- `internal/storage` — 21 tests (CRUD, FTS5, search, raw query, cascade delete, node_scores, project_summaries)
- `internal/parser` — 17 tests (Go/JS/TS/PHP parsing, edge extraction)
- `internal/embedding` — 5 tests (hash embedder, serialization, cosine similarity, empty string)
- `internal/graph` — 24 tests (BFS, cycles, depth limits, PageRank, DAG, betweenness, blast radius depth, communities, in-degree)
- `internal/search` — 8 tests (composite scoring, per-file cap, CamelCase, stop words, limits)
- `internal/mcp` — 6 tests (initialize, tools/list, tools/call, unknown method, concurrent)
- `internal/adr` — 4 tests (discover files, ADR directory, empty repo, max chars truncation)
- `tests/integration_test.go` — full pipeline (parse → store → embed → graph → search → delete)

### Devil's Advocate Reviews (4 completed)
- **Review #1** (after Commits 1-3): 9 issues found — SQL injection, FTS5 schema, CASCADE deletes, watcher race, PHP bugs, file size check. All fixed.
- **Review #2** (after Commit 4): 7 issues found — BFS direction, path traversal, byte range, notification responses, marshal errors. All fixed.
- **Review #3** (after Commits 5-6): 23 issues found — double-close panic (sync.Once), goroutine limit (semaphore), fd leak (walker.Stop), SQL injection hardening (trusted_schema OFF, blocklist), symlink bypass (EvalSymlinks), test improvements (cycles, concurrent, empty string, DAG PageRank). All fixed.
- **Review #4** (after Phase 2): 16 issues found (3 CRITICAL, 5 HIGH, 4 MEDIUM, 4 LOW). 10 fixed:
  - CRITICAL: FTS5 query injection (sanitize special chars), RawQuery multi-statement bypass (reject `;`), RawQuery transaction isolation (BeginTx with ReadOnly). All fixed.
  - HIGH: Community cache invalidation on AddEdge/RemoveEdge/RemoveNode (fixed), misleading Community.Modularity field removed (fixed), isolated nodes in community detection (fixed). Stale betweenness on incremental updates and race between search signals are documented limitations.
  - MEDIUM: Missing mutex on UpdateFTS/DeleteFTSByFile (fixed), UTF-8 truncation in ADR (fixed). ADR path traversal via symlinks and re-index serialization are low-risk.

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

### Features Explicitly Skipped (for now)
- trace_call_path (covered by impact tool)
- get_key_symbols (achievable via query tool)
- HTTP UI server / graph visualization
- Cypher query language
- Co-change frequency (requires git history integration)
- explore mode (premature)
- HITS authority/hub scores (C project uses, but InDegree covers similar ground)

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
- Parser uses regex for JS/TS/PHP (not tree-sitter) — findBlockEnd is naive with brace counting
- .gitignore only reads root file, not nested ones
- Go call edges only resolve within same file (cross-package calls create edges to non-existent nodes)
- HashEmbedder is a fallback — doesn't provide real semantic similarity
- sqlite-vec extension must be loaded for semantic search to work
- PersonalizedPageRank is an approximation (standard PageRank + post-hoc blending)
- gonum Betweenness doesn't support sampling (O(V*E) for large graphs)
- Betweenness scores become stale after incremental file updates (only recomputed on full indexRepo)
- Composite search signals (PPR, InDegree) are computed under separate RLocks — concurrent graph rebuilds can cause inconsistency between signals within a single search
- No re-index serialization — concurrent `index` tool calls can race
- ADR discoverer does not validate paths against symlinks pointing outside repo root

# qb-context — Project Knowledge Base

> Living document for team reference. Last updated: 2026-03-30.

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
│   ├── types/types.go              — ASTNode, ASTEdge, enums, ID generation
│   ├── watcher/watcher.go          — Filesystem watcher (fsnotify + debounce + gitignore)
│   ├── parser/parser.go            — Multi-language parser (Go native AST, regex for JS/TS/PHP)
│   ├── parser/queries/*.scm        — Tree-sitter query files (reference, for future use)
│   ├── storage/sqlite.go           — SQLite storage (WAL, FTS5, sqlite-vec)
│   ├── storage/migrations.go       — Schema creation
│   ├── embedding/engine.go         — Embedding engine (hash fallback, ONNX interface)
│   ├── embedding/model/embed.go    — Placeholder for ONNX model embedding
│   ├── graph/graph.go              — gonum directed graph (BFS, PageRank)
│   ├── search/hybrid.go            — Hybrid search (RRF fusion)
│   └── mcp/
│       ├── server.go               — JSON-RPC 2.0 MCP server over stdio
│       └── tools.go                — 5 MCP tool implementations
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

### Storage (`internal/storage`)
- SQLite with WAL mode, foreign keys, busy timeout, trusted_schema OFF
- Tables: `nodes`, `edges` (with CASCADE), `nodes_fts` (FTS5 with porter tokenizer), `node_embeddings` (vec0, cosine, 384-dim)
- Uses `ON CONFLICT DO UPDATE` for nodes (not INSERT OR REPLACE which cascades deletes)
- Uses `INSERT OR IGNORE` for edges
- `RawQuery`: SELECT-only, blocks load_extension/writefile/fts3_tokenizer, wrapped in read-only transaction
- `UpsertEmbedding` validates 384-dim, uses delete+insert for vec0 compatibility
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
- `PersonalizedPageRank`: PageRankSparse (0.85 damping) with post-hoc blending (approximation, not true PPR)
- Thread-safe with sync.RWMutex

### Search (`internal/search`)
- `HybridSearch`: dual-path search combining FTS5 lexical + KNN semantic
- Reciprocal Rank Fusion (k=60): score = Σ 1/(k + rank + 1) across lists
- PageRank boost: multiplies RRF score by (1 + rank*100) from active files

### MCP Server (`internal/mcp`)
- Custom JSON-RPC 2.0 over stdio (no third-party MCP SDK)
- Handles: initialize, tools/list, tools/call, ping, notifications/initialized
- Goroutine semaphore (max 10 concurrent handlers)
- `GetHandler(name)` and `GetTools()` for programmatic access (used by CLI tool)

### MCP Tools (`internal/mcp/tools.go`)
- `context` → HybridSearch.Search (query + limit + active files)
- `impact` → GraphEngine.BlastRadius + node enrichment
- `read_symbol` → Store.GetNode + byte-range file read (path traversal + symlink protected via EvalSymlinks)
- `query` → Store.RawQuery (SELECT-only, read-only transaction)
- `index` → full re-index pipeline callback

### CLI Tool (`cmd/qb-context/main.go`)
- `qb-context cli <tool_name> [json_args]` — direct tool invocation for testing/benchmarking
- `qb-context cli --list` — show available tools
- Boots full pipeline, indexes repo, calls handler directly, prints JSON to stdout
- Modeled after C project's `cli` subcommand

### Main Orchestrator (`cmd/qb-context/main.go`)
- Boot: config → storage → embedding → parser → graph → initial index → watcher → MCP server
- Worker pool for parallel file parsing during initial index
- Incremental updates via filesystem watcher callbacks
- Graceful shutdown on SIGINT/SIGTERM with sync.Once cleanup

---

## Key Libraries

| Library | Purpose |
|---------|---------|
| github.com/mattn/go-sqlite3 | SQLite driver (CGO) |
| github.com/fsnotify/fsnotify | Filesystem events |
| github.com/crackcomm/go-gitignore | .gitignore matching |
| gonum.org/v1/gonum v0.17.0 | Graph engine, PageRank, (future: Betweenness, Louvain) |
| (future) github.com/asg017/sqlite-vec-go-bindings | Vector search |
| (future) github.com/shota3506/onnxruntime-purego | ONNX inference |

---

## Completed Work — All 8 Commits

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

### Test Coverage
- `internal/types` — 6 tests (ID generation, enum values)
- `internal/storage` — 15 tests (CRUD, FTS5, search, raw query, cascade delete)
- `internal/parser` — 17 tests (Go/JS/TS/PHP parsing, edge extraction)
- `internal/embedding` — 5 tests (hash embedder, serialization, cosine similarity, empty string)
- `internal/graph` — 16 tests (BFS, cycles, depth limits, PageRank, DAG)
- `internal/search` — 4 tests (RRF fusion, PageRank boost, limits)
- `internal/mcp` — 6 tests (initialize, tools/list, tools/call, unknown method, concurrent)
- `tests/integration_test.go` — full pipeline (parse → store → embed → graph → search → delete)

### Devil's Advocate Reviews (3 completed)
- **Review #1** (after Commits 1-3): 9 issues found — SQL injection, FTS5 schema, CASCADE deletes, watcher race, PHP bugs, file size check. All fixed.
- **Review #2** (after Commit 4): 7 issues found — BFS direction, path traversal, byte range, notification responses, marshal errors. All fixed.
- **Review #3** (after Commits 5-6): 23 issues found — double-close panic (sync.Once), goroutine limit (semaphore), fd leak (walker.Stop), SQL injection hardening (trusted_schema OFF, blocklist), symlink bypass (EvalSymlinks), test improvements (cycles, concurrent, empty string, DAG PageRank). All fixed.

---

## Next Phase — 4 Features from C Project

> **Full plan with agent assignments, file changes, schema additions, and conflict matrix:**
> `.claude/plans/binary-baking-parasol.md`

### Feature 1: Louvain Community Detection
- **gonum API:** `community.Modularize(g, 1.0, nil)` → `ReducedGraph.Communities()` + `community.Q()` for modularity
- In-memory cached on GraphEngine (lazy via `sync.Once`), invalidated on `BuildFromEdges`
- Expose via `context` tool `mode: "architecture"`
- Returns community clusters with node details + modularity score
- **Files:** `graph.go` (DetectCommunities + cache fields), `tools.go` (mode param on context), `types.go` (Community struct)
- **Tests:** two-cluster detection, cache invalidation, empty graph

### Feature 2: Betweenness Centrality + Risk Levels (CRITICAL PATH)
- **gonum API:** `network.Betweenness(g)` — Brandes' algorithm, returns `map[int64]float64`
- Computed at INDEX TIME (after PageRank), normalized to [0,1], stored in new `node_scores` table
- New `BlastRadiusWithDepth()` returns `map[string]int` (hashID → hop depth)
- Impact tool restructured: hop 1=CRITICAL, 2=HIGH, 3=MEDIUM, 4+=LOW
- Returns grouped: `{risk_score, direct[], high_risk[], medium_risk[], low_risk[], affected_tests[], summary}`
- **Schema:** `node_scores (node_id TEXT PK, pagerank REAL, betweenness REAL, FK → nodes ON DELETE CASCADE)`
- **Files:** `graph.go`, `migrations.go`, `sqlite.go`, `tools.go`, `main.go`, `types.go`
- **Tests:** betweenness normalization, blast radius depths, node_scores CRUD, cascade delete

### Feature 3: ADR (Architecture Decision Records) Support
- New `internal/adr/adr.go` package — `Discoverer.Discover()` walks repo for ARCHITECTURE.md, ADR.md, adr/ dirs
- Reads content (max 8000 chars), computes SHA-256 hash, parses into sections (max 16)
- **Schema:** `project_summaries (project TEXT PK, summary TEXT, source_hash TEXT, created_at, updated_at)`
- Surfaced as `architecture_context` field in `context` tool responses
- Called during `indexRepo()` after graph build
- **Files:** `adr/adr.go` (new), `migrations.go`, `sqlite.go`, `tools.go`, `main.go`, `types.go`
- **Tests:** discover files, empty repo, max chars, CRUD roundtrip

### Feature 4: Multi-Signal Ranked Search Fusion (depends on Feature 2)
- Replace simple RRF + PageRank with composite scoring:
  ```
  composite = 0.35*PPR + 0.25*BM25 + 0.15*Betweenness + 0.10*InDegree + 0.15*SemanticSim
  ```
- All signals normalized to [0,1] before weighting
- PPR seeded from top 10 FTS results (query-time, not index-time)
- New `ComputeInDegree()` method on GraphEngine (count `g.dg.To(id).Len()`, normalize)
- FTS5 enhancements: prefix matching (`term*`), CamelCase splitting, stop word filtering, per-file cap (max 3)
- **Files:** `hybrid.go` (full refactor), `graph.go` (InDegree), `sqlite.go` (GetAllBetweenness)
- **Tests:** composite scoring, betweenness boost, CamelCase, per-file cap, normalization

### Execution Plan
```
Phase 1 (3 parallel Sonnet agents):
  Agent A: Feature 2 (Betweenness + Risk)  ██████████████
  Agent B: Feature 1 (Louvain Communities)  ████████████
  Agent C: Feature 3 (ADR Support)          ████████████

Phase 2 (1 sequential Sonnet agent, after Agent A):
  Agent D: Feature 4 (Ranked Fusion)                      ██████████████

Final: Devil's Advocate Opus review                                      ████████
```

### Shared File Conflict Matrix
| File | Agent A | Agent B | Agent C | Agent D |
|------|---------|---------|---------|---------|
| `graph.go` | ComputeBetweenness, BlastRadiusWithDepth | DetectCommunities + cache | — | ComputeInDegree |
| `tools.go` | Rewrite impact handler | Add mode to context | Add ADR to context | — |
| `migrations.go` | node_scores table | — | project_summaries table | — |
| `sqlite.go` | NodeScore CRUD | — | ProjectSummary CRUD | GetAllBetweenness |
| `types.go` | RiskLevel, NodeScore | Community | ProjectSummary | — |
| `main.go` | Betweenness in indexRepo | — | ADR discover in indexRepo | — |
| `hybrid.go` | — | — | — | Full refactor |

All conflicts are additive (new methods, new struct fields, appending to slices). No overlapping edits.

### Features Explicitly Skipped (for now)
- trace_call_path (covered by impact tool)
- get_key_symbols (achievable via query tool)
- HTTP UI server / graph visualization
- Cypher query language
- Co-change frequency (requires git history integration)
- explore mode (premature)

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

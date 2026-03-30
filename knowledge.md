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

---

## Architecture

### Project Structure
```
qb-context/
├── cmd/qb-context/main.go         — CLI entry point
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
- SQLite with WAL mode, foreign keys, busy timeout
- Tables: `nodes`, `edges` (with CASCADE), `nodes_fts` (FTS5 with porter tokenizer), `node_embeddings` (vec0, cosine, 384-dim)
- Uses `ON CONFLICT DO UPDATE` for nodes (not INSERT OR REPLACE which cascades deletes)
- Uses `INSERT OR IGNORE` for edges
- `RawQuery` enforces SELECT-only (SQL injection protection)
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
- Emits `FileEvent` on a buffered channel

### Embedding (`internal/embedding`)
- `Embedder` interface: Embed(text) → []float32, EmbedBatch, Close
- `HashEmbedder`: deterministic hash-based pseudo-embeddings (fallback)
- Future: ONNX Runtime via purego for all-MiniLM-L6-v2 (INT8 quantized, ~22MB)
- Utility: SerializeFloat32, DeserializeFloat32, CosineSimilarity

### Graph (`internal/graph`)
- gonum directed graph with string hash ID <-> int64 ID mapping
- `BlastRadius`: BFS traversing **incoming edges** (`g.dg.To()`) — finds dependents (who calls this node), not dependencies
- `PersonalizedPageRank`: PageRankSparse with blended personalization
- Thread-safe with sync.RWMutex

### Search (`internal/search`)
- `HybridSearch`: dual-path search combining FTS5 lexical + KNN semantic
- Reciprocal Rank Fusion (k=60): score = Σ 1/(k + rank + 1) across lists
- PageRank boost: multiplies RRF score by (1 + rank*100) from active files
- Returns ranked `SearchResult` (Node + Score)

### MCP Server (`internal/mcp`)
- Custom JSON-RPC 2.0 over stdio (no third-party MCP SDK)
- Handles: initialize, tools/list, tools/call, ping, notifications/initialized
- Silently ignores unknown notifications (no response for requests without ID)
- Handles marshal errors in tool responses
- 5 tools fully wired: context, impact, read_symbol, query, index

### MCP Tools (`internal/mcp/tools.go`)
- `context` → HybridSearch.Search (query + limit + active files)
- `impact` → GraphEngine.BlastRadius + node enrichment
- `read_symbol` → Store.GetNode + byte-range file read (path traversal protected)
- `query` → Store.RawQuery (SELECT-only)
- `index` → full re-index pipeline callback

### Main Orchestrator (`cmd/qb-context/main.go`)
- Boot: config → storage → embedding → parser → graph → initial index → watcher → MCP server
- Worker pool for parallel file parsing during initial index
- Incremental updates via filesystem watcher callbacks
- Graceful shutdown on SIGINT/SIGTERM

---

## Key Libraries

| Library | Purpose |
|---------|---------|
| github.com/mattn/go-sqlite3 | SQLite driver (CGO) |
| github.com/fsnotify/fsnotify | Filesystem events |
| github.com/crackcomm/go-gitignore | .gitignore matching |
| gonum.org/v1/gonum | Graph engine + PageRank |
| (future) github.com/asg017/sqlite-vec-go-bindings | Vector search |
| (future) github.com/shota3506/onnxruntime-purego | ONNX inference |
| (future) github.com/odvcencio/gotreesitter | Pure Go Tree-sitter |

---

## Completed Work

### Commit 1: Project Scaffold
- Go module, directory structure, core types, config, main skeleton

### Commit 2: Storage + Watcher + Parser (3 parallel Sonnet agents)
- SQLite storage layer with FTS5 + vec0
- Filesystem watcher with debouncing + gitignore
- Multi-language parser (Go/JS/TS/PHP)

### Commit 3: Embedding + Graph + MCP Scaffold (3 parallel Sonnet agents)
- Hash-based embedding engine with Embedder interface
- gonum graph engine with BFS + PageRank
- MCP JSON-RPC server with 5 tool stubs

### Lint Setup
- .golangci.yml created with errcheck, govet, gosec, etc.
- Fixed: unused variables, stdout→stderr in migrations, promoted jsKeywords to package level

### Tests for Commits 1+2
- types_test.go: 6 tests (all pass)
- parser_test.go: 17 tests (all pass)
- storage_test.go: 14 tests (need -tags "fts5" to pass)

### Devil's Advocate Review (Opus model)
Critical issues identified and fixed:
- C1: SQL injection in RawQuery → SELECT-only enforcement
- C2: FTS5 schema broken → being fixed by agent
- C3: INSERT OR REPLACE cascade deletes → ON CONFLICT DO UPDATE
- H2: Watcher race condition → being fixed by agent (stopped flag)
- H3: PHP multi-class bug → being fixed by agent (byte range check)
- H5: No embedding dimension validation → 384-dim check added
- H6: No file size check → being fixed by agent (5MB limit)
- L5: stdout→stderr in migrations → fixed
- M3: isJSKeyword map per-call → promoted to package level

---

## All Commits Complete

All 6 commits landed, all tests pass (`go test -tags "fts5" ./... -count=1`).

### Commit 4: Hybrid Search + MCP Wiring (2 parallel Sonnet agents)
- Hybrid search: FTS5 + KNN + RRF fusion + PageRank boost
- All 5 MCP tools wired to real implementations

### Commit 5: Orchestrator + Security Fixes
- Full main.go boot sequence with worker pool, watcher, graceful shutdown
- BFS blast radius direction fix (incoming edges)
- Path traversal protection in read_symbol
- Byte range validation (uint32 underflow, 5MB cap)
- MCP notification silence + marshal error handling

### Commit 6: Tests
- Unit tests: embedding (4), graph (6), MCP server (5), hybrid search (4)
- Integration test: full pipeline (parse → store → embed → graph → search → delete)

### Devil's Advocate Reviews
- **Review #1** (after Commits 1-3): 9 issues found, all fixed
- **Review #2** (after Commit 4): 7 issues found, all fixed in Commit 5
- **Review #3** (after Commits 5-6): in progress

---

## Process & Workflow

### Agent Strategy
- **Sonnet** agents for all code implementation and fixes
- **Opus** Devil's Advocate agent reviews each commit
- Agents run in parallel within each commit phase
- Each commit is a clean, self-contained unit

### Build & Test
```bash
go build ./...                        # compilation check
go test -tags "fts5" ./...            # run all tests (FTS5 requires build tag)
golangci-lint run ./...               # lint (needs manual install)
```

### Known Limitations
- Parser uses regex for JS/TS/PHP (not tree-sitter) — findBlockEnd is naive with brace counting
- .gitignore only reads root file, not nested ones
- Go call edges only resolve within same file (cross-package calls create edges to non-existent nodes)
- HashEmbedder is a fallback — doesn't provide real semantic similarity
- sqlite-vec extension must be loaded for semantic search to work

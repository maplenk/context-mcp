# qb-context — Project Knowledge Base

> Living document for team reference. Last updated: 2026-04-04 (post-Phase 13 — Search quality optimization: Wave 1+2 improvements + 4-phase parameter sweep + DA review fixes, B+C 33.3% → 53.1%).

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
├── cmd/qb-context/main.go         — CLI entry + MCP daemon + CLI tool subcommand + indexPath + ONNX init + Cold Start
├── internal/
│   ├── config/config.go            — Config with CLI flags (incl. --onnx-model, --onnx-lib, --embedding-dim, --cold-start, --git-*)
│   ├── types/types.go              — ASTNode, ASTEdge, enums, RiskLevel, NodeScore, Community, ProjectSummary
│   ├── watcher/watcher.go          — Filesystem watcher (fsnotify + debounce + nested gitignore + hot-reload)
│   ├── watcher/watcher_test.go     — 11 watcher tests (create/modify/delete, debounce, gitignore)
│   ├── parser/parser.go            — Multi-language parser (Go native AST, tree-sitter for JS/TS/PHP via go-tree-sitter)
│   ├── parser/queries/*.scm        — Tree-sitter S-expression query files (reference)
│   ├── storage/sqlite.go           — SQLite storage (WAL, FTS5, sqlite-vec statically linked, configurable embedding dim)
│   ├── gitmeta/gitmeta.go          — Git metadata extraction via go-git (snapshot, commits, file history, intent compaction)
│   ├── gitmeta/helpers.go          — Git helpers (diffTrees, truncateBytes, isLowSignalCommit, extractTrailersJSON)
│   ├── gitmeta/gitmeta_test.go     — 21 gitmeta tests
│   ├── storage/migrations.go       — Versioned schema migrations (v3: git metadata tables)
│   ├── embedding/engine.go         — Embedding interface (Dim() method), TFIDFEmbedder default, variable EmbeddingDim
│   ├── embedding/tokenizer.go      — Pure Go BPE tokenizer (HuggingFace tokenizer.json, 151K vocab, byte-level)
│   ├── embedding/onnx.go           — ONNXEmbedder (build tag: onnx) — purego ONNX Runtime (no CGO), Qwen2 model, last-token pooling, Matryoshka
│   ├── embedding/onnx_stub.go      — Stub for non-ONNX builds
│   ├── embedding/model/embed.go    — Model metadata (Qwen2, Matryoshka dims, INT8)
│   ├── graph/graph.go              — gonum directed graph (true PPR, subgraph PPR, BFS, Betweenness, Louvain, InDegree cache, TraceCallPath)
│   ├── search/config.go            — SearchConfig struct (22 tunable params) + DefaultConfig()
│   ├── search/hybrid.go            — Multi-signal composite search (PPR, BM25, betweenness, semantic, query-kind boost, graph expansion, per-file cap)
│   ├── adr/adr.go                  — ADR discoverer (with symlink boundary validation)
│   └── mcp/
│       ├── server.go               — mcp-golang SDK server over stdio
│       ├── tools.go                — 13 CLI tools, 5 SDK tools (context, impact, read_symbol, query, index)
│       └── tools_test.go           — 35 tool tests
├── tests/
│   ├── integration_test.go         — Full pipeline integration test (synthetic files)
│   ├── incremental_test.go         — Incremental update pipeline tests
│   ├── concurrent_test.go          — Concurrency and race condition tests
│   ├── realrepo_test.go            — Real-repo integration test against qbapi (build tag: realrepo)
│   ├── benchmark_grading_test.go   — Automated benchmark grading (15 queries, B+C threshold guard)
│   └── param_sweep_test.go         — Parameter sweep harness (Phases 1-4, ~130 configs, SWEEP=1 gated)
├── .golangci.yml                   — Linter configuration
├── go.mod / go.sum
└── knowledge.md                    — This file
```

### Core Types (`internal/types`)
- `NodeType` uint8: Function(1), Class(2), Struct(3), Method(4), Interface(5), File(6)
- `EdgeType` uint8: Calls(1), Imports(2), Implements(3), Instantiates(4), Defines(5), DefinesMethod(6), Inherits(7)
- `ASTNode`: ID (SHA-256 of path + null byte + symbol), FilePath, SymbolName, NodeType, StartByte, EndByte, ContentSum
- `ASTEdge`: SourceID, TargetID, EdgeType, TargetSymbol (optional — raw symbol name for cross-file resolution)
- `FileEvent`: Path, Action (Created/Modified/Deleted)
- `SearchResult`: Node, Score, Breakdown (*ScoreBreakdown, omitempty)
- `ScoreBreakdown`: PPR, BM25, Betweenness, InDegree, Semantic (per-signal normalized scores)
- `Inspectable`: Rank, TargetType, Name, FilePath, ID, Score, Reason, WhyNow, NextTool, NextArgs (ranked discovery result)
- `InspectableResponse`: Inspectables, Total, Query, Summary (wraps discovery tool output)
- `RiskLevel` string: CRITICAL, HIGH, MEDIUM, LOW (hop-based impact classification)
- `NodeScore`: NodeID, PageRank, Betweenness (precomputed graph metrics)
- `Community`: ID, NodeIDs (Louvain cluster membership)
- `ProjectSummary`: Project, Summary, SourceHash (ADR/architecture documents)

### Storage (`internal/storage`)
- SQLite with WAL mode, foreign keys, busy timeout, trusted_schema OFF
- Tables: `nodes`, `edges` (**FK removed in migration v2**), `nodes_fts` (FTS5), `node_embeddings` (vec0), `node_scores`, `project_summaries`, `repo_git_snapshot`, `git_commits`, `git_file_history`, `git_file_intent`, `schema_version`
- Versioned migrations: `schema_version` table tracks current version, currently at **v5**
- **Migration v3** (Cold Start): 4 new Git metadata tables — `repo_git_snapshot` (one-row repo state), `git_commits` (bounded commit history), `git_file_history` (file-commit associations), `git_file_intent` (compacted intent summaries)
- **Migration v4**: FK removal from edges table
- **Migration v5** (Search Quality): `search_terms` column added to FTS5 — new 5-column FTS table (symbol_name, file_path, content_sum, search_terms, node_id UNINDEXED). Post-migration backfill hook populates search_terms via `BuildSearchTerms()` for all existing nodes
- Git storage methods: `UpsertRepoSnapshot`, `GetRepoSnapshot`, `UpsertGitCommits`, `UpsertFileHistory`, `UpsertFileIntents`, `GetFileIntent`, `GetFileIntentsByPaths`, `GetLatestStoredCommitHash`
- `NewStore()` accepts optional `embeddingDim` parameter (default 384, ONNX models use e.g. 256)
- **sqlite-vec statically linked** via `asg017/sqlite-vec-go-bindings/cgo` (Blueprint Alignment)
  - `sqlite_vec.Auto()` called once via `sync.Once` before DB open
  - vec0 creation failure is now a fatal error (sqlite-vec always available)
- `hasVecTable` flag: always true now (kept as defense-in-depth)
- `SearchSemantic()` gracefully returns nil when vec table unavailable (legacy fallback)
- `UpsertEmbedding()` is a no-op when vec table unavailable (legacy fallback)
- FTS sync errors now properly checked in UpsertNode/UpsertNodes
- `GetNodeByName` uses `ORDER BY file_path, id` for deterministic results
- `GetNodeIDsByFile`, `GetAllFilePaths`, `SearchNodesByName`, `GetNodesByFile`, `GetAllNodeScores` helpers added
- Uses `ON CONFLICT DO UPDATE` for nodes (not INSERT OR REPLACE which cascades deletes)
- Uses `INSERT OR IGNORE` for edges
- `RawQuery`: SELECT-only, rejects `;` (multi-statement), blocks load_extension/writefile/readfile/edit/fts3_tokenizer/attach, uses `BeginTx(ReadOnly: true)` for proper transaction isolation, word-boundary LIMIT injection
- `UpsertNode`: **transactional** (INSERT + FTS DELETE + FTS INSERT wrapped in tx)
- `UpsertEmbedding`: **transactional** with proper error handling on DELETE
- `UpdateFTS`: DELETE error no longer swallowed
- `DeleteByFile`: explicitly deletes `node_scores` before nodes
- `SearchLexical`: FTS5 input sanitized at storage layer
- Connection pool configured: `SetMaxOpenConns(4)`, `SetMaxIdleConns(2)` for SQLite WAL
- Migration vec0 table uses **configurable embedding dim** (not hardcoded 384)
- **Build tag**: Requires `-tags "fts5"` for FTS5 support with go-sqlite3

### Parser (`internal/parser`)
- Go files: uses `go/parser` + `go/ast` (native, accurate), extracts import edges, **type aliases and named types** now captured
- Go interfaces use `NodeTypeInterface` (not NodeTypeClass)
- **JS/TS/PHP files: tree-sitter AST parsing** via `smacker/go-tree-sitter` (CGO, wraps real C tree-sitter library)
  - Replaced pure-Go `go-tree-sitter` which stack-overflowed on large PHP files (500KB+) due to Go goroutine stack recursion in GLR parser
  - C tree-sitter manages its own heap-allocated parse stack — handles any file size
  - JS/JSX: `javascript.GetLanguage()`, TS: `typescript.GetLanguage()`, TSX: `tsx.GetLanguage()`
  - PHP: `php.GetLanguage()` — classes, methods, functions, use statements, instantiation, call edges
  - Tree-sitter gives exact byte offsets (StartByte/EndByte) from AST nodes
  - TypeScript-specific: interface→NodeTypeInterface, enum→NodeTypeStruct, type alias→NodeTypeFunction
  - Call edges still use regex on node body text for reliability (jsCallExprRe, phpMethodCallRe etc.)
- **Structural edges emitted from all parsers** (Phase 8):
  - DEFINES (file→class/function/type) — Go, JS/TS, PHP
  - DEFINES_METHOD (class/struct→method) — Go (via receiver type), JS/TS, PHP
  - INHERITS (class→parent) — JS/TS (via `superclass` field), PHP (via `base_clause` child)
  - IMPLEMENTS (class→interface) — PHP (via `class_interface_clause` child)
  - INHERITS/IMPLEMENTS edges include `TargetSymbol` for cross-file resolution
  - PHP namespace stripping: `\App\Models\Base` → `Base` for symbol lookup
- PHP methods without visibility keywords now detected (defaults to public)
- PHP deduplication via `seen` map — standalone function regex no longer duplicates class methods
- **File-level nodes** (NodeTypeFile) created for every parsed file — import edges now have valid source/target nodes in the graph, fixing graph connectivity
- Import edge TargetIDs use `GenerateNodeID(importPath, importPath)` — points to target file's file node
- **Call edge deduplication** via `callSeen` maps in Go, JS, and PHP parsers
- `findBlockEnd()`: state-machine tracking 7 states, **template literal `${...}` interpolation** handled with nested brace tracking
- `findBlockEnd()` fallback returns `len(content)` instead of arbitrary `startPos + 5000`
- `buildContentSum()`: blank-line check prevents capturing unrelated doc blocks; Go functions include param types
- `GenerateNodeID()` uses **null byte separator** (prevents `"a:b" + "c"` == `"a" + "b:c"` collisions)
- File size check: skips files > 5MB
- Tree-sitter .scm query files kept as reference for future integration

### Watcher (`internal/watcher`)
- fsnotify for OS-level events, recursive directory watching
- Debounce with configurable window (default 500ms) + improved coalescing (CREATE+WRITE→CREATE, DELETE always wins)
- **Nested .gitignore** support via go-gitignore (root + discovered during walk)
- **Hot-reload** of .gitignore files on modification (M10)
- `Stop()` is **idempotent** via `sync.Once` — safe to call multiple times
- **Gitignores slice** protected by mutex in `Start()` and `isExcluded()` — prevents race on concurrent access
- `WalkSourceFiles()` standalone function — no fsnotify allocation needed (L4)

### Embedding (`internal/embedding`)
- `Embedder` interface: `Embed(text) → []float32`, `EmbedBatch`, `Dim() int`, `Close`
- `EmbeddingDim` is an **atomic variable** (`sync/atomic.Int32`) with `GetEmbeddingDim()`/`SetEmbeddingDim()` accessors — thread-safe
- `TFIDFEmbedder` (default): word/subword tokenization with CamelCase splitting, TF-IDF weighting, random projection to 384-dim. Provides real semantic locality (similar code → similar vectors)
- `HashEmbedder`: deterministic hash-based pseudo-embeddings (last-resort fallback, stateless)
- **`ONNXEmbedder`** (build tag: `onnx`): Runs quantized Qwen2 model via ONNX Runtime
  - Pure Go BPE tokenizer reads HuggingFace `tokenizer.json` (151K vocab, byte-level encoding, NFC normalization)
  - Handles both array `[["a","b"],...]` and string `["a b",...]` merge formats
  - Pre-tokenization regex adapted for Go RE2 (no negative lookahead)
  - Last-token pooling (causal/decoder-only model)
  - **Matryoshka dimension truncation**: 64, 128, 256, 512, or 896 (default 256)
  - L2 normalization, thread-safe via mutex
  - Semantic quality: `sim(ReadFile, ReadFileContents) = 0.69`, `sim(ReadFile, SQL) = 0.17`
  - Model: `/Users/naman/Documents/coindex/quantized_model` (473MB, INT8, Qwen2ForCausalLM)
- `NewEmbedder()` factory returns TFIDFEmbedder; main.go tries ONNX first if `--onnx-model` configured
- **Trigram/bigram generation** operates on `[]rune` (not `[]byte`) for correct Unicode handling
- BPE tokenizer tracks **unknown token drops** with atomic counter + periodic warning log (every 1000th)
- ONNX session: **tensor leak fix** — partially allocated output tensors cleaned up on error paths
- ONNX hidden dimension **derived at runtime** from output shape (fallback to 896)
- Utility: SerializeFloat32, DeserializeFloat32, CosineSimilarity
- **Build**: `go build -tags "fts5,onnx" ./...` for ONNX support; `go build -tags "fts5" ./...` for TFIDF only

### Graph (`internal/graph`)
- gonum v0.17.0 directed graph with string hash ID <-> int64 ID mapping
- `BlastRadius`: BFS traversing **incoming edges** (`g.dg.To()`) — finds dependents (who calls this node)
- `BlastRadiusWithDepth`: same as BlastRadius but returns `map[string]int` (hashID → hop depth)
- `PersonalizedPageRank`: **TRUE PPR** via custom power iteration with teleportation vector seeded to active nodes (damping 0.85, epsilon 1e-6, max 100 iterations). Dangling nodes distribute rank to teleportation vector.
- `PageRank`: standard (non-personalized) PageRank via power iteration
- `ComputeBetweenness`: Brandes' algorithm via `network.Betweenness()`, normalized using **graph-theoretic maximum** `(n-1)*(n-2)` for directed graphs (comparable across graph sizes)
- `DetectCommunities`: Louvain via `community.Modularize()`, cached with invalidation on any graph mutation
- `ComputeInDegree`: in-degree authority per node, normalized to [0,1], **cached with invalidation** on mutations. **Returns copy on cache miss** (not live reference)
- `ComputeSearchSignals`: computes PPR + InDegree using **RLock with upgrade-to-Lock** pattern for search consistency
- `GetConnectors`: checks `communityValid` flag before reading community data — prevents stale results
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
- Multi-signal composite scoring (optimized via 4-phase parameter sweep):
  ```
  composite = 0.35*PPR + 0.30*BM25 + 0.20*Betweenness + 0.00*InDegree + 0.15*SemanticSim
  ```
- All signals normalized to [0,1] before weighting
- **Helper file penalty**: `composite *= 0.3` for `_ide_helper`, `.d.ts`, `generated` files — prevents auto-generated files from dominating results
- **Snapshot-based signal fetching**: PPR + InDegree computed under single lock via `ComputeSearchSignals()` (no race conditions)
- PPR seeded from top 10 FTS results + active file nodes (query-time, not index-time)
- FTS5 enhancements: prefix matching (`term*`), CamelCase splitting, stop word filtering, FTS5 special char sanitization (includes `*` wildcard), **boolean operator neutralization** (OR/AND/NOT/NEAR lowercased)
- **Min-max normalization** for all scores (handles negative cosine similarity correctly)
- PPR seeds **deduplicated** before computation
- CamelCase regex includes **digits** (`[0-9]+`) for identifiers like `HTTP2Client`
- Per-file cap: configurable via `max_per_file` parameter (default 3), helper files capped at 1
- Stop words: configurable via `SetStopWords()` function

### MCP Server (`internal/mcp`)
- **mcp-golang SDK** (github.com/metoro-io/mcp-golang v0.16.1) over stdio transport
- Full MCP capability negotiation (tools, resources, prompts)
- Dual registration: SDK typed handlers for MCP protocol + ToolHandler (json.RawMessage) for CLI mode
- `GetHandler(name)` and `GetTools()` for programmatic access (used by CLI tool)
- Supports: initialize, tools/list, tools/call, resources/list, resources/read, prompts/list, prompts/get, ping, notifications

### MCP Tools (`internal/mcp/tools.go`) — 13 tools
- `context` → HybridSearch.Search (query + limit + max_per_file + active files), supports `mode: "architecture"`, includes `architecture_context` from ADR documents, **`git_context`** (repo snapshot + file intents for result files)
- `impact` → BlastRadiusWithDepth + risk levels (hop 1=CRITICAL, 2=HIGH, 3=MEDIUM, 4+=LOW), betweenness risk_score, affected_tests, structured summary
- `read_symbol` → Store.GetNode + byte-range file read (path traversal + symlink protected via EvalSymlinks)
- `query` → Store.RawQuery (SELECT-only, read-only transaction with BeginTx)
- `index` → full or targeted re-index (respects `path` parameter via `indexPath()`)
- `trace_call_path` → bidirectional BFS path finding between two symbols
- `get_key_symbols` → top-K symbols by PageRank with in/out degree stats, optional file filter
- `search_code` → regex-based code search across indexed files with path traversal protection
- `detect_changes` → git-based file/symbol change detection with git ref validation, **`file_intents`** for changed files
- `get_architecture_summary` → enhanced Louvain + entry points + hubs + connectors
- `explore` → multi-search with dependency/dependent/hotspot analysis
- `understand` → 3-tier symbol resolution (exact → fuzzy → FTS) with callers/callees/PageRank/community, **`file_intent`** for symbol's file
- `health` → daemon status (node count, edge count, version)
- **Tool registration**: errors logged (not silently discarded), duplicate names prevented
- **Response size cap**: 1MB limit on `toToolResponse` marshaled output
- **PageRank caching**: `understand` and `get_key_symbols` use stored scores, fall back to recomputation
- **detect_changes**: includes `status` field (file_modified/deleted/new_file) per symbol, tightened git ref regex

### CLI Tool (`cmd/qb-context/main.go`)
- `qb-context cli <tool_name> [json_args]` — direct tool invocation for testing/benchmarking
- `qb-context cli --list` — show available tools
- Boots full pipeline, indexes repo, calls handler directly, prints JSON to stdout
- Modeled after C project's `cli` subcommand

### Main Orchestrator (`cmd/qb-context/main.go`)
- Boot: config → **embedder (ONNX if configured, else TFIDF)** → storage (with embedding dim) → parser → graph → initial index (+ cross-file resolution + betweenness + ADR + **Cold Start**) → watcher → MCP server
- Worker pool for parallel file parsing during initial index
- **Cross-file edge resolution** in `indexRepo()`: builds symbol→nodeID index from class/struct/interface nodes, resolves dangling INHERITS/IMPLEMENTS edges using `TargetSymbol` field before storing
- Betweenness centrality computed and stored in `node_scores` at index time
- ADR documents discovered and stored in `project_summaries` at index time
- **Incremental graph updates** via filesystem watcher: RemoveNode/AddEdge instead of full rebuild
- **Async betweenness refresh** after 20 incremental changes, acquires `indexMu`
- **Batch embedding recovery**: falls back to per-item embedding on batch failure
- `indexPath()` for targeted re-indexing — **path traversal protection**, skips excluded dirs, recomputes betweenness/PageRank
- `handleFileEvents()` acquires `indexMu` per event via `processFileEvent` helper — prevents concurrent index corruption
- **Memory monitor** goroutine uses `memDone` channel for clean shutdown (no goroutine leak)
- Graceful shutdown on SIGINT/SIGTERM with sync.Once cleanup
- ONNX config validation: warns on invalid settings at startup
- **Cold Start** (Phase 9): Git metadata ingestion via `go-git` in `indexRepo()`, `indexPath()`, and `processFileEvent()`:
  - Repo snapshot: branch/HEAD, dirty status, staged/modified/untracked counts
  - Commit history: bounded walk (default 500 commits), subject/body/trailers
  - File history: file-commit associations with per-file cap (default 20)
  - Intent compaction: dedup, low-signal filtering, bounded text generation
  - Embedding enrichment: `[git-intent]` text appended to `ContentSum` for semantic retrieval
  - Context-aware with timeouts (60s for indexRepo, 30s for indexPath)
  - Graceful degradation: disabled silently for non-git directories, logs errors without blocking

---

## Key Libraries

| Library | Purpose |
|---------|---------|
| github.com/mattn/go-sqlite3 | SQLite driver (CGO) |
| github.com/fsnotify/fsnotify | Filesystem events |
| github.com/go-git/go-git/v5 v5.17.2 | Pure Go Git implementation (repo open, log, diff, status, worktree) |
| github.com/crackcomm/go-gitignore | .gitignore matching |
| gonum.org/v1/gonum v0.17.0 | Graph engine, PageRank, Betweenness, Louvain community detection, InDegree |
| github.com/metoro-io/mcp-golang v0.16.1 | MCP SDK (stdio transport, tool/resource/prompt support) |
| github.com/shota3506/onnxruntime-purego | ONNX Runtime pure Go bindings (purego, no CGO, build tag: onnx) |
| github.com/smacker/go-tree-sitter | Tree-sitter CGO bindings for JS/TS/PHP (wraps real C tree-sitter, bundled grammars) |
| github.com/asg017/sqlite-vec-go-bindings/cgo v0.1.6 | sqlite-vec statically linked (vec0 always available) |
| golang.org/x/text v0.35.0 | Unicode NFC normalization for BPE tokenizer |

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

### Phase 4: DA Review #6 + ONNX Embedder (Commits 24-25)

| # | Hash | Description | Status |
|---|------|-------------|--------|
| 24 | `03ed37b` | DA Review Wave 1 — 22 fixes (C4, H1-H7, M1, M3-M10, L2-L5) | Done |
| 25 | `afc28f7` | ONNX embedder with Qwen2 model support (C2) | Done |

### Phase 5: DA Review #7 Full Fix Sprint (Commits 26-31)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 26 | `3126a83` | Parser + Types — 16 issues (C1, C11, H2-H5, H21-H22, M9-M15, L17) | Agent A (Opus) | Done |
| 27 | `38b4ea9` | Storage Security — 15 issues (C7-C8, C10, H6-H8, M1-M8, M32) | Agent B (Opus) | Done |
| 28 | `3facd0c` | Race Conditions + Main — 15 issues (C9, C14-C16, H10-H12, H20, M17, M20-M21, L18-L19) | Agent C (Opus) | Done |
| 29 | `896e919` | Search + Hybrid + Graph — 14 issues (C12-C13, C18, H1, H19, M27-M30, L4, L21) | Agent D (Opus) | Done |
| 30 | `664112d` | Tools + Server — 22 issues (H13-H18, H23-H24, M22-M26, M31, L14-L16, L23) | Agent E (Opus) | Done |
| 31 | `5241fd0` | Test Improvements — 13 issues (L1-L3, L5-L13, L22) | Agent F (Opus) | Done |

### Phase 6: Blueprint Alignment (Commits 32-37)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 32 | `53e24f7` | Replace yalue/onnxruntime_go (CGO) with shota3506/onnxruntime-purego | Opus (worktree) | Done |
| 33 | `58a5020` | Consolidate MCP SDK tools from 13 to 5 blueprint tools | Opus (worktree) | Done |
| 34 | `f8468bd` | Add sqlite-vec CGO bindings for guaranteed vec0 tables | Opus (worktree) | Done |
| 35 | `0ee152b` | Replace regex JS/TS/PHP parsers with tree-sitter (go-tree-sitter) | Opus (worktree) | Done |
| 36 | `ede6fcf` | DA review fixes + real-repo integration tests (22 subtests) | Opus (worktree) | Done |

### Phase 7: Search Quality & Speed (Commit 37)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 37 | `caf8d0f` | Subgraph PPR, structural edges (DEFINES/DEFINES_METHOD/INHERITS), BM25 10x name weight, domain stop words, helper file cap, JS extends, DA fixes | 4 Opus agents | Done |

### Phase 8: Cross-File Edge Resolution (Commits 38-39)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 38 | `f9dfebf` | Fix nested dir matching in shouldSkip + helper file score penalty (0.3x) | Opus (worktree) | Done |
| 39 | `9e637b9` | TargetSymbol cross-file edge resolution + structural edge emission from all parsers | Opus + orchestrator | Done |

**Key changes:**
- `TargetSymbol` field on `ASTEdge` — stores raw symbol name for INHERITS/IMPLEMENTS edges
- Cross-file edge resolution pass in `indexRepo()`, `realrepo_test.go`, `integration_test.go` — builds symbol→nodeID index from class/struct/interface nodes, resolves dangling targets before FK filtering
- Structural edges emitted from Go/JS/PHP tree-sitter parsers: DEFINES, DEFINES_METHOD, INHERITS (with TargetSymbol), IMPLEMENTS (with TargetSymbol)
- `shouldSkip` rewritten to match path components (catches nested `node_modules/`)
- Helper file score penalty: `composite *= 0.3` for `_ide_helper`, `.d.ts`, `generated` files

### Phase 9: Cold Start Enhancement (Commits 40-43)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 40 | `e2cecd6` | Cold Start gitmeta package + go-git + config flags | Opus (worktree) | Done |
| 41 | `ae72156` | Schema migration v3 + storage CRUD for git tables | Opus (worktree) | Done |
| 42 | `a39e052` | Integrate Cold Start into indexRepo/indexPath + MCP tool enhancements | 2 Opus agents | Done |
| 43 | `b1caa27` | Fix H1-H4: rows.Err, embedding intent, git error handling, stop sentinel + M1-M7 | 2 Opus agents | Done |

**Key changes:**
- `internal/gitmeta` package: pure Go Git metadata extraction via go-git (repo snapshot, commit history, file history walking, intent compaction, low-signal commit filtering)
- Schema migration v3: `repo_git_snapshot`, `git_commits`, `git_file_history`, `git_file_intent` tables
- Cold Start in `indexRepo()`: snapshot → commits → file history → intent compaction → embedding enrichment
- Cold Start in `indexPath()`: incremental file history + intent refresh for changed files (30s context timeout)
- Cold Start in `processFileEvent()`: embedding text enriched with file intent
- Embedding enrichment: `[git-intent]` text appended to `ContentSum` for semantic retrieval
- MCP `context` tool: `git_context` field with repo snapshot + file intents
- MCP `detect_changes` tool: `file_intents` for changed files
- MCP `understand` tool: `file_intent` for symbol's file
- Config flags: `--cold-start`, `--git-history-depth`, `--git-per-file-cap`, `--git-max-message`, `--git-max-intent`
- DA review #11: 0 CRITICAL, 4 HIGH (all fixed), 8 MEDIUM (7 fixed), 8 LOW

### Phase 10: Harness-First MCP Phase 1 (Commits 44-46)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 44 | `5a355de` | Inspectable contract + ScoreBreakdown + context handler refactor | Opus (worktree) | Done |
| 45 | `356b010` | Architecture summary handler refactor — ranked top-5, drop member lists | Opus (worktree) | Done |
| 46 | `0be1a8f` | Fix 3 HIGH + 2 MEDIUM DA review issues | Opus (worktree) | Done |

**Key changes:**
- New types: `ScoreBreakdown`, `Inspectable`, `InspectableResponse` — shared contract for all discovery tools
- `SearchResult.Breakdown` (*ScoreBreakdown): per-signal scores (PPR, BM25, Betweenness, InDegree, Semantic) populated in hybrid search loop
- `contextHandler` refactored: returns `InspectableResponse` (default limit 5), per-result `why_now` from file intents, `reason` from top-2 score signals, `next_tool`/`next_args` for tool chaining, removed `architecture_context` blob and raw `git_context`
- `architectureSummaryHandler` refactored: pools entry points/hubs/connectors, ranks by composite (0.4*normalized_pagerank + 0.3*betweenness + 0.3*normalized_degree), returns `InspectableResponse` top 5 — drops full community member lists (5-100KB → <3KB)
- `generateReason()` helper: picks top 2 non-zero score signals for human-readable explanation
- DA review #12: 0 CRITICAL, 3 HIGH (all fixed: rank gap, omitempty struct, unnormalized PageRank), 4 MEDIUM (2 fixed: Total mismatch, file-node routing), 5 LOW
- Benchmarks: all 26 queries pass, search quality 7/7, CLI tools 9/9

### Phase 11: Security Workflow Hardening (working tree)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 47 | `working tree` | Harden `.github/workflows/security.yml` to act as an enforcing security gate instead of report-only CI | Opus agents + orchestrator | Done |

**Key changes:**
- `security.yml` now runs on weekly schedule (`0 6 * * 1`) in addition to push/PR/manual dispatch to catch newly disclosed dependency CVEs without a code change
- `govulncheck` and `gosec` installs are pinned (`v1.1.4`, `v2.22.11`) and findings now fail the workflow instead of being swallowed by `@latest` drift or `|| true`
- Go jobs use `persist-credentials: false` on checkout plus `go mod download && go mod verify` so module verification is meaningful on isolated cold runners
- Trivy table output now fails on HIGH/CRITICAL findings, while the SARIF run remains non-blocking so results still reach the GitHub Security tab
- `aquasecurity/trivy-action` is SHA-pinned and `github/codeql-action/upload-sarif` is pinned to `38697555549f1db7851b81482ff19f1fa5c4fedc # v4` using the reusable pattern from the C project workflows
- SARIF uploads are guarded for fork PR permission downgrades and missing output files via `hashFiles(...)` checks, so upload steps do not mask the real scanner failure reason
- DA review #13: no CRITICAL, HIGH, or MEDIUM issues remaining in the final workflow

### Phase 12: Search Quality Waves 1+2 (Commits 48-50)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 48 | `ab13dae` | Wave 1: search_terms FTS column, prefix aliases, query-kind boost, path penalties | Opus (worktree) | Done |
| 49 | `869ce3f` | Wave 2: re-enable graph expansion with query-relevance filter, increase result limits | Opus (worktree) | Done |
| 50 | `615bb5f` + `524e6be` | SearchConfig struct + parameter sweep harness | 2 Opus agents | Done |

**Key changes (Wave 1):**
- **search_terms FTS column** (migration v5): CamelCase-split + file basename tokens for better BM25 matching (credit: C reference v0.8.0 autoresearch)
- **Query kind detection** in `hybrid.go`: route terms ("api", "endpoint", "route", "login"), PascalCase → class, under_score → function, default → natural
- **Node-type boosts**: route→2.5x, routeMethod→1.3x, class→1.5x, function→1.2x applied post-composite
- **Prefix alias matching**: `strings.HasPrefix(term, aliasKey)` with min key length 3 — "authentication" matches "auth"
- **New aliases**: loyalty, session, sync, database; extended auth with "middleware"
- **Stop words added**: flow, happens, places, every, there, also, each, them, they, up, than, handling
- **Path penalties tightened**: tests 0.5→0.3, migrations 0.4→0.2, added config→0.8
- **BM25 score floor (0.05)** prevents min-max normalization from crushing low-frequency node types
- **BM25 column weights**: symbol=10.0, filePath=1.0, content=1.0, searchTerms=5.0, nodeId=0

**Key changes (Wave 2):**
- **Graph expansion re-enabled** with query-relevance filter: check SymbolName/FilePath/ContentSum for query terms before including neighbor
- Multi-seed connection bypass: if a neighbor connects to 2+ seed nodes, include regardless of query relevance
- **Result limits doubled**: A=10→20, B/C=20→40

**Impact:** B+C improved from 33.3% (27/81) → 48.1% (39/81) pre-sweep (nondeterministic baseline)

### Phase 13: Parameter Sweep Optimization (Commits 51-52)

| # | Hash | Description | Agent | Status |
|---|------|-------------|-------|--------|
| 51 | `166f473` | SearchConfig struct + sweep harness merged | 2 Opus agents | Done |
| 52 | `e8f8b04` | 4-phase sweep: optimal defaults applied | Orchestrator | Done |

**Sweep methodology:**
- `SearchConfig` struct (22 fields) replaces all magic numbers in `hybrid.go`
- `NewWithConfig()` constructor enables per-config benchmark runs
- `runBenchmarkWith()` extracted from TestAutomatedGrading for reuse
- 4-phase progressive narrowing across ~130 total configurations

**4-Phase sweep results (pre-determinism fix — scores had ±2 variance from map iteration nondeterminism):**

| Phase | Configs | Winner | B+C (observed) | Delta |
|-------|---------|--------|----------------|-------|
| 1: Single-param | 48 | MaxPerFile=1 | 43/81 (53.1%) | +4 |
| 2: Combos on MaxPerFile=1 | 35 | +no-indegree+seeds=10 | 45/81 (55.6%) | +2 |
| 3: Fine-tune Phase 2 winner | 45 | +floor=0.00 | 46/81 (56.8%) | +1 |
| 4: Combine top findings | 20 | Converged (6 configs tied) | 46/81 (56.8%) | 0 |

**Deterministic score after DA review fixes:** 43/81 (53.1%) — stable across all runs after sort tiebreak + expansion determinism fixes.

**Optimal config changes (applied to DefaultConfig):**

| Parameter | Old | New | Rationale |
|-----------|-----|-----|-----------|
| MaxPerFile | 3 | **1** | Forces file diversity — single best result per file |
| BM25ScoreFloor | 0.05 | **0.00** | Artificial floor hurt diversity by lifting irrelevant nodes |
| WeightInDegree | 0.10 | **0.00** | InDegree was a noise signal — didn't correlate with relevance |
| WeightBetweenness | 0.15 | **0.20** | Graph centrality is a strong quality signal |
| WeightBM25 | 0.25 | **0.30** | Lexical matching deserves more weight |
| ExpansionSeedCount | 5 | **10** | More seed diversity improves graph expansion coverage |

**Per-query scores (final):**

| Query | Score | Notes |
|-------|-------|-------|
| A1 FiscalYearController | 1/1 | Perfect |
| A2 OrderController | 2/3 | Missing v3 (no v3 class node) |
| A3 order API endpoints | 0/5 | Route matching weak for multi-version APIs |
| B1 payment processing | 2/12 | Needs better semantic understanding of payment flows |
| B2 auth/session | 4/9 | Good but misses non-obvious middleware |
| B3 loyalty program | **4/4** | Perfect |
| B4 schema management | **3/3** | Perfect |
| B5 error handling | **1/1** | Perfect |
| B6 omnichannel sync | 3/7 | Missing event listeners |
| C1 order creation | 3/14 | "creation" matches many irrelevant methods |
| C2 stock transactions | 4/8 | Good, missing route nodes |
| C3 webhooks | **7/7** | Perfect (improved from 5/7) |
| C4 OpenTelemetry | **8/8** | Perfect |
| C5 inventory writes | 4/8 | Good, missing bulk routes |

**Net improvement from all search work: 33.3% → 53.1% B+C (+19.8%)**

**Remaining weak spots (not parameter-tunable, need algorithmic changes):**
- B1 payment: Need semantic flow tracing, not just keyword matching
- C1 order creation: "creation" in search_terms floods irrelevant methods (salesOrderCreationWebhook, dateCreationByBatchCode)
- A3 order endpoints: Route nodes for multi-version APIs need better extraction/matching
- Route nodes in C2/C5: Stock transaction routes not being matched

**Sweep harness preserved:** `tests/param_sweep_test.go` with Phases 1-4 for future tuning. Run with `SWEEP=1 go test -tags "fts5,realrepo" -run TestParameterSweep -v ./tests/`

### Test Coverage (13 packages, all passing — 268+ unit tests + 22 real-repo subtests)
- `internal/types` — 12 tests (ID generation, enum values, null byte separator collision, hex format validation)
- `internal/gitmeta` — 21 tests (non-git graceful degradation, normal/detached/dirty snapshots, commit history with depth caps, file history with per-file caps and path filtering, intent compaction with dedup/low-signal filtering/truncation, helpers)
- `internal/storage` — 28 tests (CRUD, FTS5, search, raw query, cascade delete, node_scores, project_summaries, deterministic order, schema version, edges without FK, RawQuery LIMIT, write rejection, transactional upsert, **14 git storage tests**: snapshot CRUD, commit dedup, file history, file intents, batch lookup, latest commit hash)
- `internal/parser` — 15 tests (Go/JS/TS/PHP parsing, edge extraction, import edges, class methods, findBlockEnd states, docblocks, indented PHP classes, file-level nodes, cross-file edges)
- `internal/embedding` — 36 tests (hash embedder, TFIDF embedder, semantic locality, CamelCase similarity, tokenization, serialization, BPE tokenizer load/encode/roundtrip/unknown tokens, ONNX embedder basic/similarity/invalidDim/OS-portable)
- `internal/graph` — 42 tests (BFS, cycles, depth limits, true PPR, PPR personalization bias, DAG, betweenness theoretical normalization, blast radius depth/multi-edge, communities, in-degree caching/copy, search signals, change counter, trace call path, graph connectivity)
- `internal/search` — 40+ tests (composite scoring, per-file cap, CamelCase, stop words, FTS sanitization, limits, concurrent SetStopWords, boolean operator neutralization, ScoreBreakdown population, query kind detection (8 cases), node-type boost (16 cases), BM25 score floor, prefix alias matching, graph expansion (query relevance filter, multi-seed, no-graph))
- `internal/mcp` — 42 tests (CLI handlers, SDK protocol, initialize, tools/list, tools/call, all 13 tools, concurrent registration, data race fix, 5 core blueprint tool handler tests, InspectableResponse context test, architecture summary inspectable test)
- `internal/adr` — 20 tests (discover files, ADR directory, empty repo, max chars truncation, symlink boundary validation)
- `internal/watcher` — 11 tests (create/modify/delete events, debounce, gitignore, excluded dirs, stop safety, walk existing, subdirectory events)
- `tests/integration_test.go` — full pipeline (parse → store → embed → graph → search → delete → graph connectivity assertion)
- `tests/incremental_test.go` — 5 tests (add/modify/delete/consistency/full cycle)
- `tests/concurrent_test.go` — 5 tests (search during index, multi-file changes, search consistency, race conditions)
- `tests/realrepo_test.go` — 3 test functions, 22 subtests (build tag: `realrepo`) against `/Users/naman/Documents/QBApps/qbapi` Laravel project: full pipeline indexing (31K nodes, 8K edges), all MCP tool handlers, domain-relevant search quality
- Benchmark tests added for parser, graph, and search packages

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
- **Review #6 (REVIEW-2.md)**: 27 issues found (5 CRITICAL, 7 HIGH, 10 MEDIUM, 5 LOW). **22 fixed in Wave 1** (03ed37b):
  - CRITICAL: C4 FK removal from edges (cross-file edges were silently dropped). C2 ONNX embedder implemented (afc28f7).
  - HIGH: H1 active file context in search, H2 stored betweenness scores, H3 PHP regex fix, H4 language filter in walk, H5 ADR in architecture mode, H6 memory monitoring, H7 search algorithm documentation. All fixed.
  - MEDIUM: M1 debounce coalescing docs, M3 nested .gitignore, M4 stopwords thread safety, M5 file filter path matching, M6 RawQuery LIMIT, M7 PageRank storage, M8 indexMu mutex, M9 resources+prompts, M10 gitignore hot-reload. All fixed.
  - LOW: L2 community loop break, L3 CLI subcommand detection, L4 WalkSourceFiles, L5 watcher stop race. All fixed.
  - **Remaining**: C1 tree-sitter (future), C3 ncruces/go-sqlite3 (future), C5 sqlite-vec (future), L1 structured logging (future).
- **Review #7 (REVIEW-3.md)**: 97 issues found (18 CRITICAL, 24 HIGH, 32 MEDIUM, 23 LOW). **86 fixed across 6 parallel Opus agents** (5 deferred as architectural decisions, 1 false positive, 5 duplicate/already-handled):
  - **Agent A (Parser+Types, 16 issues)**: C1 file-level nodes for graph connectivity, C11 PHP dedup, H2/H3 cross-package call edges, H4 template literal interpolation, H5 PHP visibility, H21 Go type aliases, H22 NodeTypeInterface, M9 call edge dedup, M10 findBlockEnd fallback, M11-M12 JS regex, M13-M14 ContentSum, M15 null byte separator, L17 TypeScript constructs.
  - **Agent B (Storage, 15 issues)**: C7 transactional UpsertNode, C8/M4 readfile/edit blocklist, C10 configurable embedding dim, H6 DeleteByFile node_scores, H7 UpdateFTS error propagation, H8 transactional UpsertEmbedding, M1-M8 various storage hardening, M32 migration safety.
  - **Agent C (Race Conditions, 15 issues)**: C9 path traversal protection, C14 memory monitor leak, C15 signal handler race, C16 handleFileEvents indexMu, H10 gitignores mutex, H11 atomic EmbeddingDim, H12 async betweenness locking, H20 .gitignore in indexPath, M17 recompute graph metrics, M20 ONNX validation, M21 git ref regex, L18-L19 watcher safety.
  - **Agent D (Search+Graph, 14 issues)**: C12 FTS5 boolean injection, C13/C18 min-max normalization, H1 InDegree cache copy, H19 sanitized FTS fallback, M27 rune trigrams, M28 community staleness, M29 RLock optimization, M30 seed dedup, L4 stop words test, L21 digit CamelCase.
  - **Agent E (Tools+Server, 22 issues)**: H13 registration error logging, H14 active_files InputSchema, H15-H16 read_symbol/search_code fixes, H17 PageRank caching, H18 BPE unknown tokens, H23 detect_changes status, H24 ONNX tensor leak, M22-M26 server/tool fixes, M31 configurable ONNX dim, L14-L16 response cap/filter/BFS, L23 duplicate prevention.
  - **Agent F (Tests, 13 issues)**: L1 5 core tool tests, L2 cross-file edges, L3 graph connectivity, L5-L12 test improvements, L22 write rejection, L13 deps.
  - **Deferred**: C2 tree-sitter, C3 ONNX library, C4 model choice, C5 model not embedded, C6 sqlite-vec. **False positive**: C17 Go 1.25.0 (valid).
- **Review #8 (Blueprint Alignment DA)**: Quick review of Phase 1+2 changes. Found 6 issues (bounds check in ONNX, output tensor cleanup, nil-on-close, hidden dim derivation). All fixed in commit `ede6fcf`.
- **Review #9 (Search Quality DA)**: 13 issues found (0 CRITICAL, 2 HIGH, 5 MEDIUM, 6 LOW). **2 HIGH + 2 MEDIUM fixed**:
  - HIGH: `isHelperFile` false positives (path-component matching), over-aggressive domain stop words (removed "get", "class", "method", "code", etc.).
  - MEDIUM: Added test coverage for 3 new EdgeType values, JS/TS extends detection for INHERITS edges.
  - LOW (accepted): PPR subgraph parameter differences (intentional speed trade-off), Go DEFINES_METHOD file-local resolution, PHP namespaced extends, methods double-counted in-degree (intentional — both edges are semantically correct).
- **Review #10 (Cross-File Edge Resolution DA)**: 3 HIGH, 2 MEDIUM, 1 LOW:
  - HIGH #1 (false positive): JS INHERITS missing TargetSymbol — actually present at line 505.
  - HIGH #2 (accepted): `indexPath` and `processFileEvent` skip cross-file resolution — full re-index on daemon start mitigates; incremental resolution would require DB lookups per edge (future optimization).
  - HIGH #3 (accepted): Double penalty (score 0.3x + cap 1) for helper files — intentional; score penalty handles ranking, cap handles diversity.
  - MEDIUM #4 (accepted): `symbolIndex` first-wins is non-deterministic for duplicate names — acceptable for Laravel (one class per file); would need FQN for disambiguation (future).
  - MEDIUM #5 (accepted): Stop words "end", "flow", "logic" may suppress legitimate searches — CamelCase splitting mitigates (`AuthFlow` → `Auth` + `Flow`); plain `flow` is rare as a code search.
  - LOW #6: DEFINES_METHOD source may be dangling for cross-file receivers — pre-existing, same-file resolution only.
- **Review #11 (Cold Start DA)**: 16 issues found (0 CRITICAL, 4 HIGH, 8 MEDIUM, 8 LOW). **4 HIGH + 7 MEDIUM fixed**:
  - HIGH: H1 missing rows.Err() in GetFileIntentsByPaths, H2 processFileEvent lacks embedding intent enrichment, H3 NewExtractor swallows all git errors (now only returns nil for ErrRepositoryNotExists), H4 fragile stop sentinel (replaced with errStopIteration). All fixed.
  - MEDIUM: M1 UTF-8-safe truncateBytes, M2 Snapshot auto-sets RepoRoot from Extractor, M3 UpsertRepoSnapshot error handling in indexPath, M4 trailer key validation fix, M5 json.Marshal for trailers, M6 context.Context with timeouts, M7 UTC author_time storage. All fixed.
  - LOW (accepted): L1 nil-nil return pattern, L2 manual FNV, L3 non-deterministic test times, L4 map iteration order, L5 inconsistent API shapes, L6 revert filtering, L7 untested time parse, L8 directory entries in initial commits.

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
- Composite scoring: `0.35*PPR + 0.30*BM25 + 0.20*Betweenness + 0.00*InDegree + 0.15*SemanticSim` (optimized via 4-phase parameter sweep)
- All signals normalized to [0,1] before weighting
- Query-time PPR seeded from top 10 FTS results
- FTS5 enhancements: prefix matching (`term*`), CamelCase splitting, stop word filtering, special char sanitization
- **search_terms column** (migration v5): CamelCase-split + file basename tokens for better BM25 matching (credit: C reference v0.8.0 autoresearch methodology)
- **Query kind detection**: route/class/function/natural intent classification → node-type boosting (credit: code-review-graph)
- **Prefix alias matching**: `strings.HasPrefix(term, aliasKey)` — "authentication" matches "auth" alias
- **Graph expansion with query-relevance filter**: re-enabled, checks SymbolName/FilePath/ContentSum for query terms + multi-seed bypass
- **Node-type boosts**: route=2.5x, routeMethod=1.3x, class=1.5x, function=1.2x
- **Path penalties**: generated/vendor=0.3x, migrations=0.2x, tests=0.3x, examples=0.6x, config=0.8x
- Per-file cap: max **1** result per unique `file_path` (optimized from 3 — forces file diversity)
- BM25 score floor: **0.00** (removed — artificial floor hurt diversity)
- Expansion seeds: **10** (up from 5 — expands from more top results)
- `ComputeInDegree()` on GraphEngine — counts incoming edges, but **weight=0.00** (eliminated as noise signal)
- **Tests:** composite scoring, per-file cap, CamelCase splitting, stop words, query kind detection (8 cases), node-type boost (16 cases), BM25 score floor, prefix alias matching, graph expansion (query relevance filter, multi-seed connection)

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

### Feature 11: ONNX Embedder with Qwen2 Model (Done — now purego)
- **Pure Go BPE tokenizer** (`tokenizer.go`): Reads HuggingFace `tokenizer.json`, handles both array and string merge formats, 151K vocab, byte-level encoding, NFC normalization, Go RE2-compatible pre-tokenization regex
- **ONNXEmbedder** (`onnx.go`, build tag: `onnx`): Loads quantized Qwen2 model via `shota3506/onnxruntime-purego` (pure Go, no CGO for ONNX)
- **purego API**: `ort.NewRuntime(libPath)` → `rt.NewEnv()` → `rt.NewSession()` → `session.Run()` with map-based I/O
- **Last-token pooling**: Appropriate for causal/decoder-only models (takes hidden state of last token)
- **Matryoshka dim truncation**: Configurable (64/128/256/512/896), default 256, with bounds check vs hiddenDim
- **Config**: `--onnx-model`, `--onnx-lib`, `--embedding-dim` CLI flags
- **Graceful fallback**: ONNX failure → TFIDF embedder, non-ONNX builds → stub returns error
- **Safety**: Close all output values via defer loop, nil fields on Close() (double-close safe)
- **Quality**: sim(ReadFile, ReadFileContents) = 0.69, sim(ReadFile, SQL) = 0.17
- **Tests**: 7 new tests (tokenizer load/encode/special/roundtrip + ONNX basic/similarity/invalidDim)

### Feature 12: DA Review #6 Wave 1 — 22 Bug Fixes (Done)
- **C4 FK removal**: Migration v2 removes foreign keys from edges table — INSERT OR IGNORE + FK was silently dropping all cross-file edges
- **H1 active file context**: `ActiveFiles []string` in search, resolved to node IDs, passed as PPR seeds
- **H2 stored betweenness**: Architecture summary and explore use stored scores, not recomputed
- **H3 PHP regex fix**: Removed `^` anchors, added indented declaration support
- **H4 language filter**: `parser.IsSupported(path)` filter in indexPath walk
- **H5 ADR architecture**: Architecture mode includes `GetAllProjectSummaries()`
- **H6 memory monitoring**: 5-min ticker, 2GB warning
- **H7 search docs**: Algorithm documentation for RRF → weighted linear combination
- **M3 nested gitignore**: `gitignoreEntry` struct with baseDir, discovered during walk
- **M4 thread safety**: `stopWordsMu sync.RWMutex` for concurrent access
- **M9 resources+prompts**: `qb://graph/stats` resource, `explain_symbol` prompt template
- **M10 gitignore reload**: Runtime .gitignore modification detection
- 10 files changed, 603 insertions, 70 deletions

### Feature 13: Blueprint Alignment — 5 Gaps Addressed (Done)
- **Gap 1: Tree-sitter** — Initially used `gotreesitter` (pure-Go reimplementation, v0.12.2) but it stack-overflowed on large PHP files (500KB+) due to Go goroutine stack recursion in GLR parser. Replaced with `smacker/go-tree-sitter` (CGO, wraps real C tree-sitter library). C tree-sitter manages heap-allocated parse stack — handles any file size. Alternative considered: `tree-sitter/go-tree-sitter` (official, modular grammars, more maintenance) but smacker chosen for bundled grammars and simpler API. All parser tests pass.
- **Gap 2: purego ONNX** (shota3506/onnxruntime-purego): Replaced CGO yalue/onnxruntime_go with pure Go bindings via ebitengine/purego. Eliminates CGO requirement for ONNX inference.
- **Gap 3: MCP tools 13→5** SDK: Removed RegisterSDKTool for 8 non-blueprint tools. MCP clients see 5 tools (context, impact, read_symbol, query, index). All 13 remain in CLI mode.
- **Gap 4: sqlite-vec always available** (asg017/sqlite-vec-go-bindings/cgo v0.1.6): Statically linked via CGO. `sqlite_vec.Auto()` via sync.Once. Vec0 creation failure is now fatal.
- **Gap 5: Single binary verified**: `go build -tags "fts5"` = single binary (no external deps). `go build -tags "fts5,onnx"` = single binary + ONNX Runtime .dylib sidecar.
- **DA Review**: 2 HIGH + 4 MEDIUM issues fixed (ONNX bounds check, output cleanup, Close safety, sync.Once, PHP offset, vec0 logging)
- **Real-repo test** (build tag: `fts5,realrepo`): 3 test functions, 22 subtests against qbapi Laravel project (31K+ nodes, 8K+ edges)

### Feature 14: Cold Start Enhancement — Git-Derived Intent (Done)
- **Pure Go Git integration** via `go-git/v5` — repo open, log, diff, status, worktree support
- **`internal/gitmeta` package**: `Extractor` with `Snapshot()`, `RecentCommits()`, `FileHistory()`, `CompactFileIntents()`, `RecentCommitsSummary()`
- **Schema migration v3**: 4 new tables (`repo_git_snapshot`, `git_commits`, `git_file_history`, `git_file_intent`)
- **Repo snapshot**: branch/HEAD state, dirty/staged/modified/untracked counts, human-readable summary
- **File-level intent compaction**: dedup, low-signal filtering (lint/fmt/wip/merge), bounded text (default 1500 bytes)
- **Embedding enrichment**: `[git-intent]` text appended to `ContentSum` for all three indexing paths
- **MCP tool enhancements**: `git_context` in context, `file_intents` in detect_changes, `file_intent` in understand
- **Config flags**: `--cold-start` (default on), `--git-history-depth` (500), `--git-per-file-cap` (20), `--git-max-message` (2000), `--git-max-intent` (1500)
- **Graceful degradation**: non-git dirs → silently disabled, shallow repos → partial history, errors → logged, not fatal
- **Context-aware timeouts**: 60s for full index, 30s for incremental
- **Tests**: 21 gitmeta tests + 14 storage tests = 35 new tests
- **DA Review #11**: 16 issues found, 11 fixed (4 HIGH + 7 MEDIUM)

### Features Explicitly Skipped (for now)
- HTTP UI server / graph visualization
- Cypher query language
- ~~Co-change frequency (requires git history integration)~~ **Now partially addressed by Cold Start file history**
- HITS authority/hub scores (C project uses, but InDegree covers similar ground)
- Symbol-level git attribution via blame (Phase 5 of Cold Start plan — deferred)

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
go build -tags "fts5" ./...              # compilation check (TFIDF only)
go build -tags "fts5,onnx" ./...         # compilation check (with ONNX support)
go test -tags "fts5" ./... -count=1      # run all tests (TFIDF mode)
go test -tags "fts5,onnx" ./... -count=1 # run all tests (with ONNX tests)
qb-context cli --list                    # list available MCP tools
qb-context cli context '{"query":"payment","limit":5}'  # test a tool

# Run with ONNX model:
qb-context --onnx-model /path/to/model --onnx-lib /path/to/libonnxruntime.dylib --embedding-dim 256
```

### Known Limitations
- Tree-sitter JS/TS/PHP parsers extract symbol definitions via AST; call edges still use regex on node body text for reliability
- INHERITS/IMPLEMENTS cross-file resolution via `TargetSymbol` works in `indexRepo()` (full index) but NOT in `indexPath()` or `processFileEvent()` (incremental) — incremental updates produce dangling edges until next full re-index
- `symbolIndex` first-wins: duplicate class names across files (e.g., `User` model + `User` resource) resolve non-deterministically — would need FQN for disambiguation
- Go call edges resolve cross-package via file-level nodes, but symbol-level cross-package resolution is approximate
- gonum Betweenness doesn't support sampling (O(V*E) for large graphs)
- Index operations serialized via `indexMu` mutex, but concurrent search during index is safe
- ONNX embedder requires ONNX Runtime shared library installed on the system (purego FFI, no CGO for ONNX itself)
- BPE tokenizer pre-tokenization regex simplified for Go RE2 (no negative lookahead) — functionally equivalent for embedding
- Embedding dim change requires re-indexing all embeddings (no automatic migration)
- mattn/go-sqlite3 requires CGO; ncruces/go-sqlite3 (pure Go WASM) is a future replacement target
- ONNX model file not embedded in binary — must be provided via `--onnx-model` path

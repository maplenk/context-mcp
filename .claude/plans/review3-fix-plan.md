# REVIEW-3 Fix Plan — 91 Issues (6 Parallel Agents)

## Deferred (5 issues — architectural decisions, not bugs)
- C2: Tree-sitter (future)
- C3: ONNX library choice (yalue works, purego is aspirational)
- C4: Model choice (Qwen2 is deliberate)
- C5: Model not embedded (deliberate for now)
- C6: sqlite-vec extension (not available as Go module; graceful fallback in place)

## False Positive (1 issue)
- C17: go 1.25.0 — user has Go 1.25.0 installed, this is valid

## Agent A: Parser + Types (16 issues)
Files: `internal/parser/parser.go`, `internal/types/types.go`
- C1: Create file-level nodes for import edge anchoring + cross-file symbol resolution
- C11: PHP parser creates duplicate nodes (add seen map like JS)
- H2: Go call edges use wrong file path for cross-package targets
- H3: JS/TS call edges same problem
- H4: findBlockEnd template literal ${...} interpolation
- H5: PHP methods without visibility keywords lose class association
- H21: Go parser ignores type aliases and named types
- H22: Add NodeTypeInterface to types enum
- M9: Call edge deduplication (Go/JS/PHP)
- M10: findBlockEnd fallback returns arbitrary 5000
- M11: JS arrow function regex misses common patterns
- M12: looksLikeRegex doesn't check keywords
- M13: ContentSum in Go parser just the name
- M14: buildContentSum captures wrong doc blocks
- M15: GenerateNodeID separator collision
- L17: TypeScript constructs not parsed (interface, enum, type, namespace)

## Agent B: Storage Security + Data Integrity (15 issues)
Files: `internal/storage/sqlite.go`, `internal/storage/migrations.go`
- C7: UpsertNode is NOT atomic — wrap in transaction
- C8: readfile() missing from blocklist
- C10: Hardcoded embedding dim in migration — use configurable dim
- H6: DeleteByFile doesn't delete node_scores
- H7: UpdateFTS swallows DELETE error
- H8: UpsertEmbedding DELETE error swallowed
- M1: UpsertNode uses mutex instead of transaction
- M2: RawQuery LIMIT check is substring-based
- M3: Semicolon check blocks legitimate queries
- M4: Missing readfile, edit() from blocklist
- M5: DB() method exposes raw connection (add warning comment or restrict)
- M6: FTS5 query not sanitized at storage layer
- M7: Connection pool size not configured for SQLite
- M8: UpsertEmbedding not transactional
- M32: Migration v2 table swap non-atomic on old SQLite

## Agent C: Race Conditions + Main (15 issues)
Files: `cmd/qb-context/main.go`, `internal/watcher/watcher.go`, `internal/embedding/engine.go`
- C9: index tool has no path validation
- C14: Memory monitor goroutine leaks — add stop channel
- C15: Signal handler race — use cleanup sync.Once already exists, fix watcher double-stop
- C16: handleFileEvents has no index mutex
- H10: Race on gitignores slice
- H11: EmbeddingDim mutable global — not thread-safe
- H12: handleFileEvents async betweenness races with event loop
- H20: indexPath doesn't respect .gitignore
- M17: indexPath doesn't recompute betweenness/PageRank
- M20: No config validation for ONNX settings
- M21: detect_changes git ref regex allows @{}
- L18: ADR changes not detected by watcher (.md not watchable)
- L19: Watcher.Stop()/close not protected by sync.Once (already has stopped flag, make idempotent)

## Agent D: Search + Hybrid + Graph (14 issues)
Files: `internal/search/hybrid.go`, `internal/graph/graph.go`, `internal/search/hybrid_test.go`
- C12: FTS5 keyword injection via boolean operators (OR, AND, NOT, NEAR)
- C13: Semantic scores can be negative, breaking normalization
- C18: PPR max-value normalization produces incomparable scores
- H1: InDegree cache returns live reference on miss (graph.go:341-379)
- H19: buildFTSQuery fallback passes unsanitized query
- M27: Trigram/bigram on bytes, not runes
- M28: GetConnectors uses potentially stale community data
- M29: ComputeSearchSignals takes write lock for read operation (downgrade to RLock where possible)
- M30: PPR seeds not deduplicated
- L4: TestBuildFTSQuery_StopWords is a no-op
- L21: camelCaseRe drops digits

## Agent E: Tools + Server (22 issues)
Files: `internal/mcp/tools.go`, `internal/mcp/server.go`, `internal/mcp/server_test.go`
- H13: All RegisterSDKTool errors silently discarded
- H14: context tool CLI InputSchema missing active_files
- H15: readSymbolHandler off-by-one EndByte untested
- H16: searchCodeHandler leaks file descriptors (use defer)
- H17: understand and get_key_symbols recompute PageRank every call
- H18: BPE tokenizer silently drops unknown tokens (in embedding/tokenizer.go)
- H23: detect_changes reports all symbols as changed
- H24: ONNX session tensor leak on error path (in embedding/onnx.go)
- M24: contextHandler doesn't validate empty query
- M25: impactHandler classifies depth-0 nodes as LOW
- M26: searchCodeHandler only searches indexed files
- M31: Hardcoded ONNX hidden dimension
- L14: toToolResponse no size cap
- L15: FileFilter has inconsistent semantics across tools
- L16: TraceCallPath bidirectional BFS single parent
- L19: Already handled by Agent C
- L23: Duplicate tool name registration not prevented
- M22: server_test.go data race on bytes.Buffer
- M23: server_test.go done channel never read

## Agent F: Test Improvements (13 issues)
Files: `tests/*_test.go`, `internal/*/..._test.go`
- L1: No tests for 5 core blueprint tools
- L2: No cross-file edge tests
- L3: Integration test never verifies graph connectivity
- L5: BlastRadius test only tests one edge
- L6: ONNX tests hardcode macOS paths
- L7: TestBPETokenizer hardcodes absolute path
- L8: No benchmark tests
- L9: TestGenerateNodeID_KnownValue tests nothing
- L10: Test data uses fake byte ranges
- L11: No error recovery tests
- L12: No large-file tests
- L22: RawQuery has no test for write rejection
- L13: Stale/vulnerable indirect deps (go mod tidy + update)

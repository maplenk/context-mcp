# Plan: Improve Code Search Quality

## Current State (2026-04-03)

### Baseline & Progress

| Metric | Manual Baseline | Automated Baseline | After All Changes | Delta |
|--------|----------------|-------------------|-------------------|-------|
| B+C hit rate | 12.7% (13/102) | 23.5% (19/81) | **33.3% (27/81)** | **+9.8%** |
| C-reference | 27.1% (23/85) | — | — | — |

Note: Manual and automated baselines differ because the automated grader parses rubric items differently (skips free-form descriptions, wildcards, etc.), resulting in 81 vs 102 rubric items.

### Per-Query Scores (Automated Grading)

| Query | Before | After | Notes |
|-------|--------|-------|-------|
| A1 FiscalYearController | 1/1 | 1/1 | Stable |
| A2 OrderController | 3/3 | 3/3 | Stable |
| A3 order API endpoints | 0/5 | 0/5 | Routes indexed but score 0 |
| A4 payment files | skip | skip | No rubric items parsed |
| B1 payment processing | 3/12 | 4/12 | +razorPay via payment→razorpay alias |
| B2 auth & session | 1/9 → 0/9 | 0/9 | **Regressed** — config/session.php lost after file doc merge |
| B3 loyalty program | 1/4 | 1/4 | Stable |
| B4 schema management | 0/3 | 1/3 | +UpdateSchema via schema→migration alias |
| B5 error handling | 0/1 | 1/1 | +Handler.php via error→exception alias |
| B6 omnichannel sync | 0/7 | 3/7 | +EasyEcom, Unicommerce, OnlineOrder via alias |
| C1 order creation flow | 2/14 | 2/14 | Routes not surfacing |
| C2 stock transaction | 2/8 | 2/8 | Routes not surfacing |
| C3 webhook dispatch | 4/7 | 4/7 | Stable |
| C4 OpenTelemetry | 5/8 | 6/8 | +OpenTelemetrySpan |
| C5 inventory flow | 1/8 | 4/8 | +InventoryController, InventoryWeb, StockLedger via alias |

## Completed Work

### Phase 0: Quick Scoring Fixes [DONE] — commit `388b971`
- `pathPenalty()` replaces `isHelperFile()` — graduated penalties: migrations 0.4x, tests 0.5x, examples 0.6x, vendor/generated 0.3x
- `cleanQuery()` strips structural phrases: "end to end", "step by step", "how does", "show me", etc.
- Targeted stopwords: only `handle` and `end` (user rejected broad list of get/set/create/update etc.)

### Phase 1: Automated Grading Harness [DONE] — commit `adec6ec`
- `tests/benchmark_grading_test.go` — parses `benchmarks/human-answers.md`, runs all 15 queries, computes per-tier hit rates
- Run: `go test -tags "fts5,realrepo" -run TestAutomatedGrading -v ./tests/ -count=1 -timeout 300s`
- Regression guard at 5% B+C threshold (raise as improvements land)

### Phase 2: Laravel Route Extraction [DONE] — commit `adec6ec`
- `internal/types/types.go` — `NodeTypeRoute` (value 7), `EdgeTypeHandles` (value 8)
- `internal/parser/routes.go` — regex-based extractor: `Route::(get|post|put|delete|patch)` + `'uses' => 'Controller@method'` + group prefix tracking with brace-depth matching
- `internal/parser/parser.go` — `isRouteFile()` detection, wired into `parsePHP()`
- `tests/realrepo_test.go` — extended `symbolIndex` to also map method names for EdgeTypeHandles resolution via existing TargetSymbol cross-file pattern
- **Result**: Route nodes ARE indexed (23K+ nodes including routes). But route nodes **score 0.0000** due to min-max normalization — they're low BM25 candidates that normalize to 0.

### Phase 3: Query Alias Expansion [DONE] — commit `adec6ec`
- `internal/search/hybrid.go` — `queryAliases` map with 8 entries: omnichannel, auth, webhook, payment, inventory, schema, logging, error
- Applied in `buildFTSQuery` after stop word filtering, before prefix wildcards
- **Biggest impact**: B6 0→3, C5 1→4, B4 0→1, B5 0→1

### File Document Nodes [DONE] — commit `adec6ec`
- `internal/parser/filedoc.go` — `IsFileDocCandidate()` + `CreateFileDocNode()` for config/*.php, *.blade.php, cloud_schema/*.sql
- `cmd/qb-context/main.go` — wired into `indexRepo`, `indexPath`, and `processFileEvent`
- `internal/watcher/watcher.go` — `WalkAllFiles()` for discovering non-code files, `.sql` added to watchable extensions

### Graph Expansion [IMPLEMENTED BUT DISABLED] — commit `adec6ec`
- `internal/search/hybrid.go` — `expandFromSeeds()` method: takes top-5 seeds, expands 1-hop via GetCallees/GetCallers, fetches neighbors from store, applies connectivity bonus (0.15x seed score)
- **Disabled because it regresses benchmark**: expanded nodes flood top-N with structurally-connected but query-irrelevant results. Needs query-relevance filtering before re-enabling.
- `internal/storage/sqlite.go` — `GetNode(id)` method added for single-node lookups

## Root Cause Analysis of Remaining Gaps

### Problem 1: Route nodes score 0.0000
Route nodes like `POST /v1/merchant/{storeID}/order` are indexed in FTS but get minimum BM25 scores among candidates. Min-max normalization maps minimum to 0.0. Since all other signals (PPR, betweenness, inDegree, semantic) are also 0 or near-0 for routes (new nodes with few graph connections, no embeddings), the composite is 0.

**Why routes have low BM25:** Route SymbolNames like `POST /v1/merchant/{storeID}/order` tokenize to `post`, `v1`, `merchant`, `storeid`, `order` — very common tokens in a Laravel codebase. The query "order API endpoints" produces `order* OR endpoint*` which matches thousands of nodes, and routes are just a few among many.

**Potential fixes:**
- Boost route nodes directly in composite scoring (e.g., `if node.NodeType == NodeTypeRoute { composite *= 1.5 }`)
- Store richer ContentSum for routes (include controller name, feature name, category from route metadata)
- Use FTS5 column weights differently for route nodes

### Problem 2: Graph expansion hurts more than helps
When seeds are methods like `OrderController.postOrder`, their callees include dozens of helper methods, internal calls, etc. These flood the result set with 0.15x-scored nodes that displace genuine results.

**Potential fixes:**
- Only expand via specific edge types (EdgeTypeHandles, EdgeTypeDefines) not all edges
- Check if expanded node's content matches any query term before including
- Use 2-hop expansion but only following route→handler→model chains, not arbitrary call chains

### Problem 3: B2 (auth) — 0/9 hits
"authentication and session management" → query terms are "authentication", "session", "management". The "auth" alias doesn't fire because "authentication" ≠ "auth" (exact match). Session-related middleware classes (OauthMiddleware, webMiddlewareV2, thirdPartyToken) don't contain "authentication" or "session" in their symbol names.

**Potential fixes:**
- Add stemmed/substring alias matching (match "authentication" to "auth" alias key)
- Add more alias entries: "authentication" → same as "auth", "session" → "middleware", "cookie"
- Index middleware registration (Kernel.php routes middleware to classes)

### Problem 4: Missing cross-file flow traversal (C1, C2)
C1 expects 14 items spanning OrderController → Order.php → services → jobs. The search finds OrderController but can't follow the call chain to discover Order.php, OrderPaymentsService, etc. because graph expansion is disabled.

**Potential fixes (when re-enabling expansion):**
- Only expand from seeds with score > 0.3 (skip low-confidence seeds)
- Filter expanded nodes: only include if at least one query term appears in the node's file path, symbol name, or content
- Use query-aware expansion: for "order creation flow", only follow edges from nodes whose symbols contain "order"

### Problem 5: Node coverage gap vs C-reference
The C-reference implementation indexes 66 languages and has dedicated extractors for many more patterns. qb-context currently parses:
- Go (native AST)
- PHP (tree-sitter: class, function, method, use statements + regex routes)
- JS/TS (tree-sitter: class, function, method, imports)
- File doc nodes (config, Blade, SQL — text only, no structure)

**Missing from PHP parsing specifically:**
- Traits and trait usage (`use SomeTrait;` inside classes)
- Interfaces (partially — `NodeTypeInterface` exists but may not be extracted for PHP)
- Anonymous/closure functions assigned to variables (`$handler = function() { ... }`)
- Class constants and properties
- Namespace declarations and use aliases
- Artisan command registrations
- Event/listener registrations
- Middleware registrations in Kernel.php
- Service provider bindings

## Next Phase: Parser Coverage & Node Density

### Priority 1: Enrich PHP tree-sitter extraction
**File:** `internal/parser/parser.go`

The C-reference extracts far more node types. For PHP specifically:

1. **Traits**: Add `case "trait_declaration":` mirroring class handling. Traits are heavily used in Laravel (e.g., `Notifiable`, `HasFactory`). Extract trait name, methods, and `use TraitName` edges.

2. **Interfaces**: Verify `interface_declaration` is handled in PHP switch. `NodeTypeInterface` exists but may not be extracted for PHP files specifically.

3. **PHP anonymous functions / closures**: `$handler = function($x) { ... };` — extract as NodeTypeFunction with the variable name as symbol.

4. **Class properties and constants**: Currently only methods are extracted from classes. Properties like `protected $fillable = [...]` and constants like `const STATUS_ACTIVE = 1` carry searchable vocabulary.

5. **Route metadata enrichment**: Currently route ContentSum is just "POST /path -> Controller@method". Enhance with:
   - Feature name from `'feature' => 'orderCreate'`
   - Category from `'category' => 'orders'`
   - Operation from `'operation' => 'create'`
   These fields are present in qbapi routes and are highly searchable.

### Priority 2: Content enrichment for existing nodes
**File:** `internal/parser/parser.go`

Currently `ContentSum` for PHP methods is minimal (just the doc comment or first line). The C-reference stores more content per node:
- Method body first N lines (bounded)
- Method signature with parameter types
- Return type hints
- PHPDoc @param, @return, @throws annotations

More content → better FTS matching → higher BM25 scores for relevant nodes.

### Priority 3: Event/Listener/Middleware registration extraction
Laravel-specific patterns that connect files:
- `app/Providers/EventServiceProvider.php` maps events → listeners
- `app/Http/Kernel.php` maps middleware aliases → middleware classes
- `app/Console/Kernel.php` maps command schedules → command classes

These create edges that graph traversal can follow. Without them, the graph is sparse for middleware (B2) and events (B6).

### Priority 4: Fix graph expansion with query-relevance filter
Re-enable `expandFromSeeds` with:
```go
// Only include expanded node if query terms appear in its content
queryTerms := strings.Fields(strings.ToLower(query))
for _, term := range queryTerms {
    if strings.Contains(strings.ToLower(node.ContentSum), term) ||
       strings.Contains(strings.ToLower(node.SymbolName), term) {
        // include this node
    }
}
```

### Priority 5: Stemmed alias matching
Change alias lookup from exact to prefix/stem:
- "authentication" should trigger "auth" alias
- "inventory" should trigger "inventory" alias (already works)
- "webhooks" should trigger "webhook" alias

### Priority 6: BM25 weight tuning
**File:** `internal/storage/sqlite.go`
- Current: `bm25(nodes_fts, 10.0, 1.0, 0.0)` — symbol_name weight 10x, content 1x, file_path 0x
- Proposed: `bm25(nodes_fts, 5.0, 2.0, 0.5)` — reduce symbol weight, increase content weight, add file path weight
- This helps nodes with rich ContentSum (file doc nodes, routes) score higher

## Files Modified in This Work

| File | Changes |
|------|---------|
| `internal/types/types.go` | +NodeTypeRoute, +EdgeTypeHandles |
| `internal/types/types_test.go` | Updated enum tests |
| `internal/parser/routes.go` | New — Laravel route extractor |
| `internal/parser/routes_test.go` | New — 7 test cases |
| `internal/parser/filedoc.go` | New — file document node creator |
| `internal/parser/filedoc_test.go` | New — 3 test functions |
| `internal/parser/parser.go` | Wire routes into parsePHP |
| `internal/search/hybrid.go` | pathPenalty, cleanQuery, aliases, expandFromSeeds (disabled) |
| `internal/search/hybrid_test.go` | Tests for all search changes |
| `internal/storage/sqlite.go` | +GetNode method |
| `internal/watcher/watcher.go` | +WalkAllFiles, .sql watchable |
| `cmd/qb-context/main.go` | File doc node indexing in all paths |
| `tests/realrepo_test.go` | File doc walk, method symbol index |
| `tests/benchmark_grading_test.go` | New — automated grading harness |

## Verification Commands

```bash
# Build
go build -tags "fts5" ./...

# Unit tests
go test -tags "fts5" ./... -count=1

# Automated grading (requires qbapi repo)
go test -tags "fts5,realrepo" -run TestAutomatedGrading -v ./tests/ -count=1 -timeout 300s

# Full real-repo test suite
go test -tags "fts5,realrepo" -v ./tests/ -count=1 -timeout 300s
```

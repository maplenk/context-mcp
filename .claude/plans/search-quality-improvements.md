# Plan: Improve Code Search Quality (Revised)

## Context

Human evaluation of qb-context against 15 benchmark queries on the qbapi PHP codebase shows **12.7% hit rate** (13/102 rubric items) on concept/cross-file queries. The C-reference implementation gets **27.1%** (23/85 tested items). See `benchmarks/human-eval-results.md` for grading against `benchmarks/human-answers.md`.

## Phase 0: Quick Scoring Fixes [DONE]

Merged in commit `388b971`:
- **Path penalties**: `pathPenalty()` replaces `isHelperFile()` — migrations 0.4x, tests 0.5x, examples 0.6x, vendor/generated 0.3x
- **Phrase stripping**: `cleanQuery()` removes "end to end", "step by step", "how does", etc.
- **Targeted stopwords**: Only `handle` and `end` as code-noise stopwords (not broad list)

## Phase 1: Automated Grading Harness

**Why first:** Can't tune without strict, reproducible measurement. Manual grading is error-prone and slow.

**File:** `tests/benchmark_grading_test.go` (new)

Build a Go test that:
1. Reads `benchmarks/human-answers.md` — parse each query's expected files/symbols/routes
2. Runs each of the 15 queries (A1-C5) against the qbapi repo index
3. Computes hit rate: for each rubric item, check if it appears in the top-N results (N=10 for A-tier, N=20 for B/C-tier)
4. Outputs per-query scores and aggregate B+C hit rate
5. Flags regressions: fail the test if any previously-passing query (A1, A3, C4) regresses

Run with: `QBCONTEXT_REPO=/Users/naman/Documents/QBApps/qbapi go test -tags "fts5,realrepo" -run TestAutomatedGrading -v ./tests/ -count=1`

**Rubric matching rules:**
- File paths: match if result's FilePath contains the expected path suffix (case-insensitive)
- Symbol names: match if result's SymbolName matches expected symbol
- Route paths: match if result's content/symbol contains the route pattern (e.g., `/v1/merchant/{storeID}/order`)
- Partial credit: each rubric item is 1 point, no bonuses

## Phase 2: Laravel Route Indexing

**Why highest impact:** Answer key is route-heavy. B2, C1, C2, C3, C5 all expect routes in results. Today routes are invisible — the parser sees route files as just PHP files with function calls.

### 2A. New Types
**File:** `internal/types/types.go`

```go
NodeTypeRoute    NodeType = "route"     // HTTP route endpoint

EdgeTypeHandles  EdgeType = iota + 8    // route → controller method (after existing constants)
```

### 2B. Route Extractor
**File:** `internal/parser/routes.go` (new)

Regex-based extractor (not tree-sitter — route definitions are runtime calls, not AST declarations):

1. **Pattern**: `Route::(get|post|put|delete|patch)\(\s*['"]([^'"]+)['"]`
   - Captures HTTP method + URL path
   
2. **Handler resolution**: `'uses'\s*=>\s*['"](\w+)@(\w+)['"]`
   - Captures controller name + method name
   
3. **Route groups**: Track `Route::group(['prefix' => '...'])` nesting to build full paths

4. **Output per route:**
   - `ASTNode` with `NodeType: NodeTypeRoute`, `SymbolName: "POST /v1/merchant/{storeID}/order"`, `ContentSum` includes controller@method reference
   - `ASTEdge` with `EdgeType: EdgeTypeHandles` linking route node → controller method node (resolved by matching `SymbolName` in the store)

### 2C. Wire Into Indexing Pipeline
**File:** `internal/parser/parser.go`

After normal PHP parsing, if file matches `routes*.php` or `routesWeb*.php`:
- Run route extractor
- Append route nodes to the file's parse result
- Route→handler edges get resolved in a second pass after all files are indexed

**File:** `cmd/qb-context/main.go` or `internal/mcp/tools.go`

Add a post-indexing pass: resolve route handler references (`OrderController@postOrder`) to actual node IDs via `store.SearchLexical("OrderController postOrder", 5)` or exact symbol lookup.

### Projected Impact
Routes appearing in results should move: C1 (+3-4 rubric items), C2 (+2-3), C3 (+3-4), B2 (+2 login endpoints), C5 (+2-3)

## Phase 3: Repo-Specific Query Aliasing

**Why:** Business vocab doesn't match code identifiers. "omnichannel" → code uses "EasyEcom", "Unicommerce", "OnlineOrder".

**File:** `internal/search/hybrid.go`

```go
var queryAliases = map[string][]string{
    "omnichannel": {"easyecom", "unicommerce", "onlineorder"},
    "auth":        {"oauth", "login", "token", "session", "authenticate"},
    "webhook":     {"callback", "hook", "dispatchwebhook"},
    "payment":     {"razorpay", "billing", "invoice"},
    "inventory":   {"stock", "stocktransaction", "stockledger", "warehouse"},
}
```

Apply in `buildFTSQuery`: for each query term, if it matches an alias key, append alias terms with OR.

**Impact:** Fixes B6 (0/10 → 4-6/10), helps B2 (auth→oauth/login)

## Phase 4: Graph Expansion Reranking

**Why:** Cross-file failures are missing traversal, not weak scoring. Start with good seeds (top BM25+semantic hits), expand 1-2 hops over calls/defines/imports/routes edges, then rerank the expanded set.

**File:** `internal/search/hybrid.go`

After initial scoring, before final sort:
1. Take top-10 seeds from initial results
2. For each seed, expand 1 hop via `graph.GetCallees()` + `graph.GetCallers()` + route edges
3. Fetch expanded nodes from store
4. Score expanded nodes using the same composite formula
5. Merge expanded results with originals, deduplicate, re-sort

This replaces the "top-20 pair boosting" from the old plan — it's a proper traversal, not just a score bump.

**Impact:** C1 (OrderController → Order.php → services), C2 (InventoryController → Inventory.php → StockLedger), C3 (routes.webhooks.php → WebhookController → Webhook.php)

## Phase 5 (Deprioritized)

### 5A. PHP Parser Coverage
- Trait + interface extraction
- Anonymous function extraction
- Do after routes + graph expansion are validated

### 5B. BM25 Weight Tuning
- Reduce symbol name weight, increase content weight
- Do after automated grading shows remaining gaps

## Execution Order

| Phase | Agents | Depends On |
|-------|--------|------------|
| 1: Automated Grading | 1 Opus | — |
| 2: Route Indexing | 2 parallel Opus (2A+2B, 2C) | Phase 1 (for measurement) |
| 3: Query Aliasing | 1 Opus | — (can parallel with Phase 2) |
| 4: Graph Expansion | 1 Opus | Phase 2 (needs route edges) |

## Verification

After each phase:
1. `go build -tags "fts5" ./...`
2. `go test -tags "fts5" ./... -count=1`
3. Run automated grading harness (Phase 1 output)
4. Compare against previous phase's scores
5. Ensure A1, A3, C4 don't regress

## Critical Files
- `tests/benchmark_grading_test.go` — Phase 1 (new)
- `internal/types/types.go` — Phase 2 (new edge/node types)
- `internal/parser/routes.go` — Phase 2 (new)
- `internal/parser/parser.go` — Phase 2 (wire routes)
- `internal/search/hybrid.go` — Phase 3 + Phase 4
- `benchmarks/human-answers.md` — grading reference

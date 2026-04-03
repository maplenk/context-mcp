# Plan: Improve Code Search Quality

## Context

Human evaluation of qb-context against 15 benchmark queries on the qbapi PHP codebase shows **13.7% hit rate** (14/102 rubric items) on concept/cross-file queries. The C-reference implementation gets **28.2%** (24/85 tested items) using the same scoring formula. See `benchmarks/human-eval-results.md` for full grading against `benchmarks/human-answers.md`. The gap comes from data quality and pre/post-processing, not the formula itself.

Worst failures (full-rubric scoring from `benchmarks/human-eval-results.md`):
- **C1** (order creation): 0/11 — `.end()` methods flood results from "end to end" in query
- **C3** (webhooks): 0/11 — `.handle()` middleware methods flood results from "handling" in query
- **C5** (inventory flow): 0/8 — migration files dominate from "database write" in query
- **C2** (stock transaction): 1/8 — test file ranks high, migrations rank #2-4
- **B6** (omnichannel): 0/10 — "omnichannel" is business vocab, actual code uses "EasyEcom"/"Unicommerce"
- **B2** (auth/session): 0/9 — found Authenticate.php but missed all 9 rubric items

## Phase 1: Quick Wins (3 parallel agents)

### 1A. Code-Aware Stopwords + Phrase Detection
**File:** `internal/search/hybrid.go`

Add common method names to stopwords (lines 42-58):
```
handle, end, get, set, run, start, stop, create, new, make, init,
close, open, read, write, process, execute, call, invoke, begin,
finish, complete, update, delete, remove, add, find, search, check,
test, build, load, save, send, receive
```

These only filter **standalone lowercase query terms** — the existing `isCamelCase` check (line 319) preserves them inside identifiers like `stockTransaction`.

Add `cleanQuery()` before `buildFTSQuery` to strip structural phrases:
- "end to end", "start to finish", "step by step", "front to back", "beginning to end"

**Impact:** Fixes C1 (removes "end" noise), C3 (reduces "handle"/"process" noise), C5 (removes "write"/"complete" noise)

### 1B. Path-Based Scoring Penalties
**File:** `internal/search/hybrid.go`

Replace `isHelperFile()` with `pathPenalty(filePath) float64`:

| Path pattern | Penalty |
|---|---|
| `_ide_helper`, `.d.ts`, `generated/` | 0.3x |
| `vendor/`, `node_modules/`, `lib/` | 0.3x |
| `database/migrations/` | 0.4x |
| `tests/`, `test/`, `*_test.*`, `*Test.*` | 0.5x |
| `examples/` | 0.6x |
| Everything else | 1.0x |

Apply at TWO sites:
1. Line 273 (composite scoring): `composite *= pathPenalty(node.FilePath)` — replaces `isHelperFile` check
2. Lines 494-500 (`applyPerFileCap`): replace `isHelperFile(r.Node.FilePath)` with `pathPenalty(r.Node.FilePath) <= 0.3` for the per-file cap logic (cap helper/vendor files to 1 result per file)

Then delete the `isHelperFile()` function entirely — `pathPenalty()` subsumes it.

**Impact:** Fixes C2 (test file drops from #1), C5 (migrations drop from #1-4)

### 1C. Query Synonym Expansion
**File:** `internal/search/hybrid.go`

Small curated synonym map applied during `buildFTSQuery`:
```go
var querySynonyms = map[string][]string{
    "auth":        {"authentication", "oauth", "login", "session", "token"},
    "api":         {"endpoint", "route", "controller"},
    "inventory":   {"stock", "warehouse"},
    "webhook":     {"callback", "hook"},
    "payment":     {"billing", "invoice", "razorpay"},
    "omnichannel": {"easyecom", "unicommerce", "onlineorder"},
}
```

**Impact:** Fixes B6 ("omnichannel" → actual class names), helps B2 ("auth" → specific middleware)

## Phase 2: PHP Parser Coverage (2 parallel agents)

### 2A. PHP Trait + Interface Extraction
**File:** `internal/parser/parser.go`

Add `case "trait_declaration":` and `case "interface_declaration":` in the PHP switch. Both mirror `class_declaration` handling — extract name, create node, extract methods from body.

### 2B. PHP Anonymous Function Extraction
**File:** `internal/parser/parser.go`

Extract named closures: `$handler = function($x) { ... };` → `NodeTypeFunction` with variable name.

**Impact:** ~1.5x more nodes indexed (18K → 27K+), denser graph for better PPR/betweenness signals

## Phase 3: Graph Cluster Boosting (1 agent)

### 3A. Result Cluster Boosting
**File:** `internal/search/hybrid.go`

After scoring, before sorting: for each pair in top-20 results sharing a graph edge (1-hop), boost both by 1.10x (capped at 1.30x total). Uses existing `GetCallees()`/`GetCallers()` methods.

**Impact:** Helps C2 (StockLedger ↔ Inventory boost), C1 (OrderController ↔ Order.php boost)

## Phase 4: BM25 Tuning (after validation)

### 4A. Reduce Symbol Name Weight
**File:** `internal/storage/sqlite.go` (lines 557, 594)

Change `bm25(nodes_fts, 10.0, 1.0, 0.0)` → `bm25(nodes_fts, 5.0, 2.0, 0.0)`. Let content (doc comments) contribute more.

## Projected Impact

| Phase | Hit Rate (B+C) | Delta |
|---|---|---|
| Current | ~14% (14/102) | -- |
| Phase 1 | ~25% | +11% |
| Phase 2 | ~28% | +3% |
| Phase 3 | ~31% | +3% |
| Phase 4 | ~34% | +3% |

## Execution

Phase 1: 3 parallel Opus agents (1A, 1B, 1C) — independent changes
Phase 2: 2 parallel Opus agents (2A, 2B) — independent parser changes  
Phase 3: 1 agent after Phase 1+2 merge — needs graph + scoring together
Phase 4: 1 agent after validation — needs before/after measurement

## Verification

After each phase:
1. `go build -tags "fts5" ./...`
2. `go test -tags "fts5" ./... -count=1`
3. `QBCONTEXT_REPO=/Users/naman/Documents/QBApps/qbapi go test -tags "fts5,realrepo" -run TestBenchmarkQueries -v ./tests/ -count=1`
4. Compare against `benchmarks/human-answers.md`
5. Ensure A1, A3, C4 (passing queries) don't regress

## Critical Files
- `internal/search/hybrid.go` — Phase 1 + Phase 3
- `internal/search/hybrid_test.go` — all new tests
- `internal/parser/parser.go` — Phase 2
- `internal/parser/parser_test.go` — Phase 2 tests
- `internal/storage/sqlite.go` — Phase 4

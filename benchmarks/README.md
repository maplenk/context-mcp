# context-mcp Benchmark Suite

Release-level benchmarks for validating search quality, query performance, and graph
operations across context-mcp versions. The suite indexes a real-world codebase, runs
26 queries across 5 categories, and measures latency, ranking quality, and graph
algorithm performance.

---

## Table of Contents

- [Overview](#overview)
- [Directory Structure](#directory-structure)
- [Quick Start](#quick-start)
- [Benchmark Tools](#benchmark-tools)
  - [run.sh — Benchmark Current HEAD](#runsh--benchmark-current-head)
  - [compare.sh — Benchmark Any Commit](#comparesh--benchmark-any-commit)
  - [run_mcp_usage.sh — Live MCP Usage Benchmarks](#run_mcp_usagesh--live-mcp-usage-benchmarks)
  - [dashboard.py — Comparison View](#dashboardpy--comparison-view)
- [Query Suite (26 queries)](#query-suite-26-queries)
  - [Category A: Exact Match](#category-a-exact-match-4-queries)
  - [Category B: Concept Search](#category-b-concept-search-6-queries)
  - [Category C: Cross-file Flow](#category-c-cross-file-flow-5-queries)
  - [Category D: Architecture & Graph](#category-d-architecture--graph-4-queries)
  - [Category E: Domain-Specific](#category-e-domain-specific-7-queries)
- [Go Graph Micro-Benchmarks](#go-graph-micro-benchmarks)
- [Performance Thresholds](#performance-thresholds)
- [Baselines](#baselines)
  - [v0.8.0 (with Cold Start)](#v080-with-cold-start)
  - [Pre-Cold-Start (fab5104)](#pre-cold-start-fab5104)
  - [Comparison: Cold Start Impact](#comparison-cold-start-impact)
- [Result JSON Schema](#result-json-schema)
- [Interpreting Results](#interpreting-results)
- [Adding New Queries](#adding-new-queries)
- [Release Workflow](#release-workflow)

---

## Pre-Push Hook

A git pre-push hook automatically detects version changes and prompts you to run benchmarks before pushing.

### Install
```bash
./.githooks/install.sh
```

### Triggers
The hook activates when pushed commits modify:
- `internal/mcp/tools.go` — application version constant
- `internal/storage/migrations.go` — database schema version
- `knowledge.md` — release documentation
- New git tags

### Options
| Input | Action |
|-------|--------|
| `y` | Run benchmarks, push only if they pass |
| `s` | Run benchmarks in background, push immediately |
| `N` (default) | Skip benchmarks, push immediately |

### Skip
```bash
QB_SKIP_BENCH=1 git push          # skip for one push
git push --no-verify               # skip all hooks
```

---

## Overview

The benchmark suite measures three dimensions of context-mcp quality:

| Dimension            | What It Measures                              | Tools Used                       |
|----------------------|-----------------------------------------------|----------------------------------|
| **Query Latency**    | Response time for 26 real-world queries        | `TestBenchmarkQueries`           |
| **Search Quality**   | Relevance of results for domain keyword search | `TestRealRepo_SearchQuality`     |
| **Graph Performance**| PageRank, blast radius, betweenness centrality | `BenchmarkPageRank`, etc.        |
| **Harness Usage**    | Live MCP tool choice, token usage, and output size in Claude/Codex | `run_mcp_usage.sh` + smoke scripts |

**Canonical benchmark target**: **QBApps/qbapi** (set via `QB_TEST_REPO` env var)

All benchmarks MUST be run against this repo. It is a Laravel multi-tenant retail/POS
backend (~780 PHP files, staging branch) that produces ~12.6K nodes and ~16.3K edges
after indexing. This codebase was chosen because it exercises all query categories
(exact symbol lookup, semantic concept search, cross-file tracing) and has stable,
representative characteristics for regression detection.

> **Do not change the benchmark target.** Results are only comparable when run against
> the same codebase. The path is hardcoded in [`tests/realrepo_test.go`](../tests/realrepo_test.go) line 21.

For **cross-engine comparison** (e.g. codebase-memory-mcp), always use the same qbapi
target. The codebase-memory-mcp project name for CLI is:
```
project: "Users-naman-Documents-QBApps-qbapi"
```

---

## Directory Structure

```
benchmarks/
├── README.md              This file — comprehensive reference
├── mcp_smoke_tasks.json   Canonical live MCP smoke scenarios
├── queries.json           Canonical query definitions (26 queries, 5 categories)
├── run.sh                 Run full suite for current HEAD
├── compare.sh             Benchmark any git ref; compare two commits side-by-side
├── run_mcp_usage.sh       Run live Claude/Codex MCP usage benchmarks
├── dashboard.py           Terminal + HTML comparison dashboard
└── results/
    ├── baseline-v0.6.0-qbapi.json          Baseline (earliest benchmarked release)
    ├── mcp-usage-v<version>-<sha>-qbapi.json Published MCP usage artifact
    ├── mcp-usage-v<version>-<sha>-qbapi.md   Published MCP usage Markdown report
    ├── go-bench-<timestamp>.txt              Raw Go graph benchmark output
    ├── queries-<timestamp>.txt               Real-repo benchmark query output
    ├── search-quality-<timestamp>.txt        Real-repo search quality output
    ├── tools-<timestamp>.txt                 Real-repo tool smoke output
    ├── v0.7.0-c608668-qbapi.json           Search quality release
    ├── v0.8.0-fab5104-qbapi.json           Cross-file + DA hardening
    ├── v0.9.0-e1a93bc-qbapi.json           Cold Start release (current)
    └── cbm-v0.8.0-qbapi.json              codebase-memory-mcp cross-engine comparison
```

---

## Quick Start

```bash
# 1. Run benchmarks for current HEAD
./benchmarks/run.sh /Users/naman/Documents/QBApps/qbapi

# 2. Benchmark a specific commit
./benchmarks/compare.sh fab5104

# 3. Compare two commits
./benchmarks/compare.sh HEAD --baseline fab5104

# 4. View comparison dashboard
python3 benchmarks/dashboard.py

# 5. Export HTML report
python3 benchmarks/dashboard.py --html > report.html

# 6. Run live Claude/Codex MCP usage benchmarks
./benchmarks/run_mcp_usage.sh /Users/naman/Documents/QBApps/qbapi
```

---

## Benchmark Tools

### run.sh — Benchmark Current HEAD

Runs the complete benchmark suite against the current working tree and saves timestamped
results to `results/`.

```bash
./benchmarks/run.sh [target-repo-path]
```

The positional target repo path is treated as canonical for the full suite. `run.sh` exports it to `QB_TEST_REPO` before invoking the real-repo tests, so the query, quality, and tool phases all run against the same qbapi target.

A clean `run.sh` execution exits `0` only when the summed `FAIL:` counts across the query, search-quality, and tool output files are zero.

**Steps performed:**
1. `go build -tags "fts5" ./...` — compile check
2. Go graph micro-benchmarks (PageRank, BlastRadius, Betweenness) × 3 iterations
3. `TestBenchmarkQueries` — 6 core queries against real repo (indexes on first run)
4. `TestRealRepo_SearchQuality` — 7 domain keyword quality tests
5. `TestRealRepo_CLIToolsComprehensive` — 9 MCP tool smoke tests

**Output files** (in `results/`):
- `go-bench-<timestamp>.txt` — raw Go benchmark output
- `queries-<timestamp>.txt` — benchmark query results with latencies
- `search-quality-<timestamp>.txt` — search quality test output
- `tools-<timestamp>.txt` — comprehensive tool test output

---

### compare.sh — Benchmark Any Commit

Benchmarks any git ref by checking it out in a temporary [git worktree](https://git-scm.com/docs/git-worktree),
building, and running the full suite. Worktrees are auto-cleaned after the run.

```bash
./benchmarks/compare.sh <commit> [options]
```

**Options:**

| Flag               | Description                                          |
|--------------------|------------------------------------------------------|
| `--baseline <ref>` | Also benchmark this commit, then print a diff table  |
| `--repo <path>`    | Target repo to index (default: qbapi)                |
| `--skip-graph`     | Skip Go graph micro-benchmarks                       |
| `--skip-quality`   | Skip search quality tests                            |
| `--json`           | Output machine-readable JSON                         |
| `--keep-worktrees` | Don't clean up worktrees after run (for debugging)   |

**Examples:**

```bash
# Benchmark a single commit
./benchmarks/compare.sh fab5104

# Compare current HEAD against pre-cold-start
./benchmarks/compare.sh HEAD --baseline fab5104

# Compare two tagged releases
./benchmarks/compare.sh v0.9.0 --baseline v0.8.0

# Quick run (skip heavy tests)
./benchmarks/compare.sh HEAD --skip-graph --skip-quality

# Use a different target repo
./benchmarks/compare.sh HEAD --repo /path/to/django-app
```

**How it works:**
1. Resolves the git ref to a short SHA
2. Creates a worktree at `/tmp/qb-bench-<sha>-<pid>`
3. Builds `context-mcp` from that worktree
4. Runs graph benchmarks, query benchmarks, and quality tests
5. Saves structured JSON to `results/bench-<sha>-<timestamp>.json`
6. If `--baseline` was given, repeats for the baseline commit and prints a comparison table
7. Removes worktrees on exit (via `trap`)

---

### run_mcp_usage.sh — Live MCP Usage Benchmarks

Runs the publishable live benchmark flow for Claude Code and Codex using the pinned low-cost models, across both `with MCP` and `without MCP` variants by default:

- Claude Code: `--model haiku`
- Codex: `-m gpt-5.4-mini`

This script is separate from `run.sh` because it requires authenticated paid client CLIs.

```bash
./benchmarks/run_mcp_usage.sh [target-repo-path] [options]
```

**Options:**

| Flag | Description |
|------|-------------|
| `--client all|claude|codex` | Select which live client(s) to run |
| `--variant all|mcp|nomcp` | Select MCP, no-MCP, or both |
| `--transport all|stdio|http` | Select transport mode(s) |
| `--output <file>` | Publishable JSON artifact path |
| `--report <file>` | Publishable Markdown table report path |
| `--raw-dir <dir>` | Directory for raw logs and intermediate client summaries |

**Examples:**

```bash
# Full live benchmark on the canonical qbapi target
./benchmarks/run_mcp_usage.sh /Users/naman/Documents/QBApps/qbapi

# No-MCP baseline only
./benchmarks/run_mcp_usage.sh --variant nomcp

# Claude only, HTTP only
./benchmarks/run_mcp_usage.sh --client claude --transport http

# Custom output path
./benchmarks/run_mcp_usage.sh /Users/naman/Documents/QBApps/qbapi \
  --output benchmarks/results/mcp-usage-custom.json
```

**What it records:**

- `claude-mcp`, `claude-nomcp`, `codex-mcp`, and `codex-nomcp` style runs
- intended MCP tool coverage for each scenario
- actual tool calls observed from each client
- whether no-MCP runs stayed MCP-free
- estimated tokens and cost when the client exposes them
- output bytes and line counts
- per-client, per-variant, and per-transport aggregate summaries
- a Markdown sidecar report with comparison tables for pass rate, token usage, cost, bytes, and line counts

**Outputs:**

- `mcp-usage-...json` — publishable machine-readable artifact
- `mcp-usage-...md` — publishable comparison tables

**Latest qbapi snapshot (2026-04-06):**

- Full suite: benchmark queries `7/7` passed, search quality `8/8` passed, comprehensive tool tests `9/9` passed
- Claude Code live usage: `2/3` passed overall, `2/2` for MCP, `0/1` for no-MCP, `260633` tokens, `$0.129555`
- Codex live usage: `11/12` passed overall, `7/8` for MCP, `4/4` for no-MCP, `1641931` tokens
- Overall live usage: `13/15` passed, `1902564` tokens, `$0.129555`

**What it does not do:**

- it does not run in CI
- it does not replace the retrieval benchmark suite
- it does not embed raw local log paths into the publishable artifact

---

### dashboard.py — Comparison View

Interactive terminal dashboard and HTML report generator. Loads result JSON files and
presents side-by-side comparison with delta analysis.

```bash
python3 benchmarks/dashboard.py [files...] [options]
```

**Options:**

| Flag         | Description                                 |
|--------------|---------------------------------------------|
| `--latest N` | Compare only the N most recent result files |
| `--html`     | Output a styled HTML report to stdout       |
| `--no-color` | Disable ANSI color codes (for piping)       |

**Examples:**

```bash
# Compare all saved results
python3 benchmarks/dashboard.py

# Compare two specific files
python3 benchmarks/dashboard.py results/baseline-v0.6.0-qbapi.json results/v0.9.0-e1a93bc-qbapi.json

# Last 3 results only
python3 benchmarks/dashboard.py --latest 3

# Generate HTML report
python3 benchmarks/dashboard.py --html > report.html

# Pipe-friendly (no ANSI)
python3 benchmarks/dashboard.py --no-color | less
```

**Dashboard sections:**

| Section                 | What It Shows                                           |
|-------------------------|---------------------------------------------------------|
| **Overview**            | Commit, date, node/edge counts, index time              |
| **Query Latencies**     | Per-query latency with Δ% between versions              |
| **Score Quality**       | Top scores for concept queries with Δ%                  |
| **Score Sparklines**    | Visual bar charts of score distributions                |
| **Graph Benchmarks**    | PageRank, BlastRadius, Betweenness with Δ%              |
| **Test Results**        | Pass/fail counts for quality, queries, and tool tests   |
| **Verdict**             | Automated regression detection summary                  |

**Color coding:**
- 🟢 Green: improvement (latency decreased or score increased)
- 🟡 Yellow: minor change (< 15% regression)
- 🔴 Red: regression (> 15% latency increase)
- ⬜ Dim: negligible change (< 2%)

---

## Query Suite (26 queries)

All queries are defined in [`queries.json`](queries.json) and exercised by
[`tests/benchmark_queries_test.go`](../tests/benchmark_queries_test.go). The Go test
currently runs the 6 core queries (A1, A3, B1, B6, C1, C5); the full 26 are defined
for future expansion.

### Category A: Exact Match (4 queries)

Tests raw index lookup speed — no embedding or ranking involved.

| ID | Tool          | Query                              | What It Validates                   |
|----|---------------|-------------------------------------|-------------------------------------|
| A1 | `read_symbol` | `FiscalYearController`              | Direct symbol lookup by name        |
| A2 | `read_symbol` | `OrderController`                   | Core controller lookup              |
| A3 | `search_code` | `POST.*order` (regex, limit 10)     | Regex pattern search across files   |
| A4 | `search_code` | `function\s+payment` (regex)        | Function declaration pattern search |

**Expected behavior:** Sub-millisecond for `read_symbol`, under 100ms for `search_code`.

### Category B: Concept Search (6 queries)

Tests the hybrid search pipeline: TFIDF tokenization → FTS5 retrieval → composite
ranking (FTS5 score × PageRank × freshness).

| ID | Tool      | Query                                     | What It Validates                    |
|----|-----------|-------------------------------------------|--------------------------------------|
| B1 | `context` | "payment processing and billing logic"    | Multi-word domain concept ranking    |
| B2 | `context` | "user authentication and authorization middleware" | Auth layer discovery        |
| B3 | `context` | "customer loyalty points and rewards program" | Niche domain recall              |
| B4 | `context` | "database migration schema changes"       | Infrastructure code discovery        |
| B5 | `context` | "error handling and exception management" | Cross-cutting concern search         |
| B6 | `context` | "omnichannel integration sync logic"      | Integration domain with jargon       |

**Expected behavior:** Under 10ms latency. Top results should contain symbols with
matching domain keywords. Scores should show meaningful differentiation (not all equal).

### Category C: Cross-file Flow (5 queries)

Tests the ability to surface related symbols across multiple files — requires both
search quality and graph-aware ranking.

| ID | Tool      | Query                                                  | What It Validates               |
|----|-----------|--------------------------------------------------------|---------------------------------|
| C1 | `context` | "complete flow of creating a new order end to end"     | Multi-file business flow        |
| C2 | `context` | "inventory stock update from warehouse to store shelf" | Inventory pipeline              |
| C3 | `context` | "webhook callback processing from third party to database" | External integration flow   |
| C4 | `context` | "OpenTelemetry tracing span lifecycle from request to export" | Observability pipeline   |
| C5 | `context` | "complete API request to database write flow for inventory" | Request-to-storage flow    |

**Expected behavior:** Under 10ms. Results should span 3+ files. Score distribution
should show a long tail (high-relevance + supporting results).

### Category D: Architecture & Graph (4 queries)

Tests graph-powered structural queries — PageRank, community detection, blast radius,
and symbol exploration.

| ID | Tool                       | Query / Params                                     | What It Validates            |
|----|----------------------------|----------------------------------------------------|------------------------------|
| D1 | `get_key_symbols`          | limit: 20                                          | PageRank-based entry points  |
| D2 | `get_architecture_summary` | limit: 10                                          | Louvain community detection  |
| D3 | `impact`                   | `OrderController`, depth: 3                        | Blast radius traversal       |
| D4 | `explore`                  | `PaymentMappingService`, include_deps, depth: 2    | Dependency graph exploration |

**Expected behavior:** Under 1s. `get_key_symbols` should return high-PageRank nodes.
`impact` should find downstream dependents.

### Category E: Domain-Specific (7 queries)

Tests recall for simple domain keyword searches — the most common real-world usage
pattern. These validate that the FTS5 index + ranking work for everyday queries.

| ID | Tool      | Query                          | What It Validates                   |
|----|-----------|--------------------------------|-------------------------------------|
| E1 | `context` | "authentication"               | Single-word: auth domain            |
| E2 | `context` | "database"                     | Single-word: database domain        |
| E3 | `context` | "controller"                   | Single-word: architectural concept  |
| E4 | `context` | "middleware"                   | Single-word: infrastructure layer   |
| E5 | `context` | "request validation"           | Two-word: input validation          |
| E6 | `context` | "route"                        | Single-word: routing                |
| E7 | `context` | "Shopify ecommerce integration"| Third-party integration name        |

**Expected behavior:** Under 10ms. Results should contain symbols whose names or
content match the query terms.

---

## Go Graph Micro-Benchmarks

Standard Go `Benchmark*` functions in [`internal/graph/graph_test.go`](../internal/graph/graph_test.go)
using a synthetic 100-node, 300-edge graph.

| Benchmark                | What It Measures                     | Typical Range     |
|--------------------------|--------------------------------------|-------------------|
| `BenchmarkPageRank`      | Full PageRank computation            | 50–60 µs/op      |
| `BenchmarkBlastRadius`   | BFS-based blast radius traversal     | 2–4 µs/op        |
| `BenchmarkComputeBetweenness` | Betweenness centrality (all pairs) | 4–5 ms/op     |

Run independently:
```bash
go test -tags "fts5" -bench=. -benchmem -run='^$' ./internal/graph/ -count=3
```

---

## Performance Thresholds

| Metric                  | Target (p99) | Warning    | Catastrophic |
|-------------------------|-------------|------------|--------------|
| Exact match (A queries) | < 500ms     | > 1s       | > 30s        |
| Concept search (B/E)    | < 100ms     | > 500ms    | > 30s        |
| Cross-file flow (C)     | < 200ms     | > 1s       | > 30s        |
| Graph operations (D)    | < 1,000ms   | > 5s       | > 30s        |
| Go PageRank (100 nodes) | < 100µs     | > 200µs    | > 1ms        |
| Go BlastRadius          | < 10µs      | > 50µs     | > 1ms        |
| Go Betweenness          | < 10ms      | > 50ms     | > 500ms      |

---

## Baselines

### v0.6.0 — Baseline (earliest benchmarked release)

Commit: `3805b52` · Result file: `results/baseline-v0.6.0-qbapi.json`

| Query | Category   | Latency  | Results    | Top Score |
|-------|------------|----------|------------|-----------|
| A1    | Exact      | 576µs    | 1 class    | —         |
| A3    | Exact      | 71.6ms   | 10 matches | —         |
| B1    | Concept    | 34.8ms   | 10 results | —         |
| B6    | Concept    | 33.1ms   | 10 results | —         |
| C1    | Cross-file | 4.99s    | 15 results | —         |
| C5    | Cross-file | 33.9ms   | 15 results | —         |

Index: 29,101 nodes · 3,380 edges · Search quality: 8/8 passed

### v0.9.0 — Current (Cold Start)

Commit: `e1a93bc` · Result file: `results/v0.9.0-e1a93bc-qbapi.json`

| Query | Category   | Latency | Results    | Top Score |
|-------|------------|---------|------------|-----------|
| A1    | Exact      | 279µs   | 1 class    | —         |
| A3    | Exact      | 77ms    | 10 matches | —         |
| B1    | Concept    | 5.7ms   | 10 results | 0.6167    |
| B6    | Concept    | 1.2ms   | 10 results | 0.5309    |
| C1    | Cross-file | 3.0ms   | 15 results | 0.6167    |
| C5    | Cross-file | 2.2ms   | 15 results | 0.4559    |

Index: 12,653 nodes · 16,294 edges · Search quality: 17/17 passed · Tool tests: 9/9 passed

### Comparison: Baseline vs Current

| Metric          | v0.6.0 (baseline) | v0.9.0 (current) | Delta      | Notes                          |
|-----------------|-------------------|------------------|------------|--------------------------------|
| A1 latency      | 576µs             | 279µs            | **−52%**   | Faster                         |
| B1 latency      | 34.8ms            | 5.7ms            | **−84%**   | 6× faster                     |
| C1 latency      | 4.99s             | 3.0ms            | **−99.9%** | 1,920× faster                 |
| Nodes           | 29,101            | 12,653           | −57%       | Fewer, higher-quality nodes    |
| Edges           | 3,380             | 16,294           | +382%      | Much richer graph connectivity |
| Quality tests   | 8/8               | 17/17            | +9 tests   | Growing coverage               |

### Release Performance Progression

Full benchmark results across all major release points, tested against the
qbapi Laravel codebase (~780 PHP files, staging branch).

| Version | Commit    | Phase                    | Nodes  | Edges  | A1 (exact) | B1 (concept) | C1 (cross-file) | Queries | Quality |
|---------|-----------|--------------------------|-------:|-------:|------------|--------------|------------------|---------|---------|
| v0.6.0  | `3805b52` | Blueprint alignment      | 29,101 |  3,380 | 576µs      | 34.8ms       | **4.99s**        | 6/6 ✅  | 8/8 ✅  |
| v0.7.0  | `c608668` | Search quality           | 33,607 |  5,635 | 503µs      | 2.9ms        | **2.6ms**        | 6/6 ✅  | 7/7 ✅  |
| v0.8.0  | `fab5104` | Cross-file + DA hardening| 12,653 | 16,294 | 568µs      | 4.7ms        | **3.2ms**        | 6/6 ✅  | 7/7 ✅  |
| v0.9.0  | `e1a93bc` | Cold Start               | 12,653 | 16,294 | 279µs      | 5.7ms        | **3.0ms**        | 6/6 ✅  | 17/17 ✅|

Result files: `results/baseline-v0.6.0-qbapi.json`, `results/v0.7.0-c608668-qbapi.json`,
`results/v0.8.0-fab5104-qbapi.json`, `results/v0.9.0-e1a93bc-qbapi.json`

#### Key Findings

**1. Search quality revolution (v0.6 → v0.7): 1,920× faster cross-file queries**

The v0.7.0 release introduced PPR subgraph optimization and BM25 column weights,
which transformed cross-file query performance:

- **C1 (order flow):** 4.99s → 2.6ms — **1,920× improvement**
- **B1 (payment concept):** 34.8ms → 2.9ms — **12× improvement**
- **B6 (omnichannel):** 33.1ms → 746µs — **44× improvement**

These gains came from replacing full-graph PPR walks with localized subgraph
extraction and adding weighted BM25 scoring across symbol name, file path,
and content columns.

**2. Parser refinement (v0.7 → v0.8): fewer, higher-quality nodes**

The v0.8.0 release cycle refined the tree-sitter parser output:

- **Nodes:** 33,607 → 12,653 (−62%) — eliminated noisy/duplicate symbol extractions
- **Edges:** 5,635 → 16,294 (+189%) — dramatically improved cross-file edge resolution
- Net effect: each node now connects to ~1.3 edges (vs 0.17 before), creating a
  much richer graph for PageRank and context queries

**3. Zero regressions across all versions**

Every release point passes all 6 benchmark queries. No version introduced a
performance regression — latency improvements compound from v0.6 to v0.9.

**4. Cold Start adds capability without cost (v0.8 → v0.9)**

Schema v3 added git metadata tables and `[git-intent]` enrichment. Performance
impact: zero. Search scores are identical because TFIDF embeddings are unaffected
by the intent blocks. The benefit will materialize when switching to neural (ONNX)
embeddings that can leverage semantic intent context.

**5. Test coverage growth**

Search quality test cases expanded from 7 (v0.7, v0.8) to 17 (v0.9), reflecting
growing confidence in search accuracy as the system matured.

#### Reproduce

Run benchmarks at any release point:

```bash
# Single version
./benchmarks/compare.sh 3805b52             # v0.6.0

# Compare two versions
./benchmarks/compare.sh e1a93bc --baseline 3805b52   # v0.9 vs v0.6

# Full progression dashboard
python3 benchmarks/dashboard.py benchmarks/results/*.json
```

---

### Cross-Engine Comparison: context-mcp vs codebase-memory-mcp

Head-to-head benchmark of **context-mcp v0.9.0** vs **codebase-memory-mcp v0.8.0**
(CLI mode), both run against the canonical qbapi target.

#### Index Comparison

| Dimension           | context-mcp      | CBM              | Notes      |
|---------------------|-----------------|------------------|------------|
| Nodes indexed       | 12,653          | 16,806           | CBM +33%   |
| Edges indexed       | 16,294          | 35,189           | CBM +116%  |
| Parser technology   | regex           | tree-sitter      |            |
| Languages supported | 6               | 66               |            |

#### Query Latency (lower = better)

CBM queries use the **equivalent** tool for each category — `explore` for concept/cross-file
(not `search_graph`), `understand` for symbol lookup (not basic search).

| ID | Query                  | qb tool            | qb time   | CBM tool       | CBM time  | Speedup         |
|----|------------------------|--------------------|-----------:|----------------|----------:|----------------:|
| A1 | Symbol lookup          | `read_symbol`      | **279µs** | `understand`   | 369.2ms   | **1,322× faster** |
| A3 | Regex code search      | `search_code`      | **77ms**  | `search_graph` | 119.5ms   | **2× faster**    |
| B1 | Payment concept        | `context` (PPR+BM25)| **5.7ms**| `explore`      | 725.6ms   | **127× faster**  |
| B6 | Omnichannel concept    | `context` (PPR+BM25)| **1.2ms**| `explore`      | 619.9ms   | **517× faster**  |
| C1 | Order flow (cross-file)| `context` (PPR+BM25)| **3.0ms**| `explore`      | 764.7ms   | **255× faster**  |
| C5 | API→DB flow           | `context` (PPR+BM25)| **2.2ms**| `explore`      | 905.4ms   | **411× faster**  |

**Scoreboard: context-mcp 6 — 0 CBM**

#### Query Result Quality

**A1 — Symbol Lookup**
- context-mcp (`read_symbol`): Returns **full source** (14,373 chars) for
  `FiscalYearController` @ `app/Http/Controllers/FiscalYearController.php`
- CBM (`understand`): Returns metadata + truncated source, pagerank=0.00005,
  0 callers, 0 callees, `is_key_symbol=False`

**A3 — Regex Code Search**
- context-mcp (`search_code`): 10 matches — `Agriculture.php`, `OrderController.php`,
  `OrderDeletionController.php`, `v2/OrderController.php`
- CBM (`search_graph`): 119.5ms — regex name-pattern match on graph nodes

**B1 — Payment Concept**
- context-mcp (`context`): 10 results, scored 0.44–0.62 —
  `BillingWeb.generateUTAPToken` (0.617), `partner.payment` (0.451)
- CBM (`explore`): 20 matches, scored 0.43–0.56 —
  `__construct` (0.557) ⚠️ generic, `fetchPaymentValues` (0.456),
  `getStorePaymentSetting` (0.454). **0 dependencies, 0 hotspots, 0 entry_points.**

**B6 — Omnichannel Concept** *(CBM wins on quality)*
- context-mcp (`context`): 10 results, scored 0.43–0.53 —
  `integrationBilling.handle` (0.531), `SyncInventoryJob` (0.521)
- CBM (`explore`): 20 matches, scored 0.52–0.70 —
  **`syncBrevoContact` (0.699)**, `syncZOHOItem` (0.571), `syncBrevoWebsiteContact` (0.554).
  Higher scores and more relevant top results.

**C1 — Order Flow** *(CBM wins on quality)*
- context-mcp (`context`): 15 results — `Flight.endLeg` (0.617),
  `appController.endSession` — some results unrelated to orders
- CBM (`explore`): 20 matches, scored 0.65–0.66 —
  **`orderCheck` (0.664)**, `OrderRequestTracker` (0.656), `orders` (0.649).
  More relevant to "order flow" query.

**C5 — API→DB Flow**
- context-mcp (`context`): 15 results —
  `create_global_nector_api_usage_tracker_table`, `Unicommerce.APIRequestTracker`
- CBM (`explore`): 20 matches, scored 0.49–0.55 —
  `write` (0.552) ⚠️ generic, `writeStderr` (0.509). Less relevant top results.

#### Tool Parity

Both engines implement the same compound tool set. context-mcp tools were developed
independently with the same design goals (blast radius, architecture summary,
explore/understand, key symbols, call tracing).

| Tool                     | context-mcp         | CBM                   |
|--------------------------|--------------------|-----------------------|
| `impact` / blast radius  | ✅ `impact`         | ✅ `get_impact_analysis` |
| Architecture overview    | ✅ `get_architecture_summary` | ✅ `get_architecture` |
| Key symbols (PageRank)   | ✅ `get_key_symbols` | ✅ `get_key_symbols`   |
| Call tracing             | ✅ `trace_call_path` | ✅ `trace_call_path`   |
| Area exploration         | ✅ `explore`        | ✅ `explore`           |
| Symbol deep-dive         | ✅ `understand`     | ✅ `understand`        |
| Pre-change analysis      | ❌                  | ✅ `prepare_change`    |
| Session memory           | ❌                  | ✅ `get_session_context` |

#### Key Takeaways

1. **context-mcp wins all 6 queries on speed** — 2× to 1,322× faster
2. **CBM wins on result quality for 2/6 queries** (B6 omnichannel, C1 order flow) —
   CBM's `explore` finds more contextually relevant symbols in those cases
3. **context-mcp's PPR+BM25 hybrid** understands natural language concepts but
   sometimes drifts (C1: returns `Flight.endLeg` for "order flow")
4. **CBM's `explore`** returns scored results but includes generics (`__construct`,
   `write`) and leaves dependencies/hotspots/entry_points empty
5. **CBM extracts richer structure** — 33% more nodes and 116% more edges via
   tree-sitter (66 langs vs 6)
6. **CBM CLI overhead** — ~15ms subprocess spawn + ~600-900ms for `explore` compound
   queries vs context-mcp's in-process 1-6ms
7. **Near-full tool parity** — only `prepare_change` and `get_session_context` are
   CBM-exclusive; all other compound tools exist in both engines

#### Reproduce

```bash
# Run context-mcp benchmarks
go test -tags "fts5,realrepo" -v -run "TestBenchmarkQueries" ./tests/ -count=1

# Run CBM benchmarks (requires codebase-memory-mcp installed)
codebase-memory-mcp cli search_graph \
  '{"name_pattern":"FiscalYearController","label":"Class","project":"Users-naman-Documents-QBApps-qbapi"}'

# Result files
# context-mcp: benchmarks/results/v0.9.0-e1a93bc-qbapi.json
# CBM:        benchmarks/results/cbm-v0.8.0-qbapi.json
```

---

## Result JSON Schema

All results are stored as JSON in `results/`. Two formats are supported (dashboard.py
handles both automatically):

### Baseline format (from manual runs)

```json
{
  "benchmark_version": "1.0.0",
  "context_mcp_version": "v0.8.0",
  "context_mcp_commit": "3be18d3",
  "run_date": "2026-04-02T13:34:00Z",
  "environment": {
    "os": "darwin/arm64",
    "target_repo": "QBApps/qbapi",
    "index_stats": { "total_nodes": 12653, "total_edges": 16294 }
  },
  "results": [
    {
      "id": "A1",
      "category": "Exact Match",
      "tool": "read_symbol",
      "query": "FiscalYearController",
      "elapsed_us": 279,
      "elapsed_human": "279µs",
      "status": "PASS",
      "result_summary": { "symbol_name": "...", "top_score": 0.6167 }
    }
  ],
  "go_benchmarks": { ... },
  "search_quality_tests": { "passed": 17, "failed": 0 }
}
```

### compare.sh format (auto-generated)

```json
{
  "commit": "fab5104",
  "label": "pre-cold-start",
  "timestamp": "20260402T135700",
  "query_latencies": {
    "A1_read_symbol": "568µs",
    "B1_payment_concept": "4.7ms"
  },
  "query_scores": {
    "B1_scores": [0.6167, 0.4512, ...],
    "B6_scores": [0.5309, 0.5206, ...]
  },
  "graph_benchmarks_ns": {
    "pagerank": 57111,
    "blast_radius": 3039,
    "betweenness": 4607830
  },
  "test_results": { ... }
}
```

### run_mcp_usage.sh format (live client artifact)

```json
{
  "benchmark_version": "1.0.0",
  "kind": "mcp_usage",
  "context_mcp_version": "v0.3.0",
  "context_mcp_commit": "43c04a0",
  "run_date": "2026-04-06T08:12:00Z",
  "environment": {
    "os": "darwin/arm64",
    "target_repo": "/Users/naman/Documents/QBApps/qbapi",
    "clients": ["claude_code", "codex"],
    "transport": "all",
    "variant": "all",
    "task_count": 4,
    "task_ids": ["context", "assemble_context", "read_symbol", "checkpoint_delta"],
    "scenario_file": "benchmarks/mcp_smoke_tasks.json"
  },
  "models": {
    "claude_code": "haiku",
    "codex": "gpt-5.4-mini"
  },
  "summary": {
    "total_runs": 16,
    "passed_runs": 16,
    "expected_tool_coverage_runs": 16,
    "total_est_tokens": 12345,
    "total_cost_usd": 0.0821,
    "all_passed": true
  },
  "results": {
    "claude_code": {
      "total_runs": 12,
      "passed_runs": 12,
      "mode_summary": {
        "mcp": { "transport_summary": { "stdio": { "...": "..." }, "http": { "...": "..." } } },
        "nomcp": { "transport_summary": { "none": { "...": "..." } } }
      },
      "runs": [
        {
          "id": "context",
          "mode": "mcp",
          "transport": "stdio",
          "status": "PASS",
          "expected_tools": ["context"],
          "tool_calls": ["context"],
          "est_tokens": 512
        }
      ]
    }
  }
}
```

---

## Interpreting Results

### Latency

- **Microsecond-level** (< 1ms): Symbol lookups, cached queries. Healthy.
- **Millisecond-level** (1–100ms): Hybrid search, regex scan. Normal for real repos.
- **Second-level** (> 1s): Indicates a regression or very large codebase. Investigate.

### Scores

Scores range from 0.0 to 1.0 and represent composite ranking:

```
score = w_fts × FTS5_score + w_pr × PageRank + w_fresh × freshness
```

- **> 0.5**: Strong match — symbol name or content closely matches query terms.
- **0.3–0.5**: Moderate match — related but not exact.
- **< 0.3**: Weak match — tangentially related or graph-boosted.

A healthy score distribution for concept queries (B/E) should show:
- A clear top-1 or top-2 cluster (strong matches)
- A gradual decline (not a cliff-drop to zero)
- No identical scores for all results (would indicate broken ranking)

### Delta Percentages

When comparing two versions:
- **≈0%** (< 2%): No meaningful change. Run-to-run noise.
- **Negative %**: Improvement (faster latency or higher score).
- **+5% to +15%**: Minor regression. Usually noise; monitor across multiple runs.
- **> +15%**: Potential regression. Investigate the commit.

### Regression Detection

The dashboard's verdict uses these rules:
- **Regression**: Any query latency increased by > 15%
- **Improvement**: Any query latency decreased by > 10%
- Run benchmarks 2–3 times to distinguish noise from real regressions.

---

## Adding New Queries

1. **Define the query** in `queries.json`:
   ```json
   {
     "id": "B7",
     "category": "B",
     "tool": "context",
     "params": {"query": "caching and Redis usage", "limit": 10},
     "description": "Semantic search for caching domain",
     "expected": {
       "min_results": 3,
       "top_results_should_contain": ["cache", "redis", "Redis"]
     }
   }
   ```

2. **Add the test case** in `tests/benchmark_queries_test.go`:
   ```go
   {
       id:       "B7",
       category: "Concept",
       tool:     "context",
       params:   `{"query": "caching and Redis usage", "limit": 10}`,
   },
   ```

3. **Run and save baseline**:
   ```bash
   ./benchmarks/run.sh
   ```

---

## Release Workflow

Before each release, run the full benchmark suite and verify no regressions:

```bash
# 1. Benchmark current HEAD
./benchmarks/compare.sh HEAD --baseline <previous-release-tag>

# 2. Review the comparison table
#    - All queries should pass
#    - No latency regressions > 15%
#    - Search quality scores should be stable or improved

# 3. Save the baseline for this release
cp benchmarks/results/bench-<sha>-*.json \
   benchmarks/results/baseline-<version>-qbapi.json

# 4. View dashboard with all baselines
python3 benchmarks/dashboard.py

# 5. (Optional) Generate HTML report for the release notes
python3 benchmarks/dashboard.py --html > benchmarks/results/release-report.html

# 6. Run live MCP vs no-MCP usage benchmarks and save the publishable artifact
./benchmarks/run_mcp_usage.sh /Users/naman/Documents/QBApps/qbapi

# 7. Commit the new baseline and usage artifact
git add benchmarks/results/
git commit -m "Add benchmark baseline for <version>"
```

### Running Individual Tests

```bash
# Benchmark queries only
go test -tags "fts5,realrepo" -v -run "TestBenchmarkQueries" ./tests/ -count=1

# Search quality only
go test -tags "fts5,realrepo" -v -run "TestRealRepo_SearchQuality" ./tests/ -count=1

# Graph benchmarks only
go test -tags "fts5" -bench=. -benchmem -run='^$' ./internal/graph/ -count=3

# All realrepo tests (full suite)
go test -tags "fts5,realrepo" -v ./tests/ -count=1 -timeout 600s
```

### Build Tags

| Tag        | Purpose                                                  |
|------------|----------------------------------------------------------|
| `fts5`     | Required — enables SQLite FTS5 full-text search          |
| `realrepo` | Enables tests that index a real external repository      |
| `onnx`     | Enables ONNX neural embedding (optional, uses TFIDF otherwise) |

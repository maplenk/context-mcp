# qb-context Benchmark Suite

Release-level benchmarks for validating search quality, query performance, and graph operations.

## Structure

```
benchmarks/
├── README.md                              # This file
├── queries.json                           # Canonical query definitions (26 queries, 5 categories)
├── run.sh                                 # One-command benchmark runner
└── results/
    └── baseline-v0.8.0-qbapi.json         # Baseline results (April 2026)
```

## Quick Start

```bash
chmod +x benchmarks/run.sh
./benchmarks/run.sh /path/to/target/repo
```

Default target: `/Users/naman/Documents/QBApps/qbapi` (Laravel, ~12.6K nodes)

## Query Categories

| Cat | Name             | Count | What It Tests                              |
|-----|------------------|-------|--------------------------------------------|
| A   | Exact Match      | 4     | Symbol index lookup, regex pattern search   |
| B   | Concept Search   | 6     | Semantic/hybrid ranking, embedding quality  |
| C   | Cross-file Flow  | 5     | Multi-file flow tracing, graph integration  |
| D   | Architecture     | 4     | PageRank, communities, blast radius         |
| E   | Domain-Specific  | 7     | Single/multi-word domain keyword recall     |

## Performance Thresholds

| Metric                  | Target    | Catastrophic |
|-------------------------|-----------|--------------|
| Exact match (p99)       | < 500ms   | > 30s        |
| Concept search (p99)    | < 100ms   | > 30s        |
| Cross-file flow (p99)   | < 200ms   | > 30s        |
| Graph operations (p99)  | < 1000ms  | > 30s        |

## Baseline (v0.8.0, qbapi)

| Query | Category       | Latency  | Results |
|-------|----------------|----------|---------|
| A1    | Exact          | 279µs    | 1 class (FiscalYearController) |
| A3    | Exact          | 77ms     | 10 regex matches |
| B1    | Concept        | 5.7ms    | 10 results, top=0.62 |
| B6    | Concept        | 1.2ms    | 10 results, top=0.53 |
| C1    | Cross-file     | 3.0ms    | 15 results across 11 files |
| C5    | Cross-file     | 2.2ms    | 15 results across 11 files |

### Go Graph Benchmarks (100-node synthetic graph)

| Benchmark              | ns/op     | B/op     | allocs/op |
|------------------------|-----------|----------|-----------|
| PageRank               | 54,799    | 61,608   | 829       |
| BlastRadius            | 2,977     | 5,192    | 74        |
| ComputeBetweenness     | 4,455,879 | 2,761,841| 40,357    |

## Running Individual Tests

```bash
# Benchmark queries only
go test -tags "fts5,realrepo" -v -run "TestBenchmarkQueries" ./tests/ -count=1

# Search quality only
go test -tags "fts5,realrepo" -v -run "TestRealRepo_SearchQuality" ./tests/ -count=1

# Graph benchmarks only
go test -tags "fts5" -bench=. -benchmem -run='^$' ./internal/graph/ -count=3

# All realrepo tests
go test -tags "fts5,realrepo" -v ./tests/ -count=1 -timeout 600s
```

## Adding New Benchmark Queries

1. Add query definition to `queries.json` with an ID, category, tool, params, and expected results
2. Add the corresponding test case in `tests/benchmark_queries_test.go`
3. Run the suite and save new baseline: `./benchmarks/run.sh`

## Comparing Results

Results are saved as timestamped files in `results/`. Compare baselines with:

```bash
# Quick latency comparison
jq '.results[] | {id, elapsed_human}' results/baseline-v0.8.0-qbapi.json
```

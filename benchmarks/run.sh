#!/usr/bin/env bash
# benchmarks/run.sh — Run the full qb-context benchmark suite
# Usage: ./benchmarks/run.sh [qbapi-path]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET_REPO="${1:-/Users/naman/Documents/QBApps/qbapi}"
RESULTS_DIR="$REPO_ROOT/benchmarks/results"
TIMESTAMP=$(date +%Y%m%dT%H%M%S)

echo "=== qb-context Benchmark Suite ==="
echo "Target repo: $TARGET_REPO"
echo "Timestamp:   $TIMESTAMP"
echo ""

if [ ! -d "$TARGET_REPO" ]; then
    echo "ERROR: Target repo not found at $TARGET_REPO"
    echo "Usage: $0 /path/to/target/repo"
    exit 1
fi

mkdir -p "$RESULTS_DIR"

# 1. Build
echo "--- Building qb-context ---"
cd "$REPO_ROOT"
go build -tags "fts5" ./...
echo "Build: OK"
echo ""

# 2. Go graph benchmarks (synthetic 100-node graph)
echo "--- Go Graph Benchmarks (3 iterations) ---"
BENCH_OUT="$RESULTS_DIR/go-bench-$TIMESTAMP.txt"
go test -tags "fts5" -bench=. -benchmem -run='^$' ./internal/graph/ -count=3 \
    | tee "$BENCH_OUT"
echo ""
echo "Saved: $BENCH_OUT"
echo ""

# 3. Benchmark queries (real repo, requires indexing)
echo "--- Benchmark Queries (real repo) ---"
QUERY_OUT="$RESULTS_DIR/queries-$TIMESTAMP.txt"
go test -tags "fts5,realrepo" -v -run "TestBenchmarkQueries" ./tests/ -count=1 -timeout 600s \
    2>&1 | tee "$QUERY_OUT"
echo ""
echo "Saved: $QUERY_OUT"
echo ""

# 4. Search quality tests
echo "--- Search Quality Tests ---"
QUALITY_OUT="$RESULTS_DIR/search-quality-$TIMESTAMP.txt"
go test -tags "fts5,realrepo" -v -run "TestRealRepo_SearchQuality" ./tests/ -count=1 -timeout 600s \
    2>&1 | tee "$QUALITY_OUT"
echo ""
echo "Saved: $QUALITY_OUT"
echo ""

# 5. Comprehensive tool tests
echo "--- Comprehensive Tool Tests ---"
TOOLS_OUT="$RESULTS_DIR/tools-$TIMESTAMP.txt"
go test -tags "fts5,realrepo" -v -run "TestRealRepo_CLIToolsComprehensive" ./tests/ -count=1 -timeout 600s \
    2>&1 | tee "$TOOLS_OUT"
echo ""
echo "Saved: $TOOLS_OUT"
echo ""

# 6. Summary
echo "=== Benchmark Suite Complete ==="
echo "Results saved to: $RESULTS_DIR/"
echo ""
echo "Quick check:"
grep -c "PASS:" "$QUERY_OUT" 2>/dev/null | xargs -I{} echo "  Benchmark queries passed: {}"
grep -c "PASS:" "$QUALITY_OUT" 2>/dev/null | xargs -I{} echo "  Search quality passed:    {}"
grep -c "PASS:" "$TOOLS_OUT" 2>/dev/null | xargs -I{} echo "  Tool tests passed:        {}"
echo ""
FAIL_COUNT=$(grep -c "FAIL:" "$QUERY_OUT" "$QUALITY_OUT" "$TOOLS_OUT" 2>/dev/null || echo 0)
if [ "$FAIL_COUNT" = "0" ]; then
    echo "✅ All benchmarks passed"
else
    echo "❌ $FAIL_COUNT failures detected — review output files"
    exit 1
fi

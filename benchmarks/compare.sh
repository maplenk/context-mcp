#!/usr/bin/env bash
# benchmarks/compare.sh — Run benchmarks for any commit and compare against a baseline
#
# Usage:
#   ./benchmarks/compare.sh <commit>                        # benchmark a commit
#   ./benchmarks/compare.sh <commit> --baseline <commit>    # benchmark + compare two commits
#   ./benchmarks/compare.sh <commit> --repo /path/to/repo   # use a different target repo
#
# Examples:
#   ./benchmarks/compare.sh HEAD
#   ./benchmarks/compare.sh fab5104
#   ./benchmarks/compare.sh main --baseline fab5104
#   ./benchmarks/compare.sh v0.9.0 --baseline v0.8.0 --repo /path/to/laravel-app
#
# Options:
#   --baseline <ref>     Compare against this commit (runs both, prints diff table)
#   --repo <path>        Target repo to index (default: $QB_TEST_REPO or /path/to/test/repo)
#   --skip-graph         Skip Go graph micro-benchmarks
#   --skip-quality       Skip search quality tests
#   --json               Output machine-readable JSON summary
#   --keep-worktrees     Don't clean up worktrees after run (for debugging)
#   -h, --help           Show this help message
#
set -euo pipefail

# ─── Defaults ───────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TARGET_REPO="${QB_TEST_REPO:-/path/to/test/repo}"
RESULTS_DIR="$SCRIPT_DIR/results"
COMMIT=""
BASELINE=""
SKIP_GRAPH=false
SKIP_QUALITY=false
JSON_OUTPUT=false
KEEP_WORKTREES=false
TIMESTAMP=$(date +%Y%m%dT%H%M%S)

# ─── Colors ─────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
RESET='\033[0m'

# ─── Help ───────────────────────────────────────────────────────────────────────
usage() {
    sed -n '2,/^$/{ s/^# //; s/^#$//; p }' "$0"
    exit 0
}

# ─── Argument parsing ──────────────────────────────────────────────────────────
[[ $# -eq 0 ]] && usage
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)       usage ;;
        --baseline)      BASELINE="$2"; shift 2 ;;
        --repo)          TARGET_REPO="$2"; shift 2 ;;
        --skip-graph)    SKIP_GRAPH=true; shift ;;
        --skip-quality)  SKIP_QUALITY=true; shift ;;
        --json)          JSON_OUTPUT=true; shift ;;
        --keep-worktrees) KEEP_WORKTREES=true; shift ;;
        -*)              echo "Unknown option: $1"; usage ;;
        *)
            if [[ -z "$COMMIT" ]]; then
                COMMIT="$1"
            else
                echo "Unexpected argument: $1"; usage
            fi
            shift ;;
    esac
done

[[ -z "$COMMIT" ]] && { echo "ERROR: No commit specified"; usage; }

# ─── Validation ─────────────────────────────────────────────────────────────────
if [ ! -d "$TARGET_REPO" ]; then
    echo -e "${RED}ERROR: Target repo not found at $TARGET_REPO${RESET}"
    echo "Use --repo /path/to/repo to specify a different target"
    exit 1
fi

cd "$REPO_ROOT"

# Resolve refs to short SHAs
resolve_ref() {
    git rev-parse --short "$1" 2>/dev/null || { echo -e "${RED}ERROR: Cannot resolve ref '$1'${RESET}"; exit 1; }
}

COMMIT_SHA=$(resolve_ref "$COMMIT")
COMMIT_MSG=$(git --no-pager log --oneline -1 "$COMMIT_SHA" 2>/dev/null | cut -d' ' -f2-)
BASELINE_SHA=""
BASELINE_MSG=""
if [[ -n "$BASELINE" ]]; then
    BASELINE_SHA=$(resolve_ref "$BASELINE")
    BASELINE_MSG=$(git --no-pager log --oneline -1 "$BASELINE_SHA" 2>/dev/null | cut -d' ' -f2-)
fi

mkdir -p "$RESULTS_DIR"

# ─── Worktree helpers ───────────────────────────────────────────────────────────
WORKTREES=()

create_worktree() {
    local ref="$1"
    local sha="$2"
    local dir="/tmp/qb-bench-${sha}-$$"
    echo -e "${DIM}  Creating worktree at $dir for $sha...${RESET}"
    git worktree add "$dir" "$sha" --quiet 2>/dev/null
    WORKTREES+=("$dir")
    echo "$dir"
}

cleanup_worktrees() {
    if $KEEP_WORKTREES; then
        echo -e "${DIM}Keeping worktrees: ${WORKTREES[*]}${RESET}"
        return
    fi
    for wt in "${WORKTREES[@]}"; do
        if [ -d "$wt" ]; then
            git worktree remove "$wt" --force 2>/dev/null || true
        fi
    done
}
trap cleanup_worktrees EXIT

# ─── Benchmark runner ──────────────────────────────────────────────────────────
# Runs the full benchmark suite in a given directory, outputs a JSON file
run_benchmarks() {
    local work_dir="$1"
    local label="$2"
    local sha="$3"
    local out_file="$RESULTS_DIR/bench-${sha}-${TIMESTAMP}.json"

    echo -e "${BOLD}${CYAN}━━━ Benchmarking ${label} (${sha}) ━━━${RESET}"
    echo ""

    cd "$work_dir"

    # Build
    echo -e "  ${BOLD}[1/4] Building...${RESET}"
    if ! go build -tags "fts5" ./... 2>/dev/null; then
        echo -e "  ${RED}BUILD FAILED${RESET}"
        echo '{"error":"build_failed"}' > "$out_file"
        echo "$out_file"
        return 1
    fi
    echo -e "  ${GREEN}Build OK${RESET}"

    # Graph benchmarks
    local pagerank_ns=0 blast_ns=0 between_ns=0
    if ! $SKIP_GRAPH; then
        echo -e "  ${BOLD}[2/4] Graph micro-benchmarks...${RESET}"
        local graph_raw
        graph_raw=$(go test -tags "fts5" -bench=. -benchmem -run='^$' ./internal/graph/ -count=3 2>/dev/null || true)
        pagerank_ns=$(echo "$graph_raw" | grep "BenchmarkPageRank" | awk '{sum+=$3; n++} END{printf "%.0f", sum/n}')
        blast_ns=$(echo "$graph_raw" | grep "BenchmarkBlastRadius" | awk '{sum+=$3; n++} END{printf "%.0f", sum/n}')
        between_ns=$(echo "$graph_raw" | grep "BenchmarkComputeBetweenness" | awk '{sum+=$3; n++} END{printf "%.0f", sum/n}')
        echo -e "  ${GREEN}PageRank: ${pagerank_ns}ns | BlastRadius: ${blast_ns}ns | Betweenness: ${between_ns}ns${RESET}"
    else
        echo -e "  ${DIM}[2/4] Graph benchmarks skipped${RESET}"
    fi

    # Benchmark queries (real repo)
    echo -e "  ${BOLD}[3/4] Benchmark queries (indexing + querying)...${RESET}"
    local query_raw
    query_raw=$(go test -tags "fts5,realrepo" -v -run "TestBenchmarkQueries" ./tests/ -count=1 -timeout 600s 2>&1 || true)

    # Parse query results
    local total_nodes total_edges index_time
    total_nodes=$(echo "$query_raw" | grep -o '[0-9]* nodes' | head -1 | awk '{print $1}')
    total_edges=$(echo "$query_raw" | grep -o '[0-9]* edges' | head -1 | awk '{print $1}')
    index_time=$(echo "$query_raw" | grep "^ok" | awk '{print $NF}' | tr -d 's')

    # Extract per-query latencies
    local a1_us a3_us b1_us b6_us c1_us c5_us
    a1_us=$(echo "$query_raw" | grep "Query A1" -A2 | grep "Elapsed:" | grep -oE '[0-9.]+[µm]s' | head -1)
    a3_us=$(echo "$query_raw" | grep "Query A3" -A2 | grep "Elapsed:" | grep -oE '[0-9.]+[µm]s' | head -1)
    b1_us=$(echo "$query_raw" | grep "Query B1" -A2 | grep "Elapsed:" | grep -oE '[0-9.]+[µm]s' | head -1)
    b6_us=$(echo "$query_raw" | grep "Query B6" -A2 | grep "Elapsed:" | grep -oE '[0-9.]+[µm]s' | head -1)
    c1_us=$(echo "$query_raw" | grep "Query C1" -A2 | grep "Elapsed:" | grep -oE '[0-9.]+[µm]s' | head -1)
    c5_us=$(echo "$query_raw" | grep "Query C5" -A2 | grep "Elapsed:" | grep -oE '[0-9.]+[µm]s' | head -1)

    local query_pass query_fail
    query_pass=$(echo "$query_raw" | grep -c "PASS:" || echo 0)
    query_fail=$(echo "$query_raw" | grep -c "FAIL:" || echo 0)

    echo -e "  ${GREEN}Queries: ${query_pass} passed, ${query_fail} failed${RESET}"
    echo -e "  ${DIM}  A1=${a1_us} A3=${a3_us} B1=${b1_us} B6=${b6_us} C1=${c1_us} C5=${c5_us}${RESET}"

    # Search quality
    local quality_pass=0 quality_fail=0
    if ! $SKIP_QUALITY; then
        echo -e "  ${BOLD}[4/4] Search quality tests...${RESET}"
        local quality_raw
        quality_raw=$(go test -tags "fts5,realrepo" -v -run "TestRealRepo_SearchQuality" ./tests/ -count=1 -timeout 600s 2>&1 || true)
        quality_pass=$(echo "$quality_raw" | grep -c "PASS:" || echo 0)
        quality_fail=$(echo "$quality_raw" | grep -c "FAIL:" || echo 0)
        echo -e "  ${GREEN}Quality: ${quality_pass} passed, ${quality_fail} failed${RESET}"
    else
        echo -e "  ${DIM}[4/4] Search quality skipped${RESET}"
    fi

    # Extract score distributions from query output
    local b1_scores b6_scores
    b1_scores=$(echo "$query_raw" | grep -A20 "Query B1" | grep "score=" | grep -oE 'score=[0-9.]+' | sed 's/score=//' | tr '\n' ',' | sed 's/,$//')
    b6_scores=$(echo "$query_raw" | grep -A20 "Query B6" | grep "score=" | grep -oE 'score=[0-9.]+' | sed 's/score=//' | tr '\n' ',' | sed 's/,$//')

    # Write JSON
    cat > "$out_file" <<EOF
{
  "commit": "$sha",
  "label": "$label",
  "timestamp": "$TIMESTAMP",
  "target_repo": "$TARGET_REPO",
  "index_stats": {
    "total_nodes": ${total_nodes:-0},
    "total_edges": ${total_edges:-0},
    "indexing_time_s": ${index_time:-0}
  },
  "query_latencies": {
    "A1_read_symbol": "$a1_us",
    "A3_search_code": "$a3_us",
    "B1_payment_concept": "$b1_us",
    "B6_omnichannel_concept": "$b6_us",
    "C1_order_flow": "$c1_us",
    "C5_api_db_flow": "$c5_us"
  },
  "query_scores": {
    "B1_scores": [$b1_scores],
    "B6_scores": [$b6_scores]
  },
  "test_results": {
    "benchmark_queries_passed": $query_pass,
    "benchmark_queries_failed": $query_fail,
    "search_quality_passed": $quality_pass,
    "search_quality_failed": $quality_fail
  },
  "graph_benchmarks_ns": {
    "pagerank": ${pagerank_ns:-0},
    "blast_radius": ${blast_ns:-0},
    "betweenness": ${between_ns:-0}
  }
}
EOF

    echo ""
    echo -e "  ${DIM}Saved: $out_file${RESET}"
    echo "$out_file"
}

# ─── Print comparison table ────────────────────────────────────────────────────
print_comparison() {
    local file_a="$1"
    local file_b="$2"
    local label_a="$3"
    local label_b="$4"

    echo ""
    echo -e "${BOLD}${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
    echo -e "${BOLD}  COMPARISON: ${label_a} vs ${label_b}${RESET}"
    echo -e "${BOLD}${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
    echo ""

    # Use python for reliable JSON parsing and table formatting
    python3 - "$file_a" "$file_b" "$label_a" "$label_b" <<'PYEOF'
import json, sys

with open(sys.argv[1]) as f: a = json.load(f)
with open(sys.argv[2]) as f: b = json.load(f)
la, lb = sys.argv[3], sys.argv[4]

def parse_latency_us(s):
    """Convert latency string to microseconds for comparison."""
    if not s: return 0
    s = s.strip()
    if s.endswith("µs"):
        return float(s[:-2])
    elif s.endswith("ms"):
        return float(s[:-2]) * 1000
    elif s.endswith("s"):
        return float(s[:-1]) * 1_000_000
    return 0

def fmt_delta(old, new):
    if old == 0: return "  N/A"
    pct = ((new - old) / old) * 100
    if abs(pct) < 2: return f"  ≈0%"
    arrow = "⬇️" if pct < 0 else "⬆️"
    return f"{pct:+.0f}% {arrow}"

# Header
print(f"  {'Query':<28} {la:>14} {lb:>14} {'Delta':>10}")
print(f"  {'─'*28} {'─'*14} {'─'*14} {'─'*10}")

# Query latencies
ql_a = a.get("query_latencies", {})
ql_b = b.get("query_latencies", {})
labels = {
    "A1_read_symbol": "A1 read_symbol",
    "A3_search_code": "A3 search_code",
    "B1_payment_concept": "B1 payment concept",
    "B6_omnichannel_concept": "B6 omnichannel concept",
    "C1_order_flow": "C1 order flow",
    "C5_api_db_flow": "C5 API→DB flow",
}
for key, name in labels.items():
    va = ql_a.get(key, "")
    vb = ql_b.get(key, "")
    ua = parse_latency_us(va)
    ub = parse_latency_us(vb)
    delta = fmt_delta(ua, ub)
    print(f"  {name:<28} {va:>14} {vb:>14} {delta:>10}")

# Graph benchmarks
print(f"\n  {'Graph Benchmark':<28} {la:>14} {lb:>14} {'Delta':>10}")
print(f"  {'─'*28} {'─'*14} {'─'*14} {'─'*10}")
ga = a.get("graph_benchmarks_ns", {})
gb = b.get("graph_benchmarks_ns", {})
for key, name in [("pagerank","PageRank"), ("blast_radius","BlastRadius"), ("betweenness","Betweenness")]:
    va = ga.get(key, 0)
    vb = gb.get(key, 0)
    delta = fmt_delta(va, vb)
    fmt_a = f"{va/1000:.1f}µs" if va < 1_000_000 else f"{va/1_000_000:.1f}ms"
    fmt_b = f"{vb/1000:.1f}µs" if vb < 1_000_000 else f"{vb/1_000_000:.1f}ms"
    print(f"  {name:<28} {fmt_a:>14} {fmt_b:>14} {delta:>10}")

# Test results
print(f"\n  {'Test Suite':<28} {la:>14} {lb:>14}")
print(f"  {'─'*28} {'─'*14} {'─'*14}")
ta = a.get("test_results", {})
tb = b.get("test_results", {})
for key, name in [
    ("benchmark_queries_passed", "Benchmark queries"),
    ("search_quality_passed", "Search quality"),
]:
    va = ta.get(key, 0)
    vb = tb.get(key, 0)
    status_a = f"{va} ✅" if ta.get(key.replace("passed","failed"), 0) == 0 else f"{va} ⚠️"
    status_b = f"{vb} ✅" if tb.get(key.replace("passed","failed"), 0) == 0 else f"{vb} ⚠️"
    print(f"  {name:<28} {status_a:>14} {status_b:>14}")

# Index stats
print(f"\n  {'Index Stats':<28} {la:>14} {lb:>14}")
print(f"  {'─'*28} {'─'*14} {'─'*14}")
ia = a.get("index_stats", {})
ib = b.get("index_stats", {})
for key, name in [("total_nodes","Nodes"), ("total_edges","Edges"), ("indexing_time_s","Index time (s)")]:
    va = ia.get(key, 0)
    vb = ib.get(key, 0)
    print(f"  {name:<28} {str(va):>14} {str(vb):>14}")

print()
PYEOF
}

# ─── Main ───────────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${RESET}"
echo -e "${BOLD}║         context-mcp Benchmark Comparison Tool            ║${RESET}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${RESET}"
echo ""
echo -e "  Commit:    ${BOLD}$COMMIT_SHA${RESET} — $COMMIT_MSG"
if [[ -n "$BASELINE_SHA" ]]; then
    echo -e "  Baseline:  ${BOLD}$BASELINE_SHA${RESET} — $BASELINE_MSG"
fi
echo -e "  Target:    $TARGET_REPO"
echo -e "  Timestamp: $TIMESTAMP"
echo ""

# Run benchmark for the target commit
if [[ "$COMMIT_SHA" == "$(git rev-parse --short HEAD)" ]]; then
    # Current HEAD — no worktree needed
    RESULT_A=$(run_benchmarks "$REPO_ROOT" "$COMMIT" "$COMMIT_SHA")
else
    WORK_DIR=$(create_worktree "$COMMIT" "$COMMIT_SHA")
    RESULT_A=$(run_benchmarks "$WORK_DIR" "$COMMIT" "$COMMIT_SHA")
fi

# Run benchmark for the baseline commit (if specified)
if [[ -n "$BASELINE_SHA" ]]; then
    if [[ "$BASELINE_SHA" == "$(git rev-parse --short HEAD)" ]]; then
        RESULT_B=$(run_benchmarks "$REPO_ROOT" "$BASELINE" "$BASELINE_SHA")
    else
        WORK_DIR_B=$(create_worktree "$BASELINE" "$BASELINE_SHA")
        RESULT_B=$(run_benchmarks "$WORK_DIR_B" "$BASELINE" "$BASELINE_SHA")
    fi

    # Extract the last line (file path) from each result
    FILE_A=$(echo "$RESULT_A" | tail -1)
    FILE_B=$(echo "$RESULT_B" | tail -1)

    print_comparison "$FILE_A" "$FILE_B" "$COMMIT_SHA" "$BASELINE_SHA"
else
    echo ""
    echo -e "${GREEN}━━━ Benchmark complete ━━━${RESET}"
    FILE_A=$(echo "$RESULT_A" | tail -1)
    echo -e "Results: ${BOLD}$FILE_A${RESET}"
    echo ""
    echo -e "${DIM}Tip: Run with --baseline <commit> to compare two versions${RESET}"
fi

echo -e "${BOLD}Done.${RESET}"

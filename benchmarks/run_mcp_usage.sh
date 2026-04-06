#!/usr/bin/env bash
# benchmarks/run_mcp_usage.sh — Run live Claude/Codex MCP usage benchmarks
#
# Usage:
#   ./benchmarks/run_mcp_usage.sh [target-repo-path] [--client all|claude|codex] [--variant all|mcp|nomcp] [--transport all|stdio|http] [--output file] [--raw-dir dir]
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: ./benchmarks/run_mcp_usage.sh [target-repo-path] [options]

Options:
  --client all|claude|codex     Which live client(s) to run (default: all)
  --variant all|mcp|nomcp       Whether to run MCP, no-MCP, or both (default: all)
  --transport all|stdio|http    Which MCP transport(s) to exercise (default: all)
  --output <file>               Output JSON path (default: benchmarks/results/mcp-usage-v<version>-<sha>-<repo>.json)
  --report <file>               Output Markdown report path (default: <output>.md)
  --raw-dir <dir>               Directory for raw client logs and intermediate summaries
  -h, --help                    Show this help

Environment:
  QB_TEST_REPO                  Default target repo if no positional repo path is provided

Examples:
  ./benchmarks/run_mcp_usage.sh /Users/naman/Documents/QBApps/qbapi
  ./benchmarks/run_mcp_usage.sh --variant nomcp
  ./benchmarks/run_mcp_usage.sh --client claude --transport http
  ./benchmarks/run_mcp_usage.sh /Users/naman/Documents/QBApps/qbapi --output benchmarks/results/mcp-usage-custom.json
EOF
}

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RESULTS_DIR="$REPO_ROOT/benchmarks/results"
TARGET_REPO=""
CLIENT="all"
VARIANT="all"
TRANSPORT="all"
OUTPUT_FILE=""
REPORT_FILE=""
RAW_DIR=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --client)
            CLIENT="${2:-}"
            shift 2
            ;;
        --variant)
            VARIANT="${2:-}"
            shift 2
            ;;
        --transport)
            TRANSPORT="${2:-}"
            shift 2
            ;;
        --output)
            OUTPUT_FILE="${2:-}"
            shift 2
            ;;
        --report)
            REPORT_FILE="${2:-}"
            shift 2
            ;;
        --raw-dir)
            RAW_DIR="${2:-}"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        -*)
            echo "Unknown option: $1" >&2
            usage
            exit 1
            ;;
        *)
            if [[ -n "$TARGET_REPO" ]]; then
                echo "Unexpected extra argument: $1" >&2
                usage
                exit 1
            fi
            TARGET_REPO="$1"
            shift
            ;;
    esac
done

case "$CLIENT" in
    all|claude|codex) ;;
    *)
        echo "Invalid --client: $CLIENT" >&2
        exit 1
        ;;
esac

case "$VARIANT" in
    all|mcp|nomcp) ;;
    *)
        echo "Invalid --variant: $VARIANT" >&2
        exit 1
        ;;
esac

case "$TRANSPORT" in
    all|stdio|http) ;;
    *)
        echo "Invalid --transport: $TRANSPORT" >&2
        exit 1
        ;;
esac

TARGET_REPO="${TARGET_REPO:-${QB_TEST_REPO:-/Users/naman/Documents/QBApps/qbapi}}"
if [[ ! -d "$TARGET_REPO" ]]; then
    echo "ERROR: target repo not found at $TARGET_REPO" >&2
    exit 1
fi

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -z "$RAW_DIR" ]]; then
    RAW_DIR="/tmp/context-mcp-mcp-usage-${TIMESTAMP}"
fi
mkdir -p "$RAW_DIR" "$RESULTS_DIR"

BINARY="$RAW_DIR/context-mcp"
export GOCACHE="${GOCACHE:-$RAW_DIR/go-cache}"

VERSION="$(python3 - "$REPO_ROOT/internal/mcp/tools.go" <<'PY'
import re
import sys

text = open(sys.argv[1], encoding="utf-8").read()
match = re.search(r'const Version = "([^"]+)"', text)
if not match:
    raise SystemExit("could not parse Version from internal/mcp/tools.go")
print(match.group(1))
PY
)"
COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD)"
TARGET_LABEL="$(basename "$TARGET_REPO" | tr '[:upper:]' '[:lower:]')"
OUTPUT_FILE="${OUTPUT_FILE:-$RESULTS_DIR/mcp-usage-v${VERSION}-${COMMIT}-${TARGET_LABEL}.json}"
REPORT_FILE="${REPORT_FILE:-${OUTPUT_FILE%.json}.md}"

echo "=== context-mcp MCP Usage Benchmarks ==="
echo "Target repo: $TARGET_REPO"
echo "Client(s):   $CLIENT"
echo "Variant(s):  $VARIANT"
echo "Transport:   $TRANSPORT"
echo "Version:     v$VERSION ($COMMIT)"
echo "Raw dir:     $RAW_DIR"
echo "Output:      $OUTPUT_FILE"
echo "Report:      $REPORT_FILE"
echo ""

echo "--- Building context-mcp ---"
(
    cd "$REPO_ROOT"
    go build -tags "fts5" -o "$BINARY" ./cmd/context-mcp
)
echo "Build: OK"
echo ""

run_claude() {
    command -v claude >/dev/null 2>&1 || {
        echo "ERROR: claude CLI not found in PATH" >&2
        exit 1
    }
    echo "--- Running Claude Code benchmark matrix (model: haiku) ---"
    CONTEXT_MCP_BINARY="$BINARY" CONTEXT_MCP_VARIANT="$VARIANT" \
        "$REPO_ROOT/scripts/smoke-claude-mcp.sh" "$TRANSPORT" "$TARGET_REPO" "$RAW_DIR/claude"
    echo ""
}

run_codex() {
    command -v codex >/dev/null 2>&1 || {
        echo "ERROR: codex CLI not found in PATH" >&2
        exit 1
    }
    echo "--- Running Codex benchmark matrix (model: gpt-5.4-mini) ---"
    CONTEXT_MCP_BINARY="$BINARY" CONTEXT_MCP_VARIANT="$VARIANT" \
        "$REPO_ROOT/scripts/smoke-codex-mcp.sh" "$TRANSPORT" "$TARGET_REPO" "$RAW_DIR/codex"
    echo ""
}

case "$CLIENT" in
    all)
        run_claude
        run_codex
        ;;
    claude)
        run_claude
        ;;
    codex)
        run_codex
        ;;
esac

python3 - "$OUTPUT_FILE" "$REPO_ROOT/benchmarks/mcp_smoke_tasks.json" "$RAW_DIR/claude/summary.json" "$RAW_DIR/codex/summary.json" "$VERSION" "$COMMIT" "$TARGET_REPO" "$TRANSPORT" "$CLIENT" "$VARIANT" <<'PY'
import json
import os
import platform
import sys
from datetime import datetime, timezone

(
    output_file,
    tasks_file,
    claude_file,
    codex_file,
    version,
    commit,
    target_repo,
    transport,
    client,
    variant_mode,
) = sys.argv[1:11]

with open(tasks_file, encoding="utf-8") as f:
    tasks = json.load(f)

def load_summary(path):
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return json.load(f)

def summarize_client(summary):
    runs = summary.get("runs", [])
    clean_runs = []
    mode_summary = {}

    for run in runs:
        clean = {
            "id": run["id"],
            "mode": run.get("mode", "mcp"),
            "transport": run["transport"],
            "status": run["status"],
            "expected_tools": run.get("expected_tools", []),
            "tool_calls": run.get("tool_calls", []),
            "mcp_tool_calls": run.get("mcp_tool_calls", []),
            "expected_tools_called": run.get("expected_tools_called", False),
            "mcp_disabled_respected": run.get("mcp_disabled_respected", False),
            "answer_present": run.get("answer_present", False),
            "est_tokens": run.get("est_tokens", 0),
            "cost_usd": run.get("cost_usd", 0),
            "output_bytes": run.get("output_bytes", 0),
            "output_lines": run.get("output_lines", 0),
            "elapsed_ms": run.get("elapsed_ms", 0),
            "usage": run.get("usage", {}),
        }
        clean_runs.append(clean)

        mode_bucket = mode_summary.setdefault(clean["mode"], {
            "runs": 0,
            "passed": 0,
            "expected_tool_coverage": 0,
            "mcp_disabled_respected_runs": 0,
            "est_tokens": 0,
            "cost_usd": 0,
            "output_bytes": 0,
            "output_lines": 0,
            "transport_summary": {},
        })
        mode_bucket["runs"] += 1
        mode_bucket["passed"] += 1 if clean["status"] == "PASS" else 0
        mode_bucket["expected_tool_coverage"] += 1 if clean["expected_tools_called"] else 0
        mode_bucket["mcp_disabled_respected_runs"] += 1 if clean["mcp_disabled_respected"] else 0
        mode_bucket["est_tokens"] += clean["est_tokens"]
        mode_bucket["cost_usd"] += clean["cost_usd"]
        mode_bucket["output_bytes"] += clean["output_bytes"]
        mode_bucket["output_lines"] += clean["output_lines"]

        transport_bucket = mode_bucket["transport_summary"].setdefault(clean["transport"], {
            "runs": 0,
            "passed": 0,
            "expected_tool_coverage": 0,
            "mcp_disabled_respected_runs": 0,
            "est_tokens": 0,
            "cost_usd": 0,
            "output_bytes": 0,
            "output_lines": 0,
        })
        transport_bucket["runs"] += 1
        transport_bucket["passed"] += 1 if clean["status"] == "PASS" else 0
        transport_bucket["expected_tool_coverage"] += 1 if clean["expected_tools_called"] else 0
        transport_bucket["mcp_disabled_respected_runs"] += 1 if clean["mcp_disabled_respected"] else 0
        transport_bucket["est_tokens"] += clean["est_tokens"]
        transport_bucket["cost_usd"] += clean["cost_usd"]
        transport_bucket["output_bytes"] += clean["output_bytes"]
        transport_bucket["output_lines"] += clean["output_lines"]

    for mode_bucket in mode_summary.values():
        mode_runs = max(mode_bucket["runs"], 1)
        mode_bucket["avg_output_bytes"] = round(mode_bucket["output_bytes"] / mode_runs, 2)
        mode_bucket["avg_output_lines"] = round(mode_bucket["output_lines"] / mode_runs, 2)
        mode_bucket["avg_est_tokens"] = round(mode_bucket["est_tokens"] / mode_runs, 2)
        mode_bucket["avg_cost_usd"] = round(mode_bucket["cost_usd"] / mode_runs, 6)
        for transport_bucket in mode_bucket["transport_summary"].values():
            t_runs = max(transport_bucket["runs"], 1)
            transport_bucket["avg_output_bytes"] = round(transport_bucket["output_bytes"] / t_runs, 2)
            transport_bucket["avg_output_lines"] = round(transport_bucket["output_lines"] / t_runs, 2)
            transport_bucket["avg_est_tokens"] = round(transport_bucket["est_tokens"] / t_runs, 2)
            transport_bucket["avg_cost_usd"] = round(transport_bucket["cost_usd"] / t_runs, 6)

    return {
        "client": summary.get("client"),
        "model": summary.get("model"),
        "all_passed": summary.get("all_passed", False),
        "total_runs": len(clean_runs),
        "passed_runs": sum(1 for run in clean_runs if run["status"] == "PASS"),
        "expected_tool_coverage_runs": sum(1 for run in clean_runs if run["expected_tools_called"]),
        "total_est_tokens": sum(run["est_tokens"] for run in clean_runs),
        "total_cost_usd": round(sum(run["cost_usd"] for run in clean_runs), 6),
        "total_output_bytes": sum(run["output_bytes"] for run in clean_runs),
        "total_output_lines": sum(run["output_lines"] for run in clean_runs),
        "mode_summary": mode_summary,
        "runs": clean_runs,
    }

claude_summary = load_summary(claude_file)
codex_summary = load_summary(codex_file)

results = {}
if claude_summary:
    results["claude_code"] = summarize_client(claude_summary)
if codex_summary:
    results["codex"] = summarize_client(codex_summary)

all_runs = []
for result in results.values():
    all_runs.extend(result["runs"])

artifact = {
    "benchmark_version": "1.0.0",
    "kind": "mcp_usage",
    "context_mcp_version": f"v{version}",
    "context_mcp_commit": commit,
    "run_date": datetime.now(timezone.utc).isoformat(),
    "environment": {
        "os": f"{platform.system().lower()}/{platform.machine().lower()}",
        "target_repo": target_repo,
        "clients": sorted(results.keys()),
        "transport": transport,
        "variant": variant_mode,
        "task_count": len(tasks),
        "task_ids": [task["id"] for task in tasks],
        "scenario_file": "benchmarks/mcp_smoke_tasks.json",
    },
    "models": {
        "claude_code": "haiku",
        "codex": "gpt-5.4-mini",
    },
    "summary": {
        "client_mode": client,
        "variant_mode": variant_mode,
        "total_runs": len(all_runs),
        "passed_runs": sum(1 for run in all_runs if run["status"] == "PASS"),
        "expected_tool_coverage_runs": sum(1 for run in all_runs if run["expected_tools_called"]),
        "total_est_tokens": sum(run["est_tokens"] for run in all_runs),
        "total_cost_usd": round(sum(run["cost_usd"] for run in all_runs), 6),
        "total_output_bytes": sum(run["output_bytes"] for run in all_runs),
        "total_output_lines": sum(run["output_lines"] for run in all_runs),
        "all_passed": all(run["status"] == "PASS" for run in all_runs) if all_runs else False,
    },
    "results": results,
    "notes": [
        "This artifact aggregates the live Claude Code and Codex benchmark matrix.",
        "By default it includes both MCP and no-MCP runs.",
        "Raw client logs remain in the raw-dir used during the benchmark run and are not embedded here.",
        "Token and cost fields depend on what each client exposes in structured output.",
    ],
}

with open(output_file, "w", encoding="utf-8") as f:
    json.dump(artifact, f, indent=2)
    f.write("\n")
PY

python3 - "$OUTPUT_FILE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    data = json.load(f)

print("=== MCP Usage Benchmark Summary ===")
for name, result in data["results"].items():
    print(
        f"{name}: {result['passed_runs']}/{result['total_runs']} passed | "
        f"tokens={result['total_est_tokens']} | cost=${result['total_cost_usd']:.6f}"
    )
    for mode, bucket in sorted(result.get("mode_summary", {}).items()):
        print(
            f"  {mode}: {bucket['passed']}/{bucket['runs']} passed | "
            f"tokens={bucket['est_tokens']} | cost=${bucket['cost_usd']:.6f}"
        )
print(
    f"overall: {data['summary']['passed_runs']}/{data['summary']['total_runs']} passed | "
    f"tokens={data['summary']['total_est_tokens']} | cost=${data['summary']['total_cost_usd']:.6f}"
)
PY

python3 "$REPO_ROOT/benchmarks/render_mcp_usage_report.py" "$OUTPUT_FILE" --output "$REPORT_FILE"

echo ""
echo "Saved publishable artifact: $OUTPUT_FILE"
echo "Saved Markdown report: $REPORT_FILE"

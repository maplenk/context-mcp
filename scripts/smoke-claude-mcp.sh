#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: smoke-claude-mcp.sh [stdio|http|all] <repo-root> [output-dir]

Builds a temporary context-mcp binary, runs Claude Code benchmark scenarios,
and writes raw logs plus summary.json.

Environment:
  CONTEXT_MCP_BINARY     Use an existing binary instead of building one
  CLAUDE_OUTPUT_FORMAT   stream-json (default) or json
  CONTEXT_MCP_VARIANT    mcp (default), nomcp, or all
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

TRANSPORT="${1:-all}"
TARGET_REPO="${2:-}"
OUT_DIR="${3:-}"

if [[ -z "$TARGET_REPO" ]]; then
  usage
  exit 1
fi

case "$TRANSPORT" in
  stdio|http|all) ;;
  *)
    echo "invalid transport: $TRANSPORT" >&2
    usage
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TASK_FILE="$PROJECT_ROOT/benchmarks/mcp_smoke_tasks.json"
CLAUDE_OUTPUT_FORMAT="${CLAUDE_OUTPUT_FORMAT:-stream-json}"
VARIANT="${CONTEXT_MCP_VARIANT:-mcp}"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"

case "$VARIANT" in
  mcp|nomcp|all) ;;
  *)
    echo "invalid CONTEXT_MCP_VARIANT: $VARIANT" >&2
    exit 1
    ;;
esac

if [[ -z "$OUT_DIR" ]]; then
  OUT_DIR="/tmp/context-mcp-smoke-claude-${TIMESTAMP}"
fi
mkdir -p "$OUT_DIR"

if [[ -n "${CONTEXT_MCP_BINARY:-}" ]]; then
  BINARY="$CONTEXT_MCP_BINARY"
else
  BINARY="$OUT_DIR/context-mcp"
  (
    cd "$PROJECT_ROOT"
    go build -tags "fts5" -o "$BINARY" ./cmd/qb-context
  )
fi

pick_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

prime_index() {
  local db_path="$1"
  local log_prefix="$2"
  "$BINARY" -repo "$TARGET_REPO" -db "$db_path" cli get_architecture_summary '{}' \
    >"${log_prefix}.stdout.json" 2>"${log_prefix}.stderr.log"
}

start_http_server() {
  local port="$1"
  local log_file="$2"
  local db_path="$3"
  "$BINARY" -repo "$TARGET_REPO" -db "$db_path" -profile full serve-http -port "$port" >"$log_file" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 80); do
    if curl -s -o /dev/null "http://127.0.0.1:${port}/"; then
      return 0
    fi
    sleep 0.25
  done
  echo "failed to start HTTP server on port $port" >&2
  return 1
}

emit_tasks() {
  local variant="$1"
  local checkpoint="$2"
  python3 - "$TASK_FILE" "$TARGET_REPO" "$checkpoint" "$variant" <<'PY'
import base64
import json
import sys

task_file, repo_root, checkpoint, variant = sys.argv[1:5]
with open(task_file, encoding="utf-8") as f:
    tasks = json.load(f)

for task in tasks:
    prompt_key = "prompt_mcp" if variant == "mcp" else "prompt_nomcp"
    prompt = (
        task[prompt_key]
        .replace("%REPO_ROOT%", repo_root)
        .replace("%CHECKPOINT_NAME%", checkpoint)
    )
    encoded = base64.b64encode(prompt.encode("utf-8")).decode("ascii")
    print(f'{task["id"]}\t{",".join(task["expected_tools"])}\t{encoded}')
PY
}

parse_claude_result() {
  local raw_file="$1"
  local debug_file="$2"
  local expected_csv="$3"
  local answer_file="$4"
  local variant="$5"
  python3 - "$raw_file" "$debug_file" "$expected_csv" "$answer_file" "$variant" <<'PY'
import json
import os
import re
import sys

raw_file, debug_file, expected_csv, answer_file, variant = sys.argv[1:6]
expected = [item for item in expected_csv.split(",") if item]
tool_calls = []
mcp_tool_calls = []
seen = set()
mcp_seen = set()
usage = {
    "input_tokens": 0,
    "output_tokens": 0,
    "cache_read_input_tokens": 0,
    "cache_creation_input_tokens": 0,
    "total_cost_usd": 0,
}
answer_candidates = []

def add_tool(name):
    if name and name not in seen:
        seen.add(name)
        tool_calls.append(name)

def add_mcp_tool(name):
    if name and name not in mcp_seen:
        mcp_seen.add(name)
        mcp_tool_calls.append(name)

tool_pattern = re.compile(r"mcp__context-mcp__([a-z_]+)")

def walk(obj):
    if isinstance(obj, dict):
        for key, value in obj.items():
            if key in usage and isinstance(value, (int, float)):
                usage[key] = max(usage[key], value)
            if key in {"name", "tool_name", "toolName"} and isinstance(value, str):
                add_tool(value)
                match = tool_pattern.search(value)
                if match:
                    add_mcp_tool(match.group(1))
            if key in {"result", "text", "content"} and isinstance(value, str):
                answer_candidates.append(value)
            walk(value)
    elif isinstance(obj, list):
        for item in obj:
            walk(item)
    elif isinstance(obj, str):
        match = tool_pattern.search(obj)
        if match:
            add_mcp_tool(match.group(1))

raw_text = ""
if os.path.exists(raw_file):
    with open(raw_file, "r", encoding="utf-8", errors="replace") as f:
        raw_text = f.read()
    parsed = False
    if raw_text.strip().startswith("{"):
        try:
            walk(json.loads(raw_text))
            parsed = True
        except Exception:
            parsed = False
    if not parsed:
        for line in raw_text.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                walk(json.loads(line))
            except Exception:
                continue

if os.path.exists(debug_file):
    with open(debug_file, "r", encoding="utf-8", errors="replace") as f:
        debug_text = f.read()
    for match in tool_pattern.finditer(debug_text):
        add_mcp_tool(match.group(1))
    for match in re.finditer(r"Calling MCP tool: ([a-z_]+)", debug_text):
        add_mcp_tool(match.group(1))

answer_text = ""
for candidate in sorted(answer_candidates, key=len, reverse=True):
    stripped = candidate.strip()
    if stripped:
        answer_text = stripped
        break

if answer_text:
    with open(answer_file, "w", encoding="utf-8") as f:
        f.write(answer_text)

answer_present = bool(answer_text.strip()) or bool(raw_text.strip())
if variant == "mcp":
    expected_called = all(tool in mcp_seen for tool in expected)
    mcp_disabled_respected = False
    status = "PASS" if expected_called else "FAIL"
else:
    expected_called = False
    mcp_disabled_respected = len(mcp_tool_calls) == 0
    status = "PASS" if mcp_disabled_respected and answer_present else "FAIL"

output_bytes = len(answer_text.encode("utf-8")) if answer_text else len(raw_text.encode("utf-8"))
output_lines = answer_text.count("\n") + 1 if answer_text else len(raw_text.splitlines())

summary = {
    "expected_tools": expected,
    "tool_calls": tool_calls,
    "mcp_tool_calls": mcp_tool_calls,
    "expected_tools_called": expected_called,
    "mcp_disabled_respected": mcp_disabled_respected,
    "answer_present": answer_present,
    "usage": usage,
    "est_tokens": int(
        usage["input_tokens"]
        + usage["cache_read_input_tokens"]
        + usage["cache_creation_input_tokens"]
        + usage["output_tokens"]
    ),
    "cost_usd": usage["total_cost_usd"],
    "output_bytes": output_bytes,
    "output_lines": output_lines,
    "answer_file": answer_file if answer_text else "",
    "status": status,
}
print(json.dumps(summary))
PY
}

append_summary() {
  local variant="$1"
  local transport="$2"
  local task_id="$3"
  local elapsed_ms="$4"
  local raw_file="$5"
  local debug_file="$6"
  local result_json="$7"
  python3 - "$variant" "$transport" "$task_id" "$elapsed_ms" "$raw_file" "$debug_file" "$result_json" <<'PY'
import json
import sys

variant, transport, task_id, elapsed_ms, raw_file, debug_file, result_json = sys.argv[1:8]
result = json.loads(result_json)
entry = {
    "id": task_id,
    "mode": variant,
    "transport": transport,
    "elapsed_ms": float(elapsed_ms),
    "expected_tools": result["expected_tools"],
    "tool_calls": result["tool_calls"],
    "mcp_tool_calls": result["mcp_tool_calls"],
    "expected_tools_called": result["expected_tools_called"],
    "mcp_disabled_respected": result["mcp_disabled_respected"],
    "answer_present": result["answer_present"],
    "est_tokens": result["est_tokens"],
    "cost_usd": result["cost_usd"],
    "output_bytes": result["output_bytes"],
    "output_lines": result["output_lines"],
    "raw_log": raw_file,
    "debug_log": debug_file,
    "answer_file": result["answer_file"],
    "usage": result["usage"],
    "status": result["status"],
}
print(json.dumps(entry))
PY
}

write_final_summary() {
  local summary_meta="$1"
  local summary_file="$2"
  python3 - "$summary_meta" "$summary_file" "$TARGET_REPO" "$BINARY" "$CLAUDE_OUTPUT_FORMAT" "$VARIANT" <<'PY'
import json
import os
import sys
from datetime import datetime, timezone

summary_meta, summary_file, repo_root, binary, output_format, variant = sys.argv[1:7]
runs = []
if os.path.exists(summary_meta):
    with open(summary_meta, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            runs.append(json.loads(line))

summary = {
    "client": "claude-code",
    "model": "haiku",
    "output_format": output_format,
    "variant": variant,
    "repo_root": repo_root,
    "binary": binary,
    "generated_at": datetime.now(timezone.utc).isoformat(),
    "runs": runs,
    "all_passed": all(run["status"] == "PASS" for run in runs),
}
with open(summary_file, "w", encoding="utf-8") as f:
    json.dump(summary, f, indent=2)
    f.write("\n")
PY
}

run_case() {
  local variant="$1"
  local transport="$2"
  local summary_meta="$OUT_DIR/.summary_meta.jsonl"
  local case_dir="$OUT_DIR/$variant/$transport"
  local config_file="$case_dir/mcp-config.json"
  local db_path="$case_dir/context-mcp.db"
  local prime_prefix="$case_dir/prime"
  local server_log="$case_dir/http-server.log"
  mkdir -p "$case_dir"

  local checkpoint="smoke-${variant}-${transport}-${TIMESTAMP}"

  if [[ "$variant" == "mcp" ]]; then
    prime_index "$db_path" "$prime_prefix"
  fi

  if [[ "$variant" == "mcp" && "$transport" == "http" ]]; then
    local port
    port="$(pick_port)"
    start_http_server "$port" "$server_log" "$db_path"
    cat >"$config_file" <<EOF
{
  "mcpServers": {
    "context-mcp": {
      "type": "http",
      "url": "http://127.0.0.1:${port}/mcp"
    }
  }
}
EOF
  elif [[ "$variant" == "mcp" ]]; then
    cat >"$config_file" <<EOF
{
  "mcpServers": {
    "context-mcp": {
      "command": "$BINARY",
      "args": ["-repo", "$TARGET_REPO", "-db", "$db_path", "-profile", "full"]
    }
  }
}
EOF
  fi

  while IFS=$'\t' read -r task_id expected_csv prompt_b64; do
    local prompt
    prompt="$(python3 -c 'import base64, sys; print(base64.b64decode(sys.argv[1]).decode("utf-8"))' "$prompt_b64")"
    local raw_file="$case_dir/${task_id}.raw"
    local debug_file="$case_dir/${task_id}.debug.log"
    local stderr_file="$case_dir/${task_id}.stderr.log"
    local answer_file="$case_dir/${task_id}.answer.txt"

    local start_ns end_ns elapsed_ms
    start_ns="$(python3 -c 'import time; print(time.time_ns())')"
    if [[ "$variant" == "mcp" ]]; then
      (
        cd "$TARGET_REPO" && claude -p "$prompt" \
          --model haiku \
          --effort low \
          --max-turns 12 \
          --output-format "$CLAUDE_OUTPUT_FORMAT" \
          --verbose \
          --no-session-persistence \
          --tools "" \
          --allowedTools "mcp__context-mcp__context,mcp__context-mcp__assemble_context,mcp__context-mcp__read_symbol,mcp__context-mcp__checkpoint_context,mcp__context-mcp__read_delta" \
          --permission-mode bypassPermissions \
          --append-system-prompt "You are running a context-mcp benchmark with MCP enabled. Use the requested MCP tools directly and keep the final answer brief." \
          --mcp-config "$config_file" \
          --strict-mcp-config \
          --debug-file "$debug_file" \
          >"$raw_file" 2>"$stderr_file"
      ) || true
    else
      (
        cd "$TARGET_REPO" && claude -p "$prompt" \
          --model haiku \
          --effort low \
          --max-turns 12 \
          --output-format "$CLAUDE_OUTPUT_FORMAT" \
          --verbose \
          --no-session-persistence \
          --permission-mode bypassPermissions \
          --add-dir "$TARGET_REPO" \
          --append-system-prompt "You are running a repository-understanding benchmark without MCP tools. Use the normal local workspace tools available to you and keep the final answer brief." \
          --debug-file "$debug_file" \
          >"$raw_file" 2>"$stderr_file"
      ) || true
    fi
    end_ns="$(python3 -c 'import time; print(time.time_ns())')"
    elapsed_ms="$(python3 -c "print(($end_ns - $start_ns) / 1_000_000)")"

    local result_json
    result_json="$(parse_claude_result "$raw_file" "$debug_file" "$expected_csv" "$answer_file" "$variant")"
    append_summary "$variant" "$transport" "$task_id" "$elapsed_ms" "$raw_file" "$debug_file" "$result_json" >>"$summary_meta"

    python3 - "$task_id" "$variant" "$transport" "$result_json" <<'PY'
import json
import sys

task_id, variant, transport, result_json = sys.argv[1:5]
result = json.loads(result_json)
print(f"[{variant}/{transport}] {task_id}: {result['status']} | mcp_tools={','.join(result['mcp_tool_calls']) or '-'} | tokens={result['est_tokens']} | bytes={result['output_bytes']}")
PY
  done < <(emit_tasks "$variant" "$checkpoint")

  if [[ "$variant" == "mcp" && "$transport" == "http" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
  fi
}

run_variant() {
  local variant="$1"
  if [[ "$variant" == "nomcp" ]]; then
    run_case nomcp none
    return
  fi
  case "$TRANSPORT" in
    all)
      run_case mcp stdio
      run_case mcp http
      ;;
    *)
      run_case mcp "$TRANSPORT"
      ;;
  esac
}

SUMMARY_META="$OUT_DIR/.summary_meta.jsonl"
SUMMARY_FILE="$OUT_DIR/summary.json"
rm -f "$SUMMARY_META"

case "$VARIANT" in
  all)
    run_variant mcp
    run_variant nomcp
    ;;
  *)
    run_variant "$VARIANT"
    ;;
esac

write_final_summary "$SUMMARY_META" "$SUMMARY_FILE"
rm -f "$SUMMARY_META"

echo ""
echo "Claude benchmark runs complete"
echo "Output directory: $OUT_DIR"
echo "Summary: $SUMMARY_FILE"

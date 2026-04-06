#!/usr/bin/env python3
"""Render a publishable Markdown report for an MCP usage artifact."""

from __future__ import annotations

import json
import sys
from pathlib import Path


def fmt_int(value: int | float) -> str:
    return f"{int(value):,}"


def fmt_float(value: float, digits: int = 2) -> str:
    return f"{value:.{digits}f}"


def fmt_cost(value: float) -> str:
    return f"${value:.6f}"


def pass_rate(passed: int, runs: int) -> str:
    if runs == 0:
        return "0%"
    return f"{(passed / runs) * 100:.1f}%"


def print_table(headers: list[str], rows: list[list[str]]) -> None:
    print("| " + " | ".join(headers) + " |")
    print("| " + " | ".join(["---"] * len(headers)) + " |")
    for row in rows:
        print("| " + " | ".join(row) + " |")
    print()


def sorted_transport_items(bucket: dict) -> list[tuple[str, dict]]:
    order = {"stdio": 0, "http": 1, "none": 2}
    items = list(bucket.items())
    items.sort(key=lambda item: (order.get(item[0], 99), item[0]))
    return items


def render_summary(data: dict) -> None:
    env = data["environment"]
    summary = data["summary"]

    print("# MCP Usage Benchmark Report")
    print()
    print(f"- qb-context: `{data['qb_context_version']}`")
    print(f"- commit: `{data['qb_context_commit']}`")
    print(f"- run date: `{data['run_date']}`")
    print(f"- target repo: `{env['target_repo']}`")
    print(f"- clients: `{', '.join(env['clients'])}`")
    print(f"- transport selection: `{env['transport']}`")
    print(f"- variant selection: `{env['variant']}`")
    print(f"- scenarios: `{', '.join(env['task_ids'])}`")
    print()

    print("## Overall")
    print()
    overall_rows = [[
        "overall",
        str(summary["total_runs"]),
        str(summary["passed_runs"]),
        pass_rate(summary["passed_runs"], summary["total_runs"]),
        fmt_int(summary["expected_tool_coverage_runs"]),
        fmt_int(summary["total_est_tokens"]),
        fmt_cost(summary["total_cost_usd"]),
        fmt_int(summary["total_output_bytes"]),
        fmt_int(summary["total_output_lines"]),
    ]]
    for client_key, client in sorted(data["results"].items()):
        overall_rows.append([
            client_key,
            str(client["total_runs"]),
            str(client["passed_runs"]),
            pass_rate(client["passed_runs"], client["total_runs"]),
            fmt_int(client["expected_tool_coverage_runs"]),
            fmt_int(client["total_est_tokens"]),
            fmt_cost(client["total_cost_usd"]),
            fmt_int(client["total_output_bytes"]),
            fmt_int(client["total_output_lines"]),
        ])
    print_table(
        [
            "Scope",
            "Runs",
            "Passed",
            "Pass %",
            "Expected Tool Coverage",
            "Total Tokens",
            "Total Cost",
            "Output Bytes",
            "Output Lines",
        ],
        overall_rows,
    )


def render_headline_comparison(data: dict) -> None:
    print("## Claude vs Codex, MCP vs No-MCP")
    print()
    rows: list[list[str]] = []
    for client_key, client in sorted(data["results"].items()):
        mode_summary = client.get("mode_summary", {})
        mcp = mode_summary.get("mcp")
        nomcp = mode_summary.get("nomcp")
        if not mcp or not nomcp:
            continue
        delta_tokens = mcp["avg_est_tokens"] - nomcp["avg_est_tokens"]
        delta_pct = 0.0 if nomcp["avg_est_tokens"] == 0 else (delta_tokens / nomcp["avg_est_tokens"]) * 100
        rows.append([
            client_key,
            f"{mcp['passed']}/{mcp['runs']}",
            pass_rate(mcp["passed"], mcp["runs"]),
            f"{nomcp['passed']}/{nomcp['runs']}",
            pass_rate(nomcp["passed"], nomcp["runs"]),
            fmt_float(mcp["avg_est_tokens"]),
            fmt_float(nomcp["avg_est_tokens"]),
            fmt_float(delta_tokens),
            f"{delta_pct:.1f}%",
            fmt_cost(mcp["avg_cost_usd"]),
            fmt_cost(nomcp["avg_cost_usd"]),
        ])
    if rows:
        print_table(
            [
                "Client",
                "MCP Passed",
                "MCP Pass %",
                "No-MCP Passed",
                "No-MCP Pass %",
                "MCP Avg Tokens",
                "No-MCP Avg Tokens",
                "Delta Tokens",
                "Delta %",
                "MCP Avg Cost",
                "No-MCP Avg Cost",
            ],
            rows,
        )


def render_mode_comparison(data: dict) -> None:
    print("## Client / Mode Comparison")
    print()
    rows: list[list[str]] = []
    for client_key, client in sorted(data["results"].items()):
        for mode, bucket in sorted(client.get("mode_summary", {}).items()):
            rows.append([
                client_key,
                mode,
                "all",
                str(bucket["runs"]),
                str(bucket["passed"]),
                pass_rate(bucket["passed"], bucket["runs"]),
                fmt_int(bucket["expected_tool_coverage"]),
                fmt_int(bucket["est_tokens"]),
                fmt_float(bucket["avg_est_tokens"]),
                fmt_cost(bucket["cost_usd"]),
                fmt_int(bucket["output_bytes"]),
                fmt_float(bucket["avg_output_bytes"]),
                fmt_float(bucket["avg_output_lines"]),
            ])
            for transport, transport_bucket in sorted_transport_items(bucket.get("transport_summary", {})):
                rows.append([
                    client_key,
                    mode,
                    transport,
                    str(transport_bucket["runs"]),
                    str(transport_bucket["passed"]),
                    pass_rate(transport_bucket["passed"], transport_bucket["runs"]),
                    fmt_int(transport_bucket["expected_tool_coverage"]),
                    fmt_int(transport_bucket["est_tokens"]),
                    fmt_float(transport_bucket["avg_est_tokens"]),
                    fmt_cost(transport_bucket["cost_usd"]),
                    fmt_int(transport_bucket["output_bytes"]),
                    fmt_float(transport_bucket["avg_output_bytes"]),
                    fmt_float(transport_bucket["avg_output_lines"]),
                ])
    print_table(
        [
            "Client",
            "Mode",
            "Transport",
            "Runs",
            "Passed",
            "Pass %",
            "Coverage",
            "Total Tokens",
            "Avg Tokens / Run",
            "Total Cost",
            "Total Bytes",
            "Avg Bytes / Run",
            "Avg Lines / Run",
        ],
        rows,
    )


def render_delta_comparison(data: dict) -> None:
    print("## MCP vs No-MCP")
    print()
    rows: list[list[str]] = []
    for client_key, client in sorted(data["results"].items()):
        mode_summary = client.get("mode_summary", {})
        mcp = mode_summary.get("mcp")
        nomcp = mode_summary.get("nomcp")
        if not mcp or not nomcp:
            continue
        delta_tokens = mcp["avg_est_tokens"] - nomcp["avg_est_tokens"]
        delta_pct = 0.0 if nomcp["avg_est_tokens"] == 0 else (delta_tokens / nomcp["avg_est_tokens"]) * 100
        delta_bytes = mcp["avg_output_bytes"] - nomcp["avg_output_bytes"]
        rows.append([
            client_key,
            fmt_float(mcp["avg_est_tokens"]),
            fmt_float(nomcp["avg_est_tokens"]),
            fmt_float(delta_tokens),
            f"{delta_pct:.1f}%",
            fmt_cost(mcp["avg_cost_usd"]),
            fmt_cost(nomcp["avg_cost_usd"]),
            fmt_float(mcp["avg_output_bytes"]),
            fmt_float(nomcp["avg_output_bytes"]),
            fmt_float(delta_bytes),
        ])
    if rows:
        print_table(
            [
                "Client",
                "MCP Avg Tokens",
                "No-MCP Avg Tokens",
                "Delta Tokens",
                "Delta %",
                "MCP Avg Cost",
                "No-MCP Avg Cost",
                "MCP Avg Bytes",
                "No-MCP Avg Bytes",
                "Delta Bytes",
            ],
            rows,
        )
    else:
        print("No client had both MCP and no-MCP runs in this artifact.\n")


def render_scenario_matrix(data: dict) -> None:
    print("## Scenario Matrix")
    print()
    rows: list[list[str]] = []
    for client_key, client in sorted(data["results"].items()):
        for run in sorted(client.get("runs", []), key=lambda item: (item["mode"], item["transport"], item["id"])):
            rows.append([
                client_key,
                run["mode"],
                run["transport"],
                run["id"],
                run["status"],
                ", ".join(run.get("expected_tools", [])),
                "yes" if run.get("expected_tools_called") else "no",
                "yes" if run.get("mcp_disabled_respected") else "no",
                fmt_int(run.get("est_tokens", 0)),
                fmt_cost(run.get("cost_usd", 0)),
                fmt_int(run.get("output_bytes", 0)),
                fmt_int(run.get("output_lines", 0)),
            ])
    print_table(
        [
            "Client",
            "Mode",
            "Transport",
            "Scenario",
            "Status",
            "Expected Tools",
            "Coverage",
            "No-MCP Clean",
            "Tokens",
            "Cost",
            "Bytes",
            "Lines",
        ],
        rows,
    )


def main() -> int:
    if len(sys.argv) not in {2, 4}:
        print("Usage: render_mcp_usage_report.py <artifact.json> [--output <report.md>]", file=sys.stderr)
        return 1

    artifact_path = Path(sys.argv[1])
    output_path: Path | None = None
    if len(sys.argv) == 4:
        if sys.argv[2] != "--output":
            print("Usage: render_mcp_usage_report.py <artifact.json> [--output <report.md>]", file=sys.stderr)
            return 1
        output_path = Path(sys.argv[3])

    data = json.loads(artifact_path.read_text(encoding="utf-8"))

    if output_path is None:
        render_summary(data)
        render_headline_comparison(data)
        render_mode_comparison(data)
        render_delta_comparison(data)
        render_scenario_matrix(data)
        return 0

    from contextlib import redirect_stdout

    output_path.parent.mkdir(parents=True, exist_ok=True)
    with output_path.open("w", encoding="utf-8") as handle, redirect_stdout(handle):
        render_summary(data)
        render_headline_comparison(data)
        render_mode_comparison(data)
        render_delta_comparison(data)
        render_scenario_matrix(data)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

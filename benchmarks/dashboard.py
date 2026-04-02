#!/usr/bin/env python3
"""
benchmarks/dashboard.py — Compare qb-context benchmark results side-by-side.

Usage:
  python3 benchmarks/dashboard.py                           # compare all results in results/
  python3 benchmarks/dashboard.py results/a.json results/b.json   # compare specific files
  python3 benchmarks/dashboard.py --latest 3                # compare 3 most recent results
  python3 benchmarks/dashboard.py --html > report.html      # export HTML report
"""

import json
import sys
import os
import glob
import argparse
from pathlib import Path
from datetime import datetime

# ─── ANSI Colors ────────────────────────────────────────────────────────────────
class C:
    BOLD   = "\033[1m"
    DIM    = "\033[2m"
    RED    = "\033[31m"
    GREEN  = "\033[32m"
    YELLOW = "\033[33m"
    CYAN   = "\033[36m"
    WHITE  = "\033[37m"
    RESET  = "\033[0m"
    UNDERLINE = "\033[4m"

    @staticmethod
    def disable():
        for attr in ["BOLD","DIM","RED","GREEN","YELLOW","CYAN","WHITE","RESET","UNDERLINE"]:
            setattr(C, attr, "")


# ─── Data Loading ───────────────────────────────────────────────────────────────
def load_result(path: str) -> dict:
    """Load a benchmark result JSON, normalizing both old and new formats."""
    with open(path) as f:
        data = json.load(f)

    # Normalize: old baseline format vs new compare.sh format
    result = {
        "file": os.path.basename(path),
        "commit": data.get("qb_context_commit", data.get("commit", "unknown")),
        "version": data.get("qb_context_version", data.get("label", "")),
        "date": data.get("run_date", data.get("timestamp", "")),
        "nodes": 0,
        "edges": 0,
        "index_time_s": 0,
        "queries": {},
        "scores": {},
        "graph": {},
        "tests": {},
    }

    # Index stats
    env = data.get("environment", data.get("index_stats", {}))
    idx = env.get("index_stats", env)
    result["nodes"] = idx.get("total_nodes", 0)
    result["edges"] = idx.get("total_edges", 0)
    result["index_time_s"] = idx.get("indexing_time_seconds", idx.get("indexing_time_s", 0))

    # Query latencies — old format (array of results)
    if "results" in data:
        for r in data["results"]:
            qid = r["id"]
            elapsed_us = r.get("elapsed_us", 0)
            elapsed_human = r.get("elapsed_human", f"{elapsed_us}µs")
            top_score = 0
            rs = r.get("result_summary", {})
            top_score = rs.get("top_score", 0)
            result_count = rs.get("result_count", rs.get("match_count", 1))
            score_dist = rs.get("score_distribution", [])
            result["queries"][qid] = {
                "us": elapsed_us,
                "human": elapsed_human,
                "tool": r.get("tool", ""),
                "top_score": top_score,
                "result_count": result_count,
            }
            if score_dist:
                result["scores"][qid] = score_dist

    # Query latencies — new format (compare.sh output)
    if "query_latencies" in data:
        label_map = {
            "A1_read_symbol": "A1",
            "A3_search_code": "A3",
            "B1_payment_concept": "B1",
            "B6_omnichannel_concept": "B6",
            "C1_order_flow": "C1",
            "C5_api_db_flow": "C5",
        }
        for key, qid in label_map.items():
            raw = data["query_latencies"].get(key, "")
            result["queries"][qid] = {
                "us": parse_latency_us(raw),
                "human": raw,
                "tool": "",
                "top_score": 0,
                "result_count": 0,
            }
    if "query_scores" in data:
        if data["query_scores"].get("B1_scores"):
            result["scores"]["B1"] = data["query_scores"]["B1_scores"]
        if data["query_scores"].get("B6_scores"):
            result["scores"]["B6"] = data["query_scores"]["B6_scores"]

    # Graph benchmarks — old format
    gb = data.get("go_benchmarks", {}).get("graph_100_nodes", {})
    if gb:
        result["graph"] = {
            "pagerank_ns": gb.get("PageRank", {}).get("ns_per_op", 0),
            "blast_ns": gb.get("BlastRadius", {}).get("ns_per_op", 0),
            "between_ns": gb.get("ComputeBetweenness", {}).get("ns_per_op", 0),
        }
    # Graph benchmarks — new format
    gn = data.get("graph_benchmarks_ns", {})
    if gn:
        result["graph"] = {
            "pagerank_ns": gn.get("pagerank", 0),
            "blast_ns": gn.get("blast_radius", 0),
            "between_ns": gn.get("betweenness", 0),
        }

    # Test results
    sq = data.get("search_quality_tests", data.get("test_results", {}))
    result["tests"] = {
        "quality_passed": sq.get("passed", sq.get("search_quality_passed", 0)),
        "quality_failed": sq.get("failed", sq.get("search_quality_failed", 0)),
        "queries_passed": data.get("test_results", {}).get("benchmark_queries_passed", 0),
        "queries_failed": data.get("test_results", {}).get("benchmark_queries_failed", 0),
    }
    # old format
    ct = data.get("comprehensive_tool_tests", {})
    if ct:
        result["tests"]["tools_passed"] = len(ct.get("tools_tested", []))

    return result


def parse_latency_us(s: str) -> float:
    if not s:
        return 0
    s = s.strip()
    if s.endswith("µs"):
        return float(s[:-2])
    elif s.endswith("ms"):
        return float(s[:-2]) * 1000
    elif s.endswith("s"):
        return float(s[:-1]) * 1_000_000
    return 0


def fmt_us(us: float) -> str:
    if us == 0:
        return "—"
    if us < 1000:
        return f"{us:.0f}µs"
    elif us < 1_000_000:
        return f"{us/1000:.1f}ms"
    else:
        return f"{us/1_000_000:.2f}s"


def fmt_ns(ns: float) -> str:
    if ns == 0:
        return "—"
    if ns < 1000:
        return f"{ns:.0f}ns"
    elif ns < 1_000_000:
        return f"{ns/1000:.1f}µs"
    else:
        return f"{ns/1_000_000:.1f}ms"


def fmt_delta(old: float, new: float) -> str:
    if old == 0 or new == 0:
        return ""
    pct = ((new - old) / old) * 100
    if abs(pct) < 2:
        return f"{C.DIM}≈0%{C.RESET}"
    if pct < 0:
        return f"{C.GREEN}{pct:+.0f}%{C.RESET}"
    elif pct > 15:
        return f"{C.RED}{pct:+.0f}%{C.RESET}"
    else:
        return f"{C.YELLOW}{pct:+.0f}%{C.RESET}"


def fmt_score_bar(score: float, width: int = 20) -> str:
    filled = int(score * width)
    return f"{'█' * filled}{'░' * (width - filled)} {score:.4f}"


# ─── Terminal Dashboard ─────────────────────────────────────────────────────────
def print_dashboard(results: list[dict]):
    n = len(results)
    col_w = max(18, max(len(r["version"] or r["commit"]) for r in results) + 2)

    def header_label(r):
        label = r["version"] or r["commit"]
        return f"{C.BOLD}{label}{C.RESET}"

    def print_row(name, values, delta_pairs=None):
        row = f"  {name:<26}"
        for i, v in enumerate(values):
            row += f" {v:>{col_w}}"
        if delta_pairs:
            for d in delta_pairs:
                row += f"  {d}"
        print(row)

    def section(title):
        print(f"\n  {C.BOLD}{C.CYAN}{title}{C.RESET}")
        hdr = f"  {'':26}"
        for r in results:
            hdr += f" {header_label(r):>{col_w + len(C.BOLD) + len(C.RESET)}}"
        if n >= 2:
            hdr += f"  {'Δ':>8}"
        print(hdr)
        print(f"  {'─' * 26}" + f" {'─' * col_w}" * n + ("  " + "─" * 8 if n >= 2 else ""))

    # ── Title ──
    print()
    print(f"  {C.BOLD}╔{'═' * 60}╗{C.RESET}")
    print(f"  {C.BOLD}║{'qb-context Benchmark Comparison Dashboard':^60}║{C.RESET}")
    print(f"  {C.BOLD}╚{'═' * 60}╝{C.RESET}")

    # ── Overview ──
    section("Overview")
    print_row("Commit",    [r["commit"] for r in results])
    print_row("Date",      [r["date"][:10] if len(r["date"]) >= 10 else r["date"] for r in results])
    print_row("Nodes",     [f"{r['nodes']:,}" for r in results])
    print_row("Edges",     [f"{r['edges']:,}" for r in results])
    print_row("Index time",[f"{r['index_time_s']}s" for r in results])

    # ── Query Latencies ──
    section("Query Latencies")
    query_ids = ["A1", "A3", "B1", "B6", "C1", "C5"]
    query_labels = {
        "A1": "A1 read_symbol",
        "A3": "A3 search_code",
        "B1": "B1 payment concept",
        "B6": "B6 omnichannel",
        "C1": "C1 order flow",
        "C5": "C5 API→DB flow",
    }
    for qid in query_ids:
        vals = []
        uss = []
        for r in results:
            q = r["queries"].get(qid, {})
            h = q.get("human", "—")
            us = q.get("us", 0)
            vals.append(h if h else "—")
            uss.append(us)
        delta = []
        if n >= 2 and uss[0] and uss[-1]:
            delta = [fmt_delta(uss[0], uss[-1])]
        print_row(query_labels.get(qid, qid), vals, delta)

    # ── Score Quality ──
    has_scores = any(r["scores"] for r in results)
    if has_scores:
        section("Score Quality (top-10)")
        for qid in ["B1", "B6"]:
            vals = []
            tops = []
            for r in results:
                scores = r["scores"].get(qid, [])
                if scores:
                    top = max(scores)
                    avg = sum(scores) / len(scores)
                    vals.append(f"top={top:.4f}")
                    tops.append(top)
                else:
                    ts = r["queries"].get(qid, {}).get("top_score", 0)
                    vals.append(f"top={ts:.4f}" if ts else "—")
                    tops.append(ts)
            delta = []
            if n >= 2 and tops[0] and tops[-1]:
                # For scores, higher is better, so invert delta direction
                pct = ((tops[-1] - tops[0]) / tops[0]) * 100
                if abs(pct) < 1:
                    delta = [f"{C.DIM}≈0%{C.RESET}"]
                elif pct > 0:
                    delta = [f"{C.GREEN}{pct:+.1f}%{C.RESET}"]
                else:
                    delta = [f"{C.RED}{pct:+.1f}%{C.RESET}"]
            label = {"B1": "B1 payment", "B6": "B6 omnichannel"}.get(qid, qid)
            print_row(label, vals, delta)

        # Score distribution sparklines
        print()
        for qid in ["B1", "B6"]:
            for r in results:
                scores = r["scores"].get(qid, [])
                if scores:
                    label = r["version"] or r["commit"]
                    print(f"  {C.DIM}{qid} {label}:{C.RESET}")
                    for i, s in enumerate(scores[:10]):
                        bar = fmt_score_bar(s, 30)
                        print(f"    [{i:>2}] {bar}")

    # ── Graph Benchmarks ──
    has_graph = any(r["graph"] for r in results)
    if has_graph:
        section("Graph Micro-Benchmarks")
        for key, name in [("pagerank_ns","PageRank"), ("blast_ns","BlastRadius"), ("between_ns","Betweenness")]:
            vals = []
            raws = []
            for r in results:
                ns = r["graph"].get(key, 0)
                vals.append(fmt_ns(ns))
                raws.append(ns)
            delta = []
            if n >= 2 and raws[0] and raws[-1]:
                delta = [fmt_delta(raws[0], raws[-1])]
            print_row(name, vals, delta)

    # ── Test Results ──
    section("Test Results")
    for key, name in [
        ("quality_passed", "Search quality"),
        ("queries_passed", "Benchmark queries"),
        ("tools_passed", "Tool tests"),
    ]:
        vals = []
        for r in results:
            v = r["tests"].get(key, 0)
            fk = key.replace("passed", "failed")
            f = r["tests"].get(fk, 0)
            if v == 0 and f == 0:
                vals.append("—")
            elif f == 0:
                vals.append(f"{v} ✅")
            else:
                vals.append(f"{v}✅ {f}❌")
        print_row(name, vals)

    # ── Verdict ──
    if n >= 2:
        print()
        a, b = results[0], results[-1]
        regressions = 0
        improvements = 0
        for qid in query_ids:
            ua = a["queries"].get(qid, {}).get("us", 0)
            ub = b["queries"].get(qid, {}).get("us", 0)
            if ua and ub:
                pct = ((ub - ua) / ua) * 100
                if pct > 15:
                    regressions += 1
                elif pct < -10:
                    improvements += 1
        if regressions == 0:
            print(f"  {C.GREEN}{C.BOLD}✅ No performance regressions detected{C.RESET}")
        else:
            print(f"  {C.RED}{C.BOLD}⚠️  {regressions} potential regression(s) detected{C.RESET}")
        if improvements:
            print(f"  {C.GREEN}   {improvements} improvement(s){C.RESET}")
    print()


# ─── HTML Export ────────────────────────────────────────────────────────────────
def export_html(results: list[dict]) -> str:
    n = len(results)

    def th(r):
        return f"<th>{r['version'] or r['commit']}</th>"

    def td_latency(r, qid):
        q = r["queries"].get(qid, {})
        return f"<td>{q.get('human', '—')}</td>"

    def td_graph(r, key):
        ns = r["graph"].get(key, 0)
        return f"<td>{fmt_ns(ns)}</td>"

    query_ids = ["A1", "A3", "B1", "B6", "C1", "C5"]
    query_labels = {
        "A1": "A1 read_symbol", "A3": "A3 search_code",
        "B1": "B1 payment concept", "B6": "B6 omnichannel",
        "C1": "C1 order flow", "C5": "C5 API→DB flow",
    }

    rows_latency = ""
    for qid in query_ids:
        cells = "".join(td_latency(r, qid) for r in results)
        rows_latency += f"<tr><td>{query_labels[qid]}</td>{cells}</tr>\n"

    rows_graph = ""
    for key, name in [("pagerank_ns","PageRank"),("blast_ns","BlastRadius"),("between_ns","Betweenness")]:
        cells = "".join(td_graph(r, key) for r in results)
        rows_graph += f"<tr><td>{name}</td>{cells}</tr>\n"

    rows_overview = ""
    for field, label in [("commit","Commit"),("date","Date"),("nodes","Nodes"),("edges","Edges")]:
        cells = ""
        for r in results:
            v = r.get(field, "")
            if field == "date" and len(str(v)) >= 10:
                v = str(v)[:10]
            cells += f"<td>{v}</td>"
        rows_overview += f"<tr><td>{label}</td>{cells}</tr>\n"

    headers = "".join(th(r) for r in results)

    return f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>qb-context Benchmark Report</title>
<style>
  body {{ font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
         max-width: 960px; margin: 40px auto; padding: 0 20px; color: #1a1a2e; background: #f8f9fa; }}
  h1 {{ border-bottom: 3px solid #0969da; padding-bottom: 8px; }}
  h2 {{ color: #0969da; margin-top: 2em; }}
  table {{ border-collapse: collapse; width: 100%; margin: 12px 0 24px; }}
  th, td {{ border: 1px solid #d1d5db; padding: 8px 14px; text-align: right; }}
  th {{ background: #0969da; color: white; }}
  td:first-child {{ text-align: left; font-weight: 600; background: #f0f4f8; }}
  tr:nth-child(even) {{ background: #f6f8fa; }}
  tr:hover {{ background: #e8f0fe; }}
  .meta {{ color: #57606a; font-size: 0.9em; }}
  .pass {{ color: #1a7f37; }} .fail {{ color: #cf222e; }}
</style>
</head>
<body>
<h1>qb-context Benchmark Report</h1>
<p class="meta">Generated {datetime.now().strftime('%Y-%m-%d %H:%M')} · {n} version(s) compared</p>

<h2>Overview</h2>
<table><tr><th>Metric</th>{headers}</tr>
{rows_overview}</table>

<h2>Query Latencies</h2>
<table><tr><th>Query</th>{headers}</tr>
{rows_latency}</table>

<h2>Graph Micro-Benchmarks</h2>
<table><tr><th>Benchmark</th>{headers}</tr>
{rows_graph}</table>

</body></html>"""


# ─── Main ───────────────────────────────────────────────────────────────────────
def main():
    parser = argparse.ArgumentParser(description="Compare qb-context benchmark results")
    parser.add_argument("files", nargs="*", help="Result JSON files to compare")
    parser.add_argument("--latest", type=int, default=0, help="Compare N most recent results")
    parser.add_argument("--html", action="store_true", help="Output HTML report")
    parser.add_argument("--no-color", action="store_true", help="Disable ANSI colors")
    args = parser.parse_args()

    if args.no_color or args.html:
        C.disable()

    results_dir = Path(__file__).parent / "results"

    # Determine files to load
    files = args.files
    if not files:
        files = sorted(glob.glob(str(results_dir / "*.json")))
        if args.latest > 0:
            files = files[-args.latest:]

    if not files:
        print("No benchmark result files found.")
        print(f"  Looked in: {results_dir}")
        print(f"  Run benchmarks first: ./benchmarks/run.sh")
        sys.exit(1)

    results = []
    for f in files:
        try:
            results.append(load_result(f))
        except Exception as e:
            print(f"Warning: Failed to load {f}: {e}", file=sys.stderr)

    if not results:
        print("No valid results loaded.")
        sys.exit(1)

    if args.html:
        print(export_html(results))
    else:
        print_dashboard(results)


if __name__ == "__main__":
    main()

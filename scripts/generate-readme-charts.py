#!/usr/bin/env python3
"""Generate README charts for context-mcp.

Produces 4 publication-quality PNG charts styled for GitHub dark mode:
  1. architecture.png     - Horizontal pipeline flow diagram
  2. ranking-weights.png  - Donut chart of search ranking signals
  3. benchmark-latency.png - Horizontal bar chart of query latencies
  4. token-usage.png      - Real-world token usage comparison (3-way)
"""

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import matplotlib.patches as mpatches
from matplotlib.patches import FancyBboxPatch, FancyArrowPatch
import numpy as np
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
OUTPUT_DIR = Path(__file__).parent.parent / "docs" / "images"

# ---------------------------------------------------------------------------
# Style constants
# ---------------------------------------------------------------------------
BG       = "#0d1117"
TEXT     = "#e6edf3"
DIM_TEXT = "#8b949e"
ACCENT   = ["#58a6ff", "#3fb950", "#d29922", "#f85149", "#bc8cff", "#79c0ff"]

DPI = 150

def _apply_style():
    """Apply global dark-mode style settings."""
    plt.rcParams.update({
        "figure.facecolor":  BG,
        "axes.facecolor":    BG,
        "savefig.facecolor": BG,
        "text.color":        TEXT,
        "axes.labelcolor":   TEXT,
        "xtick.color":       TEXT,
        "ytick.color":       TEXT,
        "font.family":       "sans-serif",
        "font.sans-serif":   ["DejaVu Sans", "Helvetica", "Arial"],
        "axes.edgecolor":    "#30363d",
        "axes.grid":         False,
    })


# ===================================================================
# Chart 1 -- Architecture pipeline
# ===================================================================
def generate_architecture():
    _apply_style()
    fig, ax = plt.subplots(figsize=(14.5, 3.5))
    ax.set_xlim(0, 14.5)
    ax.set_ylim(0, 3.5)
    ax.axis("off")

    # Node definitions: (label, sub_lines, x_center)
    nodes = [
        ("Source Files",    ["Go, JS, TS, PHP"],                              1.1),
        ("AST Parser",     ["go/ast (native)", "tree-sitter"],                3.5),
        ("SQLite + FTS5",  ["Nodes, Edges", "Embeddings (sqlite-vec)"],       6.0),
        ("Graph Engine",   ["PageRank, Betweenness", "Louvain Communities"],  8.5),
        ("MCP Server",     ["20 Tools, 5 Prompts", "4 Resources"],           10.9),
        ("LLM Agents",     ["Claude Code, Desktop", "Codex, Any MCP Client"],13.2),
    ]

    box_w = 1.8
    box_h = 0.9
    box_y = 2.0
    sub_start_y = box_y - 0.35
    sub_gap = 0.38

    boxes_x = []  # track right edges for arrows

    for i, (label, subs, cx) in enumerate(nodes):
        x = cx - box_w / 2
        # Main box
        box = FancyBboxPatch(
            (x, box_y), box_w, box_h,
            boxstyle="round,pad=0.12",
            linewidth=1.5,
            edgecolor=ACCENT[0],
            facecolor="#161b22",
        )
        ax.add_patch(box)
        ax.text(cx, box_y + box_h / 2, label,
                ha="center", va="center", fontsize=9, fontweight="bold",
                color=TEXT)

        # Sub-labels
        for j, sub in enumerate(subs):
            sy = sub_start_y - j * sub_gap
            sub_box = FancyBboxPatch(
                (x + 0.05, sy), box_w - 0.10, 0.32,
                boxstyle="round,pad=0.06",
                linewidth=0.8,
                edgecolor="#30363d",
                facecolor="#0d1117",
            )
            ax.add_patch(sub_box)
            ax.text(cx, sy + 0.16, sub,
                    ha="center", va="center", fontsize=6,
                    color=DIM_TEXT)

        boxes_x.append((cx + box_w / 2, cx - box_w / 2))

    # Arrows between consecutive main boxes
    arrow_y = box_y + box_h / 2
    for k in range(len(nodes) - 1):
        x_start = boxes_x[k][0] + 0.05
        x_end   = boxes_x[k + 1][1] - 0.05
        arrow = FancyArrowPatch(
            (x_start, arrow_y), (x_end, arrow_y),
            arrowstyle="-|>",
            mutation_scale=14,
            linewidth=1.8,
            color=ACCENT[0],
            connectionstyle="arc3,rad=0",
        )
        ax.add_patch(arrow)

    fig.tight_layout(pad=0.5)
    fig.savefig(OUTPUT_DIR / "architecture.png", dpi=DPI, transparent=False)
    plt.close(fig)
    print("  -> architecture.png")


# ===================================================================
# Chart 2 -- Ranking weights donut
# ===================================================================
def generate_ranking_weights():
    _apply_style()
    fig, ax = plt.subplots(figsize=(6, 6))

    labels  = ["Personalized\nPageRank", "BM25\n(FTS5)",
               "Betweenness\nCentrality", "Semantic\nSimilarity"]
    sizes   = [35, 30, 20, 15]
    colors  = [ACCENT[0], ACCENT[1], ACCENT[2], ACCENT[4]]
    explode = (0.02, 0.02, 0.02, 0.02)

    wedges, texts, autotexts = ax.pie(
        sizes, labels=labels, colors=colors, explode=explode,
        autopct="%1.0f%%", startangle=90,
        pctdistance=0.78,
        wedgeprops=dict(width=0.40, edgecolor=BG, linewidth=2),
        textprops=dict(color=TEXT, fontsize=9),
    )
    for at in autotexts:
        at.set_color(TEXT)
        at.set_fontweight("bold")
        at.set_fontsize(10)

    # Center text
    ax.text(0, 0.05, "Hybrid\nRanking", ha="center", va="center",
            fontsize=14, fontweight="bold", color=TEXT)

    # Subtitle below the chart
    fig.text(0.5, 0.06,
             "Optimized via 130-configuration parameter sweep",
             ha="center", fontsize=9, color=DIM_TEXT, style="italic")

    fig.tight_layout(pad=1.2)
    fig.subplots_adjust(bottom=0.12)
    fig.savefig(OUTPUT_DIR / "ranking-weights.png", dpi=DPI, transparent=False)
    plt.close(fig)
    print("  -> ranking-weights.png")


# ===================================================================
# Chart 3 -- Benchmark latency bars
# ===================================================================
def generate_benchmark_latency():
    _apply_style()
    fig, ax = plt.subplots(figsize=(10, 3.5))

    categories = ["Regex Search", "Cross-file Flow", "Concept Search", "Exact Symbol Lookup"]
    # For range bars: (min, max).  Single values: (val, val)
    ranges = [
        (77, 77),
        (2.2, 3.0),
        (1.2, 5.7),
        (0.28, 0.28),
    ]
    bar_colors = [ACCENT[3], ACCENT[2], ACCENT[1], ACCENT[0]]

    y_pos = np.arange(len(categories))
    bar_h = 0.50

    for i, ((lo, hi), color) in enumerate(zip(ranges, bar_colors)):
        if lo == hi:
            ax.barh(y_pos[i], hi, height=bar_h, color=color,
                    edgecolor="none", zorder=3)
            ax.text(hi * 1.15, y_pos[i], f"{hi} ms",
                    va="center", ha="left", fontsize=9, color=TEXT,
                    fontweight="bold")
        else:
            # Draw range bar: full bar to max, darker overlay to min
            ax.barh(y_pos[i], hi, height=bar_h, color=color,
                    edgecolor="none", zorder=3, alpha=0.9)
            ax.barh(y_pos[i], lo, height=bar_h, color=color,
                    edgecolor="none", zorder=4, alpha=0.55)
            # Min marker line
            ax.plot([lo, lo], [y_pos[i] - bar_h / 2, y_pos[i] + bar_h / 2],
                    color=TEXT, linewidth=1.5, zorder=5)
            ax.text(hi * 1.12, y_pos[i], f"{lo} - {hi} ms",
                    va="center", ha="left", fontsize=9, color=TEXT,
                    fontweight="bold")

    ax.set_xscale("log")
    ax.set_xlim(0.1, 200)
    ax.set_yticks(y_pos)
    ax.set_yticklabels(categories, fontsize=10)
    ax.set_xlabel("Latency (ms, log scale)", fontsize=10, color=DIM_TEXT)
    ax.tick_params(axis="x", colors=DIM_TEXT)
    ax.xaxis.set_major_formatter(matplotlib.ticker.ScalarFormatter())
    ax.xaxis.get_major_formatter().set_scientific(False)

    # Grid lines (vertical only, subtle)
    ax.set_axisbelow(True)
    ax.xaxis.grid(True, color="#21262d", linewidth=0.7, which="both")
    ax.yaxis.grid(False)

    # Remove spines
    for spine in ax.spines.values():
        spine.set_visible(False)

    fig.tight_layout(pad=1.2)
    fig.subplots_adjust(bottom=0.18)

    # Footer annotation (after layout adjustments to avoid overlap)
    fig.text(0.5, 0.02,
             "Tested on real-world Laravel codebase (~780 PHP files, 18K+ nodes, 25K+ edges)",
             ha="center", fontsize=8, color=DIM_TEXT, style="italic")
    fig.savefig(OUTPUT_DIR / "benchmark-latency.png", dpi=DPI, transparent=False)
    plt.close(fig)
    print("  -> benchmark-latency.png")


# ===================================================================
# Chart 4 -- Real-world token usage comparison
# ===================================================================
def generate_token_usage():
    _apply_style()
    fig, ax = plt.subplots(figsize=(8, 5))

    # Data: 3 runs of the same task (including subagent tokens)
    labels = ["No MCP", "context-mcp", "codebase-memory-\nmcp"]
    tokens = [30375, 21437, 43250]
    colors = ["#484f58", ACCENT[1], ACCENT[2]]
    tool_counts = ["69 tools", "41 tools", "42 tools"]

    x_pos = np.arange(len(labels))
    bar_w = 0.55

    bars = ax.bar(x_pos, [t / 1000 for t in tokens], width=bar_w,
                  color=colors, edgecolor="none", zorder=3)

    # Value labels on top of each bar
    for i, bar in enumerate(bars):
        h = bar.get_height()
        ax.text(bar.get_x() + bar.get_width() / 2, h + 0.8,
                f"{h:.1f}K",
                ha="center", va="bottom", fontsize=13, color=colors[i],
                fontweight="bold")
        # Tool count inside bar
        ax.text(bar.get_x() + bar.get_width() / 2, h / 2,
                tool_counts[i],
                ha="center", va="center", fontsize=9, color=TEXT,
                alpha=0.9)

    ax.set_xticks(x_pos)
    ax.set_xticklabels(labels, fontsize=11)
    ax.set_ylabel("Total Tokens (thousands)", fontsize=10, color=DIM_TEXT)
    ax.set_ylim(0, 52)
    ax.tick_params(axis="y", colors=DIM_TEXT)
    ax.tick_params(axis="x", colors=TEXT)

    # Title
    ax.set_title("Token Usage: Same Task, Three Approaches",
                 fontsize=12, fontweight="bold", color=TEXT, pad=12)

    # Remove spines
    for spine in ax.spines.values():
        spine.set_visible(False)

    # Subtle horizontal grid
    ax.set_axisbelow(True)
    ax.yaxis.grid(True, color="#21262d", linewidth=0.7)
    ax.xaxis.grid(False)

    fig.tight_layout(pad=1.2)
    fig.subplots_adjust(bottom=0.14)

    # Footer
    fig.text(0.5, 0.02,
             "Task: Find all differences between v1/order and v3/order API paths",
             ha="center", fontsize=8, color=DIM_TEXT, style="italic")

    fig.savefig(OUTPUT_DIR / "token-usage.png", dpi=DPI, transparent=False)
    plt.close(fig)
    print("  -> token-usage.png")


# ===================================================================
# Main
# ===================================================================
def main():
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    print(f"Generating charts into {OUTPUT_DIR} ...")
    generate_architecture()
    generate_ranking_weights()
    generate_benchmark_latency()
    generate_token_usage()
    print("Done.")


if __name__ == "__main__":
    main()

# context-mcp User Guide

A local-first MCP daemon that indexes your codebase and gives LLM agents surgical, context-aware code retrieval. Provides 17 tools, 5 prompt templates, and 4 resources.

---

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [Running the Daemon](#running-the-daemon)
- [Harness Commands](#harness-commands)
- [CLI Mode](#cli-mode)
- [MCP Tools Reference](#mcp-tools-reference)
  - [context](#context--search--discover-code)
  - [impact](#impact--blast-radius-analysis)
  - [read_symbol](#read_symbol--read-source-code)
  - [list_file_symbols](#list_file_symbols--file-symbol-inventory)
  - [query](#query--raw-sql)
  - [index](#index--re-index-repository)
  - [health](#health--system-health-status)
  - [trace_call_path](#trace_call_path--trace-call-paths)
  - [get_key_symbols](#get_key_symbols--top-symbols-by-centrality)
  - [search_code](#search_code--regex-source-search)
  - [detect_changes](#detect_changes--changed-symbol-detection)
  - [get_architecture_summary](#get_architecture_summary--architecture-overview)
  - [explore](#explore--explore-codebase)
  - [understand](#understand--deep-symbol-understanding)
  - [assemble_context](#assemble_context--token-budgeted-context-assembly)
  - [checkpoint_context](#checkpoint_context--create-index-checkpoint)
  - [read_delta](#read_delta--compare-against-checkpoint)
- [MCP Prompts](#mcp-prompts)
- [MCP Resources](#mcp-resources)
- [Connecting to Claude Desktop](#connecting-to-claude-desktop)
- [Connecting to Claude Code](#connecting-to-claude-code)
- [Connecting to Codex](#connecting-to-codex)
- [Manual Smoke Tests](#manual-smoke-tests)
- [Configuration](#configuration)
- [How Indexing Works](#how-indexing-works)
- [Supported Languages](#supported-languages)

---

## Quick Start

```bash
# Build
go build -tags "fts5" -o context-mcp ./cmd/context-mcp

# With ONNX neural embeddings (recommended for best search quality)
go build -tags "fts5,onnx" -o context-mcp ./cmd/context-mcp

# Index and query your repo via CLI
./context-mcp -repo /path/to/your/project cli context '{"query": "authentication"}'

# Or run as a persistent MCP daemon
./context-mcp -repo /path/to/your/project
```

---

## Installation

### Prerequisites

- Go 1.25+ with CGO enabled (required for SQLite FTS5)
- A C compiler (gcc/clang) -- needed by `go-sqlite3`

### Build from Source

```bash
git clone https://github.com/maplenk/context-mcp.git
cd context-mcp
go build -tags "fts5" -o context-mcp ./cmd/context-mcp

# With ONNX neural embeddings (recommended for best search quality)
go build -tags "fts5,onnx" -o context-mcp ./cmd/context-mcp
```

The `-tags "fts5"` flag is **required** -- it enables SQLite full-text search.

### Verify

```bash
./context-mcp cli --list
```

Expected output:

```
TOOL                       DESCRIPTION
----                       -----------
context                    Discovers relevant code symbols using hybrid lexical + semantic search...
impact                     Analyzes the blast radius of a code symbol...
read_symbol                Safely inspects a symbol with bounded and summary modes...
list_file_symbols          Lists indexed symbols in a file in source order...
query                      Executes a read-only SQL query against the structural database.
index                      Triggers a full re-index of the repository.
health                     Returns system health status.
trace_call_path            Traces call paths between two symbols.
get_key_symbols            Returns the most important symbols by centrality.
search_code                Regex search across indexed source files.
detect_changes             Detects changed symbols since a git ref.
get_architecture_summary   Comprehensive architecture overview with communities and hubs.
explore                    Explores codebase by searching for a symbol with optional deps.
understand                 Deep symbol understanding with callers, callees, and PageRank.
assemble_context           Token-budgeted context assembly with ranked code snippets.
checkpoint_context         Create a named checkpoint of the current index state.
read_delta                 Compare current index against a named checkpoint.
```

---

## Running the Daemon

### Stdio

The default daemon mode runs as an MCP server over stdio. It indexes your repo on startup, watches for file changes, and serves tool requests.

```bash
./context-mcp -repo /path/to/your/project
```

On startup it will:

1. Scan all source files in the repo
2. Parse AST nodes (functions, classes, structs, methods)
3. Build a dependency graph and compute centrality metrics
4. Discover architecture documents (ARCHITECTURE.md, ADR.md, etc.)
5. Start watching for file changes (incremental re-indexing)
6. Listen for MCP JSON-RPC requests on stdin

The SQLite database is stored at `<repo>/.context-mcp/index.db` by default.

### Streamable HTTP

Use `serve-http` when you want a local MCP endpoint on `http://127.0.0.1:<port>/mcp`:

```bash
./context-mcp -repo /path/to/your/project serve-http -port 8080

# Optional bearer auth
./context-mcp -repo /path/to/your/project serve-http -port 8080 -bearer-token dev-token
```

If `-bearer-token` is set, clients must send `Authorization: Bearer <token>`. You can also provide the token with `QB_CONTEXT_BEARER_TOKEN`.

### Stopping

Send `SIGINT` (Ctrl+C) or `SIGTERM` for graceful shutdown.

---

## Harness Commands

`context-mcp` includes helper subcommands for Claude Code and Codex configuration:

- `install` writes or registers a client config entry
- `print-config` prints a manual config snippet without mutating client state
- `doctor` verifies binary resolution, client config, repo path, and index presence
- `uninstall` removes the generated client entry

### Transport Modes

- `stdio` is the default and launches `context-mcp` as a subprocess
- `http` and `sse` expect `--url` and point the client at an already-running MCP endpoint
- Claude Code supports `user`, `local`, and `project` scopes
- Codex helper install writes to `~/.codex/config.toml`; project-local `.codex/config.toml` remains a manual option

### Examples

```bash
# Claude Code stdio install
./context-mcp install --client claude-code --repo /absolute/path/to/your/project --profile extended

# Claude Code local-scope install
./context-mcp install --client claude-code --repo /absolute/path/to/your/project --scope local

# Codex stdio install
./context-mcp install --client codex --repo /absolute/path/to/your/project --profile extended

# Print an HTTP snippet instead of installing it
./context-mcp print-config --client codex --repo /absolute/path/to/your/project --transport http --url http://127.0.0.1:8080/mcp

# Verify installs
./context-mcp doctor --repo /absolute/path/to/your/project
./context-mcp doctor --client claude-code --repo /absolute/path/to/your/project

# Remove a Claude Code local-scope install
./context-mcp uninstall --client claude-code --scope local
```

---

## CLI Mode

Use the `cli` subcommand to invoke any MCP tool directly from the terminal. This is useful for testing, debugging, and scripting.

### Syntax

```bash
./context-mcp [flags] cli <tool_name> [json_args]
```

### List Available Tools

```bash
./context-mcp cli --list
```

### Examples

**Search for code related to "payment processing":**

```bash
./context-mcp -repo . cli context '{"query": "payment processing", "limit": 5}'
```

**Analyze the blast radius of a symbol:**

```bash
./context-mcp -repo . cli impact '{"symbol_id": "handlePayment", "depth": 3}'
```

**Read the source code of a function:**

```bash
./context-mcp -repo . cli read_symbol '{"symbol_id": "ParseFile", "mode": "bounded"}'
```

**Run a diagnostic SQL query:**

```bash
./context-mcp -repo . cli query '{"sql": "SELECT symbol_name, file_path FROM nodes WHERE node_type = 1 LIMIT 10"}'
```

**Trigger a full re-index:**

```bash
./context-mcp -repo . cli index '{}'
```

**View architecture communities:**

```bash
./context-mcp -repo . cli context '{"query": "_", "mode": "architecture"}'
```

> All output is JSON printed to stdout. Logs go to stderr.

---

## MCP Tools Reference

### `context` -- Search & Discover Code

Discovers relevant code symbols using multi-signal ranked search. Combines lexical search (FTS5/BM25), semantic similarity, graph-based PageRank, betweenness centrality, and in-degree authority into a composite score.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | yes | -- | Natural language or keyword query |
| `limit` | integer | no | 10 | Max results to return |
| `mode` | string | no | `"search"` | `"search"` for hybrid search, `"architecture"` for community detection |
| `max_per_file` | integer | no | 3 | Maximum results per unique file path |
| `active_files` | string[] | no | -- | File paths the developer is currently editing for PPR personalization |

**Search mode example:**

```json
{
  "query": "user authentication middleware",
  "limit": 5
}
```

Returns ranked results with composite scores. If architecture documents (ARCHITECTURE.md, ADR.md, etc.) exist in the repo, they are included as `architecture_context` in the response.

**Architecture mode example:**

```json
{
  "query": "_",
  "mode": "architecture"
}
```

Returns Louvain community clusters -- groups of tightly coupled code symbols -- along with a modularity score. Useful for understanding domain boundaries and component coupling.

**Response (architecture mode):**

```json
{
  "mode": "architecture",
  "communities": [
    {"id": 0, "node_ids": ["nodeId1", "nodeId2", "nodeId3"]},
    {"id": 1, "node_ids": ["nodeId4", "nodeId5"]}
  ],
  "modularity": 0.42,
  "count": 2
}
```

---

### `impact` -- Blast Radius Analysis

Traces all downstream dependents of a symbol via BFS graph traversal and classifies them by risk level based on distance.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol_id` | string | yes | -- | Symbol name or hash ID |
| `depth` | integer | no | 5 | Max BFS traversal depth |

**Example:**

```json
{
  "symbol_id": "handlePayment",
  "depth": 4
}
```

**Response:**

```json
{
  "symbol": "handlePayment",
  "depth": 4,
  "risk_score": 0.73,
  "affected_count": 12,
  "direct": [{"id": "...", "symbol_name": "processOrder", "file_path": "..."}],
  "high_risk": [...],
  "medium_risk": [...],
  "low_risk": [...],
  "affected_tests": [...],
  "summary": "Symbol has betweenness 0.73 -- 3 direct dependents, 12 total affected, 2 tests impacted"
}
```

**Risk levels by hop distance:**

| Hop | Risk Level |
|-----|-----------|
| 1 | CRITICAL -- direct dependents |
| 2 | HIGH |
| 3 | MEDIUM |
| 4+ | LOW |

The `risk_score` is the betweenness centrality of the target symbol (0-1). Higher values indicate bottleneck nodes.

---

### `read_symbol` -- Read Source Code

Safely inspects a symbol by reading indexed source from disk with bounded defaults, explicit windowing, and deterministic flow summaries for large symbols.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol_id` | string | yes | -- | Symbol name or hash ID |
| `mode` | string | no | `"bounded"` | `"bounded"`, `"signature"`, `"section"`, `"flow_summary"`, or `"full"` |
| `max_chars` | integer | no | `6000` | Soft output budget for source-bearing modes, clamped to `20000` |
| `max_lines` | integer | no | `60` | Soft output budget for source-bearing modes, clamped to `200` |
| `start_line` | integer | no | -- | Optional 1-based file-relative start line, takes precedence over `section` |
| `end_line` | integer | no | -- | Optional 1-based file-relative end line, takes precedence over `section` |
| `section` | string | no | -- | `"top"`, `"middle"`, `"bottom"`, or `"auto"` for section reads |

**Example:**

```json
{
  "symbol_id": "ParseFile",
  "mode": "bounded"
}
```

**Response:**

```json
{
  "symbol_name": "ParseFile",
  "file_path": "internal/parser/parser.go",
  "node_type": "function",
  "start_byte": 1234,
  "end_byte": 2345,
  "symbol_start_line": 42,
  "symbol_end_line": 118,
  "mode_requested": "bounded",
  "mode_used": "bounded",
  "truncated": true,
  "downgraded": false,
  "downgrade_reason": "",
  "signature": "func ParseFile(path string, repoRoot string) (*ParseResult, error)",
  "next_modes": ["signature", "section", "flow_summary", "full"],
  "selected_start_line": 42,
  "selected_end_line": 96,
  "selected_section": "auto",
  "source": "func ParseFile(path string, repoRoot string) (*ParseResult, error) {\n  ..."
}
```

Behavior notes:

- Omitting `mode` never returns a giant raw symbol body; it defaults to `bounded`.
- `full` is only honored when the symbol fits within both active safety budgets. Otherwise it downgrades to `bounded` and sets `downgraded=true`.
- `signature` returns metadata plus the signature only.
- `section` can target `start_line`/`end_line` or a named `section`.
- `flow_summary` returns structured steps, helper calls, validations, side effects, and suggested follow-up reads instead of large raw source.

Accepts either the symbol name (for example `"ParseFile"`) or the full SHA-256 hash ID. If the name matches multiple symbols, the first match is returned.

---

### `list_file_symbols` -- File Symbol Inventory

Lists indexed symbols in a file in source order. Use it when you need a method inventory and do not want to fall back to shell grep.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `path` | string | yes | -- | Repo-relative path or absolute-under-repo path |
| `limit` | integer | no | `200` | Maximum symbols to return |
| `kinds` | string[] | no | -- | Optional filters such as `function`, `method`, `class`, `struct`, `interface`, `route` |

**Example:**

```json
{
  "path": "app/Http/Controllers/OrderController.php",
  "limit": 50,
  "kinds": ["method"]
}
```

**Response:**

```json
{
  "path": "app/Http/Controllers/OrderController.php",
  "count": 3,
  "total": 3,
  "truncated": false,
  "symbols": [
    {
      "id": "f321b80a...",
      "symbol_name": "OrderController.__construct",
      "node_type": "method",
      "start_line": 86,
      "end_line": 91,
      "signature": "public function __construct()",
      "read_symbol_args": {
        "symbol_id": "f321b80a...",
        "mode": "bounded"
      }
    }
  ]
}
```

---

### `query` -- Raw SQL

Executes a read-only SQL query against the structural database. Useful for diagnostics and custom analysis.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `sql` | string | yes | -- | A `SELECT` query |

**Example queries:**

```sql
-- List all indexed functions
SELECT symbol_name, file_path FROM nodes WHERE node_type = 1

-- Count symbols per file
SELECT file_path, COUNT(*) as count FROM nodes GROUP BY file_path ORDER BY count DESC

-- Find call edges
SELECT n1.symbol_name AS caller, n2.symbol_name AS callee
FROM edges e
JOIN nodes n1 ON e.source_id = n1.id
JOIN nodes n2 ON e.target_id = n2.id
WHERE e.edge_type = 1

-- Check betweenness scores
SELECT n.symbol_name, ns.betweenness
FROM node_scores ns
JOIN nodes n ON ns.node_id = n.id
ORDER BY ns.betweenness DESC LIMIT 10

-- List architecture documents
SELECT project, source_hash FROM project_summaries
```

**Restrictions:** Only `SELECT` statements are allowed. Statements containing `INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `CREATE`, `ATTACH`, or `load_extension` are rejected. Multi-statement queries (using `;`) are blocked.

**Database schema:**

| Table | Description |
|-------|-------------|
| `nodes` | AST nodes (id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum) |
| `edges` | Relationships between nodes (source_id, target_id, edge_type) |
| `nodes_fts` | FTS5 full-text index on symbol_name and content_sum |
| `node_embeddings` | Vector embeddings for semantic search (384-dim TF-IDF or 768-dim ONNX) |
| `node_scores` | Precomputed graph metrics (pagerank, betweenness) |
| `project_summaries` | Architecture decision records and design docs |

---

### `index` -- Re-index Repository

Triggers a full re-index of the repository. The daemon also indexes automatically on startup and on file changes, so manual re-indexing is rarely needed.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `path` | string | no | -- | Optional: specific path to re-index (file or directory) |

**Example:**

```json
{}
```

---

### `health` -- System Health Status

Returns system health status including uptime, indexed file count, node/edge counts, and database size. Takes no parameters.

**Parameters:** None

**Example:**

```json
{}
```

**Response:**

```json
{
  "status": "ok",
  "uptime": "2h15m",
  "indexed_files": 142,
  "nodes": 1834,
  "edges": 4521,
  "db_size_bytes": 8392704
}
```

---

### `trace_call_path` -- Trace Call Paths

Traces call paths between two symbols. Useful for understanding how control flows from one function to another through the dependency graph.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `from` | string | yes | -- | Source symbol name or ID |
| `to` | string | yes | -- | Target symbol name or ID |
| `max_depth` | integer | no | 10 | Maximum traversal depth |

**Example:**

```json
{
  "from": "HandleRequest",
  "to": "SaveToDatabase",
  "max_depth": 8
}
```

---

### `get_key_symbols` -- Top Symbols by Centrality

Returns the most important symbols in the codebase ranked by centrality metrics. Useful for identifying core abstractions and architectural hotspots.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `limit` | integer | no | 20 | Maximum number of symbols to return |
| `file_filter` | string | no | -- | Optional file path prefix to filter results |

**Example:**

```json
{
  "limit": 10,
  "file_filter": "internal/graph/"
}
```

---

### `search_code` -- Regex Source Search

Performs regex search across indexed source files. Returns matching lines with file paths and line numbers.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `pattern` | string | yes | -- | Regex pattern to search for |
| `file_filter` | string | no | -- | Optional glob pattern to filter files (e.g. `"*.go"`) |
| `limit` | integer | no | 20 | Maximum number of results |

**Example:**

```json
{
  "pattern": "func.*Parse",
  "file_filter": "*.go",
  "limit": 10
}
```

---

### `detect_changes` -- Changed Symbol Detection

Detects symbols that have changed since a given git ref. Useful for understanding what functions/classes were modified in recent commits or compared to a branch.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `since` | string | yes | -- | Git ref to compare against (e.g. `"HEAD~5"`, `"main"`, a commit SHA) |
| `path` | string | no | -- | Optional path filter to restrict detection scope |

**Example:**

```json
{
  "since": "HEAD~5",
  "path": "internal/"
}
```

---

### `get_architecture_summary` -- Architecture Overview

Returns a comprehensive architecture overview including community clusters, entry points, hub nodes, and connector symbols. Provides a high-level map of the codebase structure.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `limit` | integer | no | 10 | Maximum number of items per category |

**Example:**

```json
{
  "limit": 15
}
```

---

### `explore` -- Explore Codebase

Explores the codebase by searching for a symbol with optional dependency analysis. A good starting point when you want to find a symbol and optionally see what it depends on.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol` | string | yes | -- | Symbol name to search for |
| `include_deps` | boolean | no | false | Whether to include dependency analysis |
| `depth` | integer | no | 2 | Dependency traversal depth (when `include_deps` is true) |

**Example:**

```json
{
  "symbol": "IndexRepository",
  "include_deps": true,
  "depth": 3
}
```

---

### `understand` -- Deep Symbol Understanding

Provides deep understanding of a symbol using 3-tier resolution (exact match, fuzzy match, search fallback) plus callers, callees, PageRank score, and community membership. The most comprehensive single-symbol analysis tool.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol` | string | yes | -- | Symbol name to analyze |

**Example:**

```json
{
  "symbol": "ComputePageRank"
}
```

---

### `assemble_context` -- Token-Budgeted Context Assembly

Returns ranked code snippets fitted within a token budget. Use when you need to gather context efficiently within a token limit. Supports multiple output fidelities from compact summaries to full source.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | yes | -- | Search query to find relevant code |
| `budget_tokens` | integer | no | 4000 | Maximum token budget |
| `mode` | string | no | `"snippets"` | Output fidelity: `summary`, `signatures`, `snippets`, `bundle`, or `full` |
| `active_files` | string[] | no | -- | File paths currently being edited |
| `max_per_file` | integer | no | 2 | Maximum results per file |
| `include_neighbors` | boolean | no | false | Include callers/callees of top results |

**Example:**

```json
{
  "query": "payment processing",
  "budget_tokens": 4000,
  "mode": "snippets"
}
```

---

### `checkpoint_context` -- Create Index Checkpoint

Creates a named checkpoint of the current index state. Use with `read_delta` to see what changed since this point.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | no | (auto-generated) | Checkpoint name |

**Example:**

```json
{
  "name": "before-refactor"
}
```

---

### `read_delta` -- Compare Against Checkpoint

Compares the current index state against a named checkpoint. Shows added, modified, and deleted symbols since the checkpoint was created.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `since` | string | yes | -- | Checkpoint name to compare against |
| `path` | string | no | -- | Filter by file path prefix |
| `limit` | integer | no | 20 | Maximum items per change type |

**Example:**

```json
{
  "since": "before-refactor",
  "path": "internal/",
  "limit": 10
}
```

---

## MCP Prompts

context-mcp provides 5 prompt templates that orchestrate multi-tool workflows:

| Prompt | Description |
|--------|-------------|
| `review_changes` | Review recent code changes and their impact (args: `since`) |
| `trace_impact` | Trace the blast radius of changes to a symbol (args: `symbol`, required) |
| `prepare_fix_context` | Gather context needed to fix a bug (args: `description` required, `file` optional) |
| `onboard_repo` | Get oriented in the codebase -- architecture, key symbols, entry points |
| `collect_minimal_context` | Collect minimum context for a task within a token budget (args: `task` required, `budget` optional) |

---

## MCP Resources

context-mcp exposes 4 read-only resources for live index data:

| Resource URI | Description |
|-------------|-------------|
| `context-mcp://repo_summary` | High-level summary: repo root, node/edge counts, active profile |
| `context-mcp://index_stats` | Detailed statistics: node counts by type, unique files, top files by node count |
| `context-mcp://changed_symbols` | Symbols in files changed since the last commit |
| `context-mcp://hot_paths` | Top 20 symbols by PageRank and betweenness centrality |

---

## Connecting to Claude Desktop

Add context-mcp to your Claude Desktop MCP configuration:

**macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "context-mcp": {
      "command": "/absolute/path/to/context-mcp",
      "args": ["-repo", "/absolute/path/to/your/project"]
    }
  }
}
```

Restart Claude Desktop after editing the config. All 17 tools will appear in Claude's tool list.

### Connecting to Claude Code

Claude Code supports `user`, `local`, and `project` scopes. The helper defaults to `user`; use `--scope local` or `--scope project` when needed.

**Helper install (user scope):**

```bash
./context-mcp install --client claude-code --repo /absolute/path/to/your/project --profile extended
```

> **Note:** The `install` helper only passes `--repo` and `--profile` flags to the binary. For ONNX-enabled installs, you must manually create `.mcp/config.json` with the full args including `-onnx-model` and `-onnx-lib`.

**Helper install (local scope):**

```bash
./context-mcp install --client claude-code --repo /absolute/path/to/your/project --scope local
```

**Project-scoped stdio config (`.mcp.json`):**

```json
{
  "mcpServers": {
    "context-mcp": {
      "command": "/absolute/path/to/context-mcp",
      "args": ["-repo", "/absolute/path/to/your/project", "-profile", "extended"]
    }
  }
}
```

**Project-scoped stdio config with ONNX embeddings (`.mcp/config.json`):**

```json
{
  "mcpServers": {
    "context-mcp": {
      "command": "/absolute/path/to/context-mcp",
      "args": [
        "-repo", "/absolute/path/to/your/project",
        "-profile", "extended",
        "-onnx-model", "/absolute/path/to/models/CodeRankEmbed-onnx-int8",
        "-onnx-lib", "/path/to/libonnxruntime.dylib"
      ]
    }
  }
}
```

**Project-scoped HTTP config (`.mcp.json`):**

```json
{
  "mcpServers": {
    "context-mcp": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

`doctor` checks user, local, and project-scoped Claude Code installs, including local-scope entries persisted in Claude's global `projects[...]` config.

### Connecting to Codex

The helper installs Codex config into `~/.codex/config.toml`. Codex also supports project-local `.codex/config.toml` if you prefer to keep the config in the repository.

**Helper install:**

```bash
./context-mcp install --client codex --repo /absolute/path/to/your/project --profile extended
```

**User-scoped stdio config (`~/.codex/config.toml` or `.codex/config.toml`):**

```toml
[mcp_servers.context-mcp]
command = "/absolute/path/to/context-mcp"
args = ["-repo", "/absolute/path/to/your/project", "-profile", "extended"]
```

**User-scoped HTTP config (`~/.codex/config.toml` or `.codex/config.toml`):**

```toml
[mcp_servers.context-mcp]
url = "http://127.0.0.1:8080/mcp"
```

---

## Manual Smoke Tests

These scripts are on-demand harness checks, not CI. They build a temporary `context-mcp` binary, create isolated MCP configs, run a small benchmark-style scenario set from [`benchmarks/mcp_smoke_tasks.json`](benchmarks/mcp_smoke_tasks.json), and write raw event logs plus `summary.json` to a temp directory.

**Claude Code (`--model haiku`):**

```bash
./scripts/smoke-claude-mcp.sh all /absolute/path/to/your/project
./scripts/smoke-claude-mcp.sh http /absolute/path/to/your/project /tmp/context-mcp-smoke-claude
```

By default the Claude smoke test uses `--output-format stream-json`, `--strict-mcp-config`, and exercises `context`, `assemble_context`, `read_symbol`, `checkpoint_context`, and `read_delta`.

**Codex (`-m gpt-5.4-mini`):**

```bash
./scripts/smoke-codex-mcp.sh all /absolute/path/to/your/project
./scripts/smoke-codex-mcp.sh stdio /absolute/path/to/your/project /tmp/context-mcp-smoke-codex
```

The Codex smoke test uses `codex exec --json` with an isolated temp MCP config override. Both scripts support `stdio`, `http`, or `all` as the first argument.

If the client CLI exposes usage or cost telemetry, the scripts record it in `summary.json`. Missing usage fields are treated as informational, not fatal.

For a publishable release artifact in [`benchmarks/results/`](/Users/naman/Documents/context-mcp/benchmarks/results), run [`benchmarks/run_mcp_usage.sh`](/Users/naman/Documents/context-mcp/benchmarks/run_mcp_usage.sh) against the canonical benchmark repo. By default it runs both `with MCP` and `without MCP` variants and emits both a JSON artifact and a Markdown comparison-table report.

---

## Configuration

All options are set via CLI flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-repo` | `.` (current directory) | Path to the repository root |
| `-db` | `.context-mcp/index.db` | Path to the SQLite database |
| `-debounce` | `500ms` | Filesystem event debounce interval |
| `-max-depth` | `5` | Default max BFS depth for impact analysis |
| `-batch-size` | `32` | Embedding batch size |
| `-workers` | `4` | Number of parallel file-parsing workers |
| `-onnx-model` | (empty) | Path to ONNX model directory (enables neural embeddings) |
| `-onnx-lib` | (empty) | Path to ONNX Runtime shared library |
| `-embedding-dim` | `384` | Embedding vector dimension. TF-IDF defaults to 384; CodeRankEmbed uses 768 |
| `-cold-start` | `true` | Enable Git-derived intent metadata ingestion |
| `-profile` | `core` | Tool profile for MCP mode: `core` (7 tools), `extended` (14), `full` (17) |
| `-ollama-endpoint` | (empty) | Ollama API endpoint (e.g., `http://localhost:11434`) |
| `-ollama-model` | `nomic-embed-code` | Ollama embedding model name |
| `-llamacpp-endpoint` | (empty) | llama.cpp server endpoint (e.g., `http://localhost:8080`) |
| `-openai-endpoint` | (empty) | OpenAI-compatible embeddings endpoint |
| `-openai-model` | `text-embedding-nomic-embed-code` | OpenAI-compatible embeddings model name |
| `-port` | `8080` | HTTP port for `serve-http` |
| `-bearer-token` | (empty) | Optional bearer token for HTTP auth |

**Example with custom settings:**

```bash
./context-mcp -repo ~/projects/my-app -workers 8 -debounce 1s
```

### Local Embedding Models

context-mcp supports three embedding backends beyond the built-in TF-IDF fallback. Priority order: ONNX > Ollama > llama.cpp > TF-IDF.

**Option 1: Pre-quantized ONNX model (recommended for best quality)**

Download the default pre-quantized CodeRankEmbed model (~490MB) with the provided script:

```bash
./scripts/download-model.sh
go build -tags "fts5 onnx" -o context-mcp ./cmd/context-mcp

# Explicit model path
./context-mcp -onnx-model models/CodeRankEmbed-onnx-int8 -onnx-lib /path/to/libonnxruntime.dylib

# If the model is at the default path, it is auto-detected
./context-mcp -onnx-lib /path/to/libonnxruntime.dylib
```

CodeRankEmbed uses 768 dimensions. Legacy Qwen2 or Jina ONNX exports are still supported, but you must set `-embedding-dim` to match the model you exported.

**Option 2: Ollama (easiest setup, no compilation needed)**

Install [Ollama](https://ollama.com), pull an embedding model, and point context-mcp at it:

```bash
ollama pull nomic-embed-code
./context-mcp -ollama-endpoint http://localhost:11434 -ollama-model nomic-embed-code -embedding-dim 768
```

Supported models include `nomic-embed-code` (768d, code-optimized), `nomic-embed-text` (768d), `mxbai-embed-large` (1024d), and `all-minilm` (384d).

**Option 3: llama.cpp server (GGUF models, native batching)**

Start `llama-server` with an embedding model, then point context-mcp at it:

```bash
llama-server -m nomic-embed-code-q8_0.gguf --embedding --port 8080
./context-mcp -llamacpp-endpoint http://localhost:8080 -embedding-dim 768
```

llama.cpp supports native batch embedding and GGUF quantized models (smaller than ONNX).

**Important:** The `-embedding-dim` flag must match the model's output dimension. Changing dimensions requires re-indexing.

### Switching Embedding Backends

When switching between embedding backends (e.g., TF-IDF to ONNX), the vector dimensions change (384 vs 768). The sqlite-vec table dimensions are set at creation time and cannot be altered.

**You must delete the old index and re-index:**

```bash
rm -f /path/to/repo/.context-mcp/index.db*
./context-mcp -repo /path/to/repo \
  -onnx-model /path/to/models/CodeRankEmbed-onnx-int8 \
  -onnx-lib /path/to/libonnxruntime.dylib \
  cli index '{}'
```

Without this step, reindexing will silently fail with "Dimension mismatch" errors on every symbol.

---

## How Indexing Works

1. **File discovery** -- Walks the repo, respecting `.gitignore` and excluded dirs (`.git`, `.context-mcp`)
2. **AST parsing** -- Extracts functions, classes, structs, and methods. Builds call/import/implements edges
3. **Storage** -- Upserts nodes and edges into SQLite with FTS5 full-text index
4. **Embeddings** -- Generates vector embeddings for each symbol (TF-IDF default; optional ONNX via `--onnx-model`)
5. **Graph build** -- Constructs a directed dependency graph in memory using gonum
6. **Centrality** -- Computes betweenness centrality (Brandes' algorithm) and stores scores
7. **ADR discovery** -- Finds and stores architecture documents (ARCHITECTURE.md, ADR.md, DESIGN.md, adr/ directories)
8. **Watch** -- Monitors filesystem for changes and incrementally re-indexes modified files

### Search Ranking

Queries are ranked using a multi-signal composite score:

```
score = 0.35 * PersonalizedPageRank
      + 0.30 * BM25 (FTS5)
      + 0.20 * Betweenness Centrality
      + 0.15 * Semantic Similarity
```

All signals are normalized to [0, 1] before weighting. FTS5 queries are enhanced with prefix matching, CamelCase splitting, and stop word filtering.

---

## Supported Languages

| Language | Parser | Accuracy |
|----------|--------|----------|
| Go | Native `go/ast` | High -- full AST walk |
| JavaScript | Tree-sitter + regex | High -- tree-sitter for declarations, regex for call extraction |
| TypeScript | Tree-sitter + regex | High -- tree-sitter for declarations, regex for call extraction |
| PHP | Tree-sitter + regex | High -- tree-sitter for declarations, regex for call extraction |

---

## Troubleshooting

**Build fails with "undefined: sqlite3"**
Ensure CGO is enabled: `CGO_ENABLED=1 go build -tags "fts5" -o context-mcp ./cmd/context-mcp`

**No search results**
Run `cli query '{"sql": "SELECT COUNT(*) FROM nodes"}'` to verify the index has data. If empty, check that your repo contains supported file types (.go, .js, .ts, .php).

**Database locked errors**
context-mcp uses WAL mode for concurrent reads. If another process holds the database, wait or delete the `.context-mcp/index.db` file and re-index.

**Large repos are slow to index**
Increase the worker count: `-workers 8`. The initial index is the slowest; subsequent file changes are indexed incrementally.

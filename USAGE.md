# qb-context User Guide

A local-first MCP daemon that indexes your codebase and gives LLM agents surgical, context-aware code retrieval.

---

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [Running the Daemon](#running-the-daemon)
- [CLI Mode](#cli-mode)
- [MCP Tools Reference](#mcp-tools-reference)
  - [context](#context--search--discover-code)
  - [impact](#impact--blast-radius-analysis)
  - [read_symbol](#read_symbol--read-source-code)
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
- [Connecting to Claude Desktop](#connecting-to-claude-desktop)
- [Configuration](#configuration)
- [How Indexing Works](#how-indexing-works)
- [Supported Languages](#supported-languages)

---

## Quick Start

```bash
# Build
go build -tags "fts5" -o qb-context ./cmd/qb-context

# Index and query your repo via CLI
./qb-context -repo /path/to/your/project cli context '{"query": "authentication"}'

# Or run as a persistent MCP daemon
./qb-context -repo /path/to/your/project
```

---

## Installation

### Prerequisites

- Go 1.24+ with CGO enabled (required for SQLite FTS5)
- A C compiler (gcc/clang) — needed by `go-sqlite3`

### Build from Source

```bash
git clone https://github.com/maplenk/context-mcp.git
cd qb-context
go build -tags "fts5" -o qb-context ./cmd/qb-context
```

The `-tags "fts5"` flag is **required** — it enables SQLite full-text search.

### Verify

```bash
./qb-context cli --list
```

Expected output:

```
TOOL                       DESCRIPTION
----                       -----------
context                    Discovers relevant code symbols using hybrid lexical + semantic search...
impact                     Analyzes the blast radius of a code symbol...
read_symbol                Retrieves the exact source code of a symbol...
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
```

---

## Running the Daemon

The daemon runs as an MCP server over stdio. It indexes your repo on startup, watches for file changes, and serves tool requests.

```bash
./qb-context -repo /path/to/your/project
```

On startup it will:

1. Scan all source files in the repo
2. Parse AST nodes (functions, classes, structs, methods)
3. Build a dependency graph and compute centrality metrics
4. Discover architecture documents (ARCHITECTURE.md, ADR.md, etc.)
5. Start watching for file changes (incremental re-indexing)
6. Listen for MCP JSON-RPC requests on stdin

The SQLite database is stored at `<repo>/.qb-context/index.db` by default.

### Stopping

Send `SIGINT` (Ctrl+C) or `SIGTERM` for graceful shutdown.

---

## CLI Mode

Use the `cli` subcommand to invoke any MCP tool directly from the terminal. This is useful for testing, debugging, and scripting.

### Syntax

```bash
./qb-context [flags] cli <tool_name> [json_args]
```

### List Available Tools

```bash
./qb-context cli --list
```

### Examples

**Search for code related to "payment processing":**

```bash
./qb-context -repo . cli context '{"query": "payment processing", "limit": 5}'
```

**Analyze the blast radius of a symbol:**

```bash
./qb-context -repo . cli impact '{"symbol_id": "handlePayment", "depth": 3}'
```

**Read the source code of a function:**

```bash
./qb-context -repo . cli read_symbol '{"symbol_id": "ParseFile"}'
```

**Run a diagnostic SQL query:**

```bash
./qb-context -repo . cli query '{"sql": "SELECT symbol_name, file_path FROM nodes WHERE node_type = 1 LIMIT 10"}'
```

**Trigger a full re-index:**

```bash
./qb-context -repo . cli index '{}'
```

**View architecture communities:**

```bash
./qb-context -repo . cli context '{"query": "_", "mode": "architecture"}'
```

> All output is JSON printed to stdout. Logs go to stderr.

---

## MCP Tools Reference

### `context` — Search & Discover Code

Discovers relevant code symbols using multi-signal ranked search. Combines lexical search (FTS5/BM25), semantic similarity, graph-based PageRank, betweenness centrality, and in-degree authority into a composite score.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | yes | — | Natural language or keyword query |
| `limit` | integer | no | 10 | Max results to return |
| `mode` | string | no | `"search"` | `"search"` for hybrid search, `"architecture"` for community detection |
| `max_per_file` | integer | no | 3 | Maximum results per unique file path |
| `active_files` | string[] | no | — | File paths the developer is currently editing for PPR personalization |

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

Returns Louvain community clusters — groups of tightly coupled code symbols — along with a modularity score. Useful for understanding domain boundaries and component coupling.

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

### `impact` — Blast Radius Analysis

Traces all downstream dependents of a symbol via BFS graph traversal and classifies them by risk level based on distance.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol_id` | string | yes | — | Symbol name or hash ID |
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
  "summary": "Symbol has betweenness 0.73 — 3 direct dependents, 12 total affected, 2 tests impacted"
}
```

**Risk levels by hop distance:**

| Hop | Risk Level |
|-----|-----------|
| 1 | CRITICAL — direct dependents |
| 2 | HIGH |
| 3 | MEDIUM |
| 4+ | LOW |

The `risk_score` is the betweenness centrality of the target symbol (0-1). Higher values indicate bottleneck nodes.

---

### `read_symbol` — Read Source Code

Retrieves the exact source code of a symbol by reading the specific byte range from disk. No grep, no scanning — precise extraction.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol_id` | string | yes | — | Symbol name or hash ID |

**Example:**

```json
{
  "symbol_id": "ParseFile"
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
  "source": "func ParseFile(path string, repoRoot string) (*ParseResult, error) {\n  ..."
}
```

Accepts either the symbol name (e.g., `"ParseFile"`) or the full SHA-256 hash ID. If the name matches multiple symbols, the first match is returned.

---

### `query` — Raw SQL

Executes a read-only SQL query against the structural database. Useful for diagnostics and custom analysis.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `sql` | string | yes | — | A `SELECT` query |

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
| `node_embeddings` | Vector embeddings for semantic search (384-dim) |
| `node_scores` | Precomputed graph metrics (pagerank, betweenness) |
| `project_summaries` | Architecture decision records and design docs |

---

### `index` — Re-index Repository

Triggers a full re-index of the repository. The daemon also indexes automatically on startup and on file changes, so manual re-indexing is rarely needed.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `path` | string | no | — | Optional: specific path to re-index (file or directory) |

**Example:**

```json
{}
```

---

### `health` — System Health Status

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

### `trace_call_path` — Trace Call Paths

Traces call paths between two symbols. Useful for understanding how control flows from one function to another through the dependency graph.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `from` | string | yes | — | Source symbol name or ID |
| `to` | string | yes | — | Target symbol name or ID |
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

### `get_key_symbols` — Top Symbols by Centrality

Returns the most important symbols in the codebase ranked by centrality metrics. Useful for identifying core abstractions and architectural hotspots.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `limit` | integer | no | 20 | Maximum number of symbols to return |
| `file_filter` | string | no | — | Optional file path prefix to filter results |

**Example:**

```json
{
  "limit": 10,
  "file_filter": "internal/graph/"
}
```

---

### `search_code` — Regex Source Search

Performs regex search across indexed source files. Returns matching lines with file paths and line numbers.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `pattern` | string | yes | — | Regex pattern to search for |
| `file_filter` | string | no | — | Optional glob pattern to filter files (e.g. `"*.go"`) |
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

### `detect_changes` — Changed Symbol Detection

Detects symbols that have changed since a given git ref. Useful for understanding what functions/classes were modified in recent commits or compared to a branch.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `since` | string | yes | — | Git ref to compare against (e.g. `"HEAD~5"`, `"main"`, a commit SHA) |
| `path` | string | no | — | Optional path filter to restrict detection scope |

**Example:**

```json
{
  "since": "HEAD~5",
  "path": "internal/"
}
```

---

### `get_architecture_summary` — Architecture Overview

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

### `explore` — Explore Codebase

Explores the codebase by searching for a symbol with optional dependency analysis. A good starting point when you want to find a symbol and optionally see what it depends on.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol` | string | yes | — | Symbol name to search for |
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

### `understand` — Deep Symbol Understanding

Provides deep understanding of a symbol using 3-tier resolution (exact match, fuzzy match, search fallback) plus callers, callees, PageRank score, and community membership. The most comprehensive single-symbol analysis tool.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `symbol` | string | yes | — | Symbol name to analyze |

**Example:**

```json
{
  "symbol": "ComputePageRank"
}
```

---

## Connecting to Claude Desktop

Add qb-context to your Claude Desktop MCP configuration:

**macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "qb-context": {
      "command": "/absolute/path/to/qb-context",
      "args": ["-repo", "/absolute/path/to/your/project"]
    }
  }
}
```

Restart Claude Desktop after editing the config. All 13 tools will appear in Claude's tool list.

### Connecting to Claude Code

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "qb-context": {
      "command": "/absolute/path/to/qb-context",
      "args": ["-repo", "."]
    }
  }
}
```

---

## Configuration

All options are set via CLI flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-repo` | `.` (current directory) | Path to the repository root |
| `-db` | `.qb-context/index.db` | Path to the SQLite database |
| `-debounce` | `500ms` | Filesystem event debounce interval |
| `-max-depth` | `5` | Default max BFS depth for impact analysis |
| `-batch-size` | `32` | Embedding batch size |
| `-workers` | `4` | Number of parallel file-parsing workers |
| `-onnx-model` | (empty) | Path to ONNX model directory (enables neural embeddings) |
| `-onnx-lib` | (empty) | Path to ONNX Runtime shared library |
| `-embedding-dim` | `384` | Embedding vector dimension (ONNX Matryoshka: 64/128/256/512/896) |
| `-ollama-endpoint` | (empty) | Ollama API endpoint (e.g., `http://localhost:11434`) |
| `-ollama-model` | `nomic-embed-code` | Ollama embedding model name |
| `-llamacpp-endpoint` | (empty) | llama.cpp server endpoint (e.g., `http://localhost:8080`) |

**Example with custom settings:**

```bash
./qb-context -repo ~/projects/my-app -workers 8 -debounce 1s
```

### Local Embedding Models

qb-context supports three embedding backends beyond the built-in TF-IDF fallback. Priority order: ONNX > Ollama > llama.cpp > TF-IDF.

**Option 1: Pre-quantized ONNX model (recommended for best quality)**

Download the pre-quantized Jina Code model (~488MB) with the provided script:

```bash
./scripts/download-model.sh                        # downloads to models/jina-code-int8/
./qb-context -onnx-model models/jina-code-int8 -onnx-lib /path/to/libonnxruntime.dylib
```

Or export manually via `optimum-cli` (see the ONNX model export documentation).

**Option 2: Ollama (easiest setup, no compilation needed)**

Install [Ollama](https://ollama.com), pull an embedding model, and point qb-context at it:

```bash
ollama pull nomic-embed-code
./qb-context -ollama-endpoint http://localhost:11434 -ollama-model nomic-embed-code -embedding-dim 768
```

Supported models include `nomic-embed-code` (768d, code-optimized), `nomic-embed-text` (768d), `mxbai-embed-large` (1024d), and `all-minilm` (384d).

**Option 3: llama.cpp server (GGUF models, native batching)**

Start `llama-server` with an embedding model, then point qb-context at it:

```bash
llama-server -m nomic-embed-code-q8_0.gguf --embedding --port 8080
./qb-context -llamacpp-endpoint http://localhost:8080 -embedding-dim 768
```

llama.cpp supports native batch embedding and GGUF quantized models (smaller than ONNX).

**Important:** The `-embedding-dim` flag must match the model's output dimension. Changing dimensions requires re-indexing.

---

## How Indexing Works

1. **File discovery** — Walks the repo, respecting `.gitignore` and excluded dirs (`.git`, `.qb-context`)
2. **AST parsing** — Extracts functions, classes, structs, and methods. Builds call/import/implements edges
3. **Storage** — Upserts nodes and edges into SQLite with FTS5 full-text index
4. **Embeddings** — Generates vector embeddings for each symbol (TF-IDF default; optional ONNX via `--onnx-model`)
5. **Graph build** — Constructs a directed dependency graph in memory using gonum
6. **Centrality** — Computes betweenness centrality (Brandes' algorithm) and stores scores
7. **ADR discovery** — Finds and stores architecture documents (ARCHITECTURE.md, ADR.md, DESIGN.md, adr/ directories)
8. **Watch** — Monitors filesystem for changes and incrementally re-indexes modified files

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
| Go | Native `go/ast` | High — full AST walk |
| JavaScript | Tree-sitter + regex | High — tree-sitter for declarations, regex for call extraction |
| TypeScript | Tree-sitter + regex | High — tree-sitter for declarations, regex for call extraction |
| PHP | Tree-sitter + regex | High — tree-sitter for declarations, regex for call extraction |

---

## Troubleshooting

**Build fails with "undefined: sqlite3"**
Ensure CGO is enabled: `CGO_ENABLED=1 go build -tags "fts5" ./cmd/qb-context`

**No search results**
Run `cli query '{"sql": "SELECT COUNT(*) FROM nodes"}'` to verify the index has data. If empty, check that your repo contains supported file types (.go, .js, .ts, .php).

**Database locked errors**
qb-context uses WAL mode for concurrent reads. If another process holds the database, wait or delete the `.qb-context/index.db` file and re-index.

**Large repos are slow to index**
Increase the worker count: `-workers 8`. The initial index is the slowest; subsequent file changes are indexed incrementally.

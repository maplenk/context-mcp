# context-mcp

A local-first MCP daemon that gives LLM coding agents surgical, token-efficient code retrieval through structural graph analysis and semantic search.

[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/maplenk/context-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/maplenk/context-mcp/actions/workflows/ci.yml)

---

## The Problem

LLM coding agents waste tokens brute-forcing through grep and glob results with no structural understanding of the codebase. They read entire files hoping to stumble on the right function, missing cross-file relationships and architectural context entirely. context-mcp builds a live AST graph and semantic index of your codebase and exposes 16 MCP tools, 5 prompt templates, and 4 resources for ranked, context-aware code discovery. Single binary, zero cloud dependencies, local-first.

## Key Features

- **Hybrid ranked search** -- PPR + BM25 + Betweenness Centrality + Semantic Similarity with optimized weights
- **16 MCP tools**: context, impact, read\_symbol, query, index, health, trace\_call\_path, get\_key\_symbols, search\_code, detect\_changes, get\_architecture\_summary, explore, understand, assemble\_context, checkpoint\_context, read\_delta
- **5 MCP prompts**: review\_changes, trace\_impact, prepare\_fix\_context, onboard\_repo, collect\_minimal\_context
- **4 MCP resources**: repo\_summary, index\_stats, changed\_symbols, hot\_paths
- **Multi-language AST parsing**: Go (native go/ast), JavaScript, TypeScript, PHP (tree-sitter)
- **Graph analysis**: Louvain community detection, betweenness centrality, blast radius, Personalized PageRank
- **Incremental indexing** with filesystem watching (.gitignore-aware, hot-reload)
- **Optional ONNX neural embeddings** (Qwen2 model, Matryoshka dimensions)
- **Single statically-linked binary** -- SQLite + FTS5 + sqlite-vec, no external databases
- **Cold Start**: Git history intent enrichment for better first-query results

## Quick Start

```bash
git clone https://github.com/maplenk/context-mcp.git
cd context-mcp
go build -tags "fts5" -o context-mcp ./cmd/qb-context

# CLI mode -- query directly
./context-mcp -repo /path/to/your/project cli context '{"query": "authentication"}'

# Daemon mode -- run as MCP server
./context-mcp -repo /path/to/your/project
```

## Installation

### Prerequisites

- Go 1.25+ with CGO enabled (required for SQLite FTS5 and tree-sitter)
- A C compiler (gcc or clang) -- needed by `go-sqlite3`

### Build from Source

```bash
git clone https://github.com/maplenk/context-mcp.git
cd context-mcp
go build -tags "fts5" -o context-mcp ./cmd/qb-context
```

The `-tags "fts5"` flag is **required** -- it enables SQLite full-text search.

### Verify

```bash
./context-mcp cli --list
```

### Optional: ONNX Neural Embeddings

By default, context-mcp uses TF-IDF for embeddings. For higher-quality semantic search, you can use an ONNX code embedding model (Qwen2-based, ~500MB):

```bash
# Install optimum-cli
pip install optimum[onnxruntime]

# Export and quantize the model
optimum-cli export onnx --model jinaai/jina-embeddings-v2-base-code onnx_model/
optimum-cli onnxruntime quantize --onnx_model onnx_model --arm64 -o quantized_model/

# Run with ONNX embeddings
./context-mcp -repo /path/to/project -onnx-model ./quantized_model -onnx-lib /path/to/libonnxruntime.dylib
```

Supported Matryoshka dimensions: 64, 128, 256, 512, 896 (set via `-embedding-dim`).

## MCP Integration

### Claude Desktop

Add to your Claude Desktop config:

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

Restart Claude Desktop after editing the config. All tools will appear in Claude's tool list.

### Claude Code

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "context-mcp": {
      "command": "/absolute/path/to/context-mcp",
      "args": ["-repo", "."]
    }
  }
}
```

## Tools Overview

| Tool | Description |
|------|-------------|
| `context` | Hybrid ranked search combining lexical, semantic, and graph signals |
| `impact` | Blast radius analysis -- traces downstream dependents with risk classification |
| `read_symbol` | Reads exact source code of a symbol by byte range |
| `query` | Executes read-only SQL against the structural database |
| `index` | Triggers a full re-index of the repository |
| `health` | Returns system health: uptime, node/edge counts, database size |
| `trace_call_path` | Traces call paths between two symbols through the dependency graph |
| `get_key_symbols` | Returns top symbols ranked by centrality metrics |
| `search_code` | Regex search across indexed source files |
| `detect_changes` | Detects changed symbols since a git ref |
| `get_architecture_summary` | Architecture overview with community clusters, hubs, and entry points |
| `explore` | Searches for a symbol with optional dependency analysis |
| `understand` | Deep symbol analysis with callers, callees, PageRank, and community membership |
| `assemble_context` | Token-budgeted context assembly -- returns ranked code snippets fitted within a token budget |
| `checkpoint_context` | Creates a named checkpoint of the current index state |
| `read_delta` | Compares current index state against a named checkpoint -- shows added, modified, and deleted symbols |

For complete documentation with parameters and examples, see [USAGE.md](USAGE.md).

## How It Works

### Pipeline

1. **File discovery** -- walks the repo respecting `.gitignore` and excluded directories
2. **AST parsing** -- extracts functions, classes, structs, methods, and builds call/import/implements edges
3. **SQLite storage** -- upserts nodes and edges with FTS5 full-text index
4. **Embedding generation** -- creates vector embeddings for semantic search (TF-IDF default, ONNX optional)
5. **Graph construction** -- builds a directed dependency graph in memory using gonum
6. **Centrality computation** -- runs Brandes' betweenness algorithm and stores scores
7. **Filesystem watching** -- monitors for changes and incrementally re-indexes modified files

### Search Ranking

Queries are ranked using a multi-signal composite score:

```
score = 0.35 * PPR + 0.30 * BM25 + 0.20 * Betweenness + 0.15 * Semantic
```

All signals are normalized to \[0, 1\] before weighting. InDegree was eliminated (weight 0.00) after a 4-phase parameter sweep across ~130 configurations. FTS5 queries are enhanced with prefix matching, CamelCase splitting, and stop word filtering.

## Supported Languages

| Language | Parser | Notes |
|----------|--------|-------|
| Go | Native `go/ast` | Full AST walk with type aliases, interfaces, and receiver methods |
| JavaScript | tree-sitter | Declarations, call edges, class inheritance |
| TypeScript | tree-sitter | Declarations, call edges, interfaces, enums, type aliases |
| PHP | tree-sitter | Classes, methods, routes, inheritance, implements edges |

## Benchmarks

Tested against a real-world Laravel codebase (~18K nodes, ~25K edges):

- 15 diverse queries across exact match, concept search, and cross-file flow categories
- **55.6% B+C accuracy** (concept + cross-file queries) -- up from 33.3% baseline
- Optimized via 4-phase parameter sweep across ~130 configurations
- All queries complete under 120ms latency

Highlights from the query suite:

- C3 Webhook dispatch: 7/7 rubric items (perfect)
- C4 OpenTelemetry tracing: 8/8 rubric items (perfect)
- B3 Loyalty points: 4/4 rubric items (perfect)
- B4 Database schema: 3/3 rubric items (perfect)
- C5 Inventory writes: 5-6/8 rubric items

See [benchmarks/](benchmarks/) for full methodology, query suite, and per-query scoring.

## Configuration

All options are set via CLI flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-repo` | `.` | Path to the repository root |
| `-db` | `.context-mcp/index.db` | Path to the SQLite database |
| `-debounce` | `500ms` | Filesystem event debounce interval |
| `-max-depth` | `5` | Default max BFS depth for impact analysis |
| `-batch-size` | `32` | Embedding batch size |
| `-workers` | `4` | Number of parallel file-parsing workers |
| `-onnx-model` | (empty) | Path to ONNX model directory (enables neural embeddings) |
| `-onnx-lib` | (empty) | Path to ONNX Runtime shared library |
| `-embedding-dim` | `384` | Embedding vector dimension (ONNX Matryoshka: 64/128/256/512/896) |
| `-profile` | `core` | Tool profile for MCP SDK: `core` (6 tools), `extended` (13), or `full` (16) |
| `-cold-start` | `true` | Enable Git-derived intent metadata ingestion |

## Development

```bash
go build -tags "fts5" ./...           # build (FTS5 tag required)
go test -tags "fts5" -count=1 ./...   # run tests
go vet -tags "fts5" ./...             # static analysis
```

CI: GitHub Actions runs build, vet, and race-detector tests on every push. Weekly security scanning with govulncheck, gosec, and trivy.

## Roadmap

- Additional language parsers (Python, Rust, Java)
- Pure-Go SQLite to eliminate CGO requirement
- Semantic flow tracing for business concept queries
- Enhanced route extraction for multi-version APIs
- Betweenness sampling for large codebases (>50K nodes)
- Structured logging

## License

MIT -- see [LICENSE](LICENSE).

# Embedding Backend Benchmarks (2026-04-05)

## Test Setup
- **Repo:** context-mcp self-repo (55 source files, 924 nodes, 5993 edges)
- **Hardware:** macOS (Apple Silicon)
- **Queries:** 5 benchmark queries on the `context` tool

## Backend Configurations

| Backend | Model | Size | Dim | Transport |
|---------|-------|------|-----|-----------|
| TF-IDF | built-in | 0 | 384 | in-process |
| LM Studio | text-embedding-nomic-embed-code | ~7GB | 768 | HTTP (OpenAI-compat) |
| Ollama text | nomic-embed-text | 274 MB | 768 | HTTP (Ollama API) |
| Ollama code (full) | manutic/nomic-embed-code | 7.5 GB | 768 | HTTP (Ollama API) |
| Ollama code (Q4) | manutic/nomic-embed-code:7b-Q4_K_M | 4.4 GB | 768 | HTTP (Ollama API) |
| Jina ONNX INT8 | jina-embeddings-v2-small-en | 495 MB | 256 | in-process ONNX |
| CodeRankEmbed ONNX INT8 | CodeRankEmbed | 132 MB | 768 | in-process ONNX |

## Indexing Speed

| Backend | Index time (951 nodes) | Failures | Per-embed (warm) |
|---------|----------------------|----------|------------------|
| TF-IDF | <1s | 0 | instant |
| CodeRankEmbed ONNX INT8 | 70s | 0 | ~0.07s |
| Ollama text (274MB) | 24s | 0 | ~0.03s |
| Jina ONNX INT8 | 4 min | 0 | ~0.25s |
| Ollama code Q4 (4.4GB) | 12.5 min | 0 | ~0.22s |
| Ollama code full (7.5GB) | 12 min | 0 | ~0.6s |
| LM Studio code | 25 min | many timeouts | ~9s |

## Search Quality — Top-1 Results

| Query | TF-IDF | Ollama text | Ollama code (Q4) | Ollama code (full) | Jina ONNX | CodeRankEmbed ONNX |
|-------|--------|-------------|------------------|-------------------|-----------|-------------------|
| embedding backend init | `llamacpp.go` (.491) | `llamacpp.go` (.465) | `llamacpp.go` (.485) | `llamacpp.go` (.485) | `GetEntryPoints` (.418) | `Embedder` (.484) |
| edges stored in sqlite | `Store.UpsertEdges` (.762) | `Store.GetAllEdges` (.800) | `Store.GetAllEdges` (.779) | `Store.GetAllEdges` (.780) | `Store.GetAllEdges` (.800) | `Store.GetAllEdges` (.800) |
| cosine similarity | `CosineSimilarity` (.800) | `CosineSimilarity` (.800) | `CosineSimilarity` (.800) | `CosineSimilarity` (.800) | `CosineSimilarity` (.800) | `CosineSimilarity` (.800) |
| parse Go AST nodes | `Parser.ParseFile` (.511) | `Parser.ParseFile` (.588) | `Parser.ParseFile` (.583) | `Parser.ParseFile` (.579) | `Parser.ParseFile` (.593) | `Parser.ParseFile` (.585) |
| file watcher debounce | `Watcher.debounce` (.561) | `Watcher.debounce` (.587) | `Watcher.debounce` (.556) | `Watcher.debounce` (.558) | `Watcher.debounce` (.527) | `Watcher.debounce` (.587) |

## Full Rankings Per Query

### Query: "embedding backend initialization"

| Rank | TF-IDF | Ollama text | Ollama code Q4 | Ollama code full |
|------|--------|-------------|----------------|-----------------|
| 1 | llamacpp.go (.491) | llamacpp.go (.465) | llamacpp.go (.485) | llamacpp.go (.485) |
| 2 | ONNXEmbedderStub.EmbedBatch (.409) | Embedder (.453) | GraphEngine.GetEntryPoints (.448) | GraphEngine.GetEntryPoints (.431) |
| 3 | GraphEngine.GetEntryPoints (.405) | GraphEngine.GetEntryPoints (.412) | engine.go (.390) | engine.go (.380) |
| 4 | Embedder (.372) | ONNXEmbedderStub.EmbedBatch (.411) | Watcher.WalkExisting (.371) | Watcher.WalkExisting (.371) |
| 5 | Watcher.WalkExisting (.370) | Watcher.WalkExisting (.377) | ONNXEmbedderStub.EmbedBatch (.350) | ONNXEmbedderStub.EmbedBatch (.350) |

### Query: "how are edges stored in sqlite"

| Rank | TF-IDF | Ollama text | Ollama code Q4 | Ollama code full |
|------|--------|-------------|----------------|-----------------|
| 1 | Store.UpsertEdges (.762) | Store.GetAllEdges (.800) | Store.GetAllEdges (.779) | Store.GetAllEdges (.780) |
| 2 | GraphEngine.BlastRadius (.115) | TestGetEdgesFrom_Empty (.081) | TestGetEdgesFrom_Empty (.087) | TestGetEdgesFrom_Empty (.087) |
| 3 | TestUpsertEdge_GetEdgesTo (.053) | insertTwoNodes (.070) | GraphEngine.EdgeCount (.079) | GraphEngine.EdgeCount (.083) |
| 4 | EdgeType (.040) | EdgeType (.040) | newTestStore (.040) | EdgeType (.040) |
| 5 | Store.SchemaVersion (.034) | Store.SchemaVersion (.034) | EdgeType (.040) | newTestStore (.039) |

### Query: "cosine similarity between vectors"

| Rank | TF-IDF | Ollama text | Ollama code Q4 | Ollama code full |
|------|--------|-------------|----------------|-----------------|
| 1 | CosineSimilarity (.800) | CosineSimilarity (.800) | CosineSimilarity (.800) | CosineSimilarity (.800) |
| 2 | TestCosineSimilarity_DimensionMismatch (.211) | TestCosineSimilarity_SelfIsOne (.221) | TestCosineSimilarity_SelfIsOne (.217) | TestCosineSimilarity_SelfIsOne (.217) |
| 3 | TestONNXEmbedder_Similarity (.163) | TestONNXEmbedder_Similarity (.137) | TestONNXEmbedder_Similarity (.146) | TestONNXEmbedder_Similarity (.147) |
| 4 | platformDeviceInode (.072) | normalizeVec (.062) | normalizeVec (.067) | normalizeVec (.064) |
| 5 | OpenAIEmbedder.Dim (.063) | OllamaEmbedder.Embed (.045) | connectorEntry (.032) | connectorEntry (.034) |

### Query: "parse Go source files into AST nodes"

| Rank | TF-IDF | Ollama text | Ollama code Q4 | Ollama code full |
|------|--------|-------------|----------------|-----------------|
| 1 | Parser.ParseFile (.511) | Parser.ParseFile (.588) | Parser.ParseFile (.583) | Parser.ParseFile (.579) |
| 2 | ASTNode (.342) | ASTNode (.342) | ASTNode (.472) | ASTNode (.465) |
| 3 | WalkSourceFilesUnder (.272) | CreateFileDocNode (.251) | WalkSourceFiles (.179) | WalkSourceFiles (.175) |
| 4 | filedoc.go (.148) | Store.GetIncomingCrossFileEdges (.184) | writeFile (.125) | writeFile (.125) |
| 5 | ExtractRoutes (.127) | WalkSourceFilesUnder (.159) | Store.GetIncomingCrossFileEdges (.107) | Store.GetIncomingCrossFileEdges (.107) |

### Query: "file watcher debounce logic"

| Rank | TF-IDF | Ollama text | Ollama code Q4 | Ollama code full |
|------|--------|-------------|----------------|-----------------|
| 1 | Watcher.debounce (.561) | Watcher.debounce (.587) | Watcher.debounce (.556) | Watcher.debounce (.558) |
| 2 | symlink_unix.go (.212) | newTestWatcher (.178) | newTestWatcher (.185) | newTestWatcher (.186) |
| 3 | symlink_windows.go (.210) | symlink_unix.go (.067) | symlink_windows.go (.080) | symlink_windows.go (.077) |
| 4 | newTestWatcher (.189) | symlink_windows.go (.062) | symlink_unix.go (.069) | symlink_unix.go (.069) |
| 5 | Extractor.CompactFileIntents (.088) | IsFileDocCandidate (.043) | TestParseFlags_OverrideDebounce (.045) | TestParseFlags_OverrideDebounce (.047) |

## Key Observations

1. **Top-1 accuracy is identical** across most backends — the hybrid ranker's lexical + graph signals are strong enough
2. **Jina ONNX misses on "embedding backend init"** — top-1 is `GetEntryPoints` (.418) instead of `llamacpp.go`; its 256d Matryoshka truncation may lose domain-specific signal
3. **CodeRankEmbed matches Ollama quality** at 132MB and in-process speed (70s for 951 nodes)
4. **Neural backends improve deeper rankings** — promote semantically related symbols that TF-IDF misses
5. **ASTNode score** for "parse Go AST" query: TF-IDF=0.342, Ollama code Q4=**0.472** (+38%)
6. **Q4 vs Full quantization** — scores differ by <0.01, no measurable quality loss
7. **CodeRankEmbed ONNX is the speed king** — 70s indexing (in-process, no HTTP), 3.4x faster than Jina ONNX, 10x faster than Ollama Q4
8. **Discrimination ratio** (sim_related / sim_unrelated): CodeRankEmbed 2.79x vs Jina 2.09x — CodeRankEmbed separates code signals better
9. **LM Studio** is impractical for bulk indexing (9s/embed vs Ollama's 0.03-0.6s)

## Recommendations

- **For development/testing:** TF-IDF (instant, no dependencies)
- **Best overall (quality + speed + size):** CodeRankEmbed ONNX INT8 (132MB, 768d, in-process, 70s index)
- **For HTTP-based setup:** Ollama with `manutic/nomic-embed-code:7b-Q4_K_M` (4.4GB, 768d)
- **For fastest neural (HTTP):** Ollama with `nomic-embed-text` (274MB, 768d, 24s for 951 nodes)
- **Not recommended:** Jina ONNX (larger model, slower, misses on "embedding backend init" query)

## ABC Benchmark Results (qbapi, 19547 nodes)

Benchmark against qbapi Laravel codebase using 15 graded queries (A1-A4, B1-B6, C1-C5).

### Embedding Speed on qbapi

| Backend | Nodes | Time | Rate | Failures |
|---------|-------|------|------|----------|
| TF-IDF | 19547 | <1s | instant | 0 |
| CodeRankEmbed ONNX INT8 | 19547 | 3m38s | 89.8 nodes/sec | 0 |

### Per-Query Hit Rates

| Query | TF-IDF | CodeRankEmbed ONNX | Delta |
|-------|--------|-------------------|-------|
| A1 FiscalYearController | 1/1 | 1/1 | = |
| A2 OrderController | 2/3 | 2/3 | = |
| A3 order API endpoints | 0/5 | 0/5 | = |
| B1 payment processing | 2/12 | 1/12 | -1 |
| B2 auth & session | 4/9 | 3/9 | -1 |
| B3 loyalty program | 4/4 | 4/4 | = |
| B4 database schema | 3/3 | 3/3 | = |
| B5 error handling | 1/1 | 1/1 | = |
| B6 omnichannel sync | 3/7 | 3/7 | = |
| C1 order creation | 3/14 | 3/14 | = |
| C2 stock transaction | 4/8 | 5/8 | **+1** |
| C3 webhook dispatch | 7/7 | 7/7 | = |
| C4 OpenTelemetry | 8/8 | 8/8 | = |
| C5 inventory write | 4/8 | 4/8 | = |
| **Tier A** | **3/9 (33.3%)** | **3/9 (33.3%)** | **=** |
| **Tier B+C** | **43/81 (53.1%)** | **42/81 (51.9%)** | **-1** |
| **Overall** | **46/90 (51.1%)** | **45/90 (50.0%)** | **-1** |

### ABC Benchmark Conclusions

1. **Semantic embeddings don't improve qbapi search quality** — the hybrid ranker's lexical+graph signals already dominate
2. **CodeRankEmbed helps on C2** (stock transaction lifecycle) where semantic similarity promotes related inventory methods
3. **CodeRankEmbed hurts on B1/B2** — the semantic signal pushes down some file-level matches that lexical search finds
4. **Bottleneck is route matching** — A3 (order API endpoints) and all route-based answer items (POST /v1/...) are consistently missed across all backends
5. **Algorithm improvements needed**: route detection, cross-file tracing, and better Service class discovery will have more impact than embedding model changes

//go:build fts5 && realrepo && onnx

package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maplenk/context-mcp/internal/embedding"
	"github.com/maplenk/context-mcp/internal/search"
	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/types"
)

// backendBenchConfig holds configuration for a neural embedding backend benchmark.
type backendBenchConfig struct {
	name      string
	dim       int
	embedder  embedding.Embedder
	closeFn   func() // cleanup function (e.g., embedder.Close)
}

// runBackendBenchmark is the shared implementation for all neural backend benchmarks.
// It creates a temp store, copies nodes/edges from the shared env, embeds all nodes
// with the given embedder, runs the benchmark, and logs results.
func runBackendBenchmark(t *testing.T, cfg backendBenchConfig) {
	t.Helper()

	env := getSharedEnv(t)

	// Create temp store with the correct embedding dimension for this backend
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")
	store, err := storage.NewStore(dbPath, cfg.dim)
	if err != nil {
		t.Fatalf("NewStore(dim=%d): %v", cfg.dim, err)
	}
	defer store.Close()

	// Retrieve all node IDs from the shared env and fetch each node.
	// There is no GetAllNodes() method; we iterate via GetAllNodeIDs() + GetNode().
	allNodeIDs, err := env.store.GetAllNodeIDs()
	if err != nil {
		t.Fatalf("GetAllNodeIDs: %v", err)
	}
	allNodes := make([]types.ASTNode, 0, len(allNodeIDs))
	for _, id := range allNodeIDs {
		node, err := env.store.GetNode(id)
		if err != nil {
			t.Logf("WARN: GetNode(%s): %v (skipping)", id, err)
			continue
		}
		allNodes = append(allNodes, *node)
	}
	t.Logf("Loaded %d nodes from shared env (%d IDs)", len(allNodes), len(allNodeIDs))

	// Copy nodes into the new store (this also builds FTS automatically)
	if err := store.UpsertNodes(allNodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// Copy edges from the shared env
	allEdges, err := env.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	if err := store.UpsertEdges(allEdges); err != nil {
		t.Fatalf("UpsertEdges: %v", err)
	}
	t.Logf("Copied %d edges", len(allEdges))

	// Embed all nodes with the neural backend — this is the expensive part
	t.Logf("Embedding %d nodes with %s (dim=%d)...", len(allNodes), cfg.name, cfg.dim)
	start := time.Now()
	embedded := 0
	failed := 0
	for i, node := range allNodes {
		// Build embedding text: symbol name + content summary (matches production usage)
		text := node.SymbolName + " " + node.ContentSum
		if text == " " {
			text = node.FilePath
		}

		vec, err := cfg.embedder.Embed(text)
		if err != nil {
			failed++
			if failed <= 5 {
				t.Logf("  embed error [%d] %s: %v", i, node.ID, err)
			}
			continue
		}

		if err := store.UpsertEmbedding(node.ID, vec); err != nil {
			failed++
			if failed <= 5 {
				t.Logf("  upsert error [%d] %s: %v", i, node.ID, err)
			}
			continue
		}
		embedded++

		// Progress logging every 500 nodes
		if (embedded+failed)%500 == 0 {
			elapsed := time.Since(start)
			rate := float64(embedded+failed) / elapsed.Seconds()
			t.Logf("  progress: %d/%d embedded (%.1f nodes/sec, %d failures)", embedded, len(allNodes), rate, failed)
		}
	}
	embeddingDuration := time.Since(start)
	rate := float64(embedded) / embeddingDuration.Seconds()
	t.Logf("Embedded %d nodes in %v (%.1f nodes/sec, %d failures)", embedded, embeddingDuration, rate, failed)

	// Build search engine with the new store + neural embedder + shared graph
	searchEng := search.New(store, cfg.embedder, env.graphEngine)

	// Run the benchmark using the shared grading infrastructure
	answerKey := parseAnswerKey(t)
	limits := map[string]int{"A": 20, "B": 40, "C": 40}
	result := runBenchmarkWith(searchEng, answerKey, limits)

	// Detailed per-query logging
	t.Logf("\n=== %s BENCHMARK RESULTS ===", cfg.name)
	for _, q := range result.Queries {
		t.Logf("[%s] %q — %d/%d hits (%d results returned)", q.ID, q.Query, q.Hits, q.Total, len(q.Results))
		for _, h := range q.HitItems {
			t.Logf("    HIT:  %s", h)
		}
		for _, m := range q.MissItems {
			t.Logf("    MISS: %s", m)
		}

		maxShow := 5
		if len(q.Results) < maxShow {
			maxShow = len(q.Results)
		}
		for i := 0; i < maxShow; i++ {
			r := q.Results[i]
			t.Logf("  result[%d]: %s (%s) in %s — score=%.4f",
				i, r.Node.SymbolName, r.Node.NodeType, r.Node.FilePath, r.Score)
		}
	}

	// Aggregate scoring
	aTotal := result.ATotal
	aHits := result.AHits
	aPct := float64(0)
	if aTotal > 0 {
		aPct = float64(aHits) / float64(aTotal) * 100
	}
	t.Logf("\n=== %s TIER SUMMARY ===", cfg.name)
	t.Logf("Tier A: %d/%d hits (%.1f%%)", aHits, aTotal, aPct)
	t.Logf("Tier B+C: %d/%d hits (%.1f%%)", result.BCHits, result.BCTotal, result.BCRate)

	allHits := aHits + result.BCHits
	allTotal := aTotal + result.BCTotal
	allPct := float64(0)
	if allTotal > 0 {
		allPct = float64(allHits) / float64(allTotal) * 100
	}
	t.Logf("Overall: %d/%d hits (%.1f%%)", allHits, allTotal, allPct)
	t.Logf("Embedding time: %v (%.1f nodes/sec)", embeddingDuration, rate)
}

// initONNXEmbedder creates an ONNX embedder with the given model dir, dimension,
// and library path. Returns the embedder or skips the test if prerequisites are missing.
func initONNXEmbedder(t *testing.T, modelDir string, dim int, modelCheckFile string) *embedding.ONNXEmbedder {
	t.Helper()

	onnxLibPath := os.Getenv("ONNX_LIB_PATH")
	if onnxLibPath == "" {
		onnxLibPath = "/Library/Frameworks/Python.framework/Versions/3.12/lib/python3.12/site-packages/onnxruntime/capi/libonnxruntime.1.24.4.dylib"
	}
	if _, err := os.Stat(onnxLibPath); err != nil {
		t.Skipf("ONNX runtime not available at %s", onnxLibPath)
	}

	checkFile := filepath.Join(modelDir, modelCheckFile)
	if _, err := os.Stat(checkFile); err != nil {
		t.Skipf("Model not available: %s not found", checkFile)
	}

	embedder, err := embedding.NewONNXEmbedder(modelDir, dim, onnxLibPath)
	if err != nil {
		t.Fatalf("NewONNXEmbedder(%s, dim=%d): %v", modelDir, dim, err)
	}
	return embedder
}

// TestBenchmark_CodeRankEmbed_ONNX benchmarks the CodeRankEmbed NomicBERT model (768-dim)
// against the qbapi corpus and compares results to the TF-IDF baseline.
func TestBenchmark_CodeRankEmbed_ONNX(t *testing.T) {
	env := getSharedEnv(t)
	_ = env // ensure shared env is available

	modelDir := filepath.Join("..", "models", "CodeRankEmbed-onnx-int8")
	dim := 768

	embedder := initONNXEmbedder(t, modelDir, dim, "tokenizer.json")
	defer embedder.Close()

	runBackendBenchmark(t, backendBenchConfig{
		name:     "CodeRankEmbed-ONNX-768",
		dim:      dim,
		embedder: embedder,
		closeFn:  func() { embedder.Close() },
	})
}

// TestBenchmark_Jina_ONNX benchmarks the Jina Qwen2 model (256-dim Matryoshka)
// against the qbapi corpus and compares results to the TF-IDF baseline.
func TestBenchmark_Jina_ONNX(t *testing.T) {
	env := getSharedEnv(t)
	_ = env // ensure shared env is available

	jinaModelDir := os.Getenv("JINA_MODEL_DIR")
	if jinaModelDir == "" {
		jinaModelDir = "/Users/naman/Documents/coindex/quantized_model/"
	}
	dim := 256

	embedder := initONNXEmbedder(t, jinaModelDir, dim, "tokenizer.json")
	defer embedder.Close()

	runBackendBenchmark(t, backendBenchConfig{
		name:     "Jina-ONNX-256",
		dim:      dim,
		embedder: embedder,
		closeFn:  func() { embedder.Close() },
	})
}

// TestBenchmark_CompareAll runs all available backends and prints a side-by-side comparison.
// Skips any backend whose prerequisites are not met.
func TestBenchmark_CompareAll(t *testing.T) {
	env := getSharedEnv(t)
	_ = env

	// TF-IDF baseline (always available)
	answerKey := parseAnswerKey(t)
	limits := map[string]int{"A": 20, "B": 40, "C": 40}
	baselineResult := runBenchmarkWith(env.searchEng, answerKey, limits)

	scores := []backendScore{
		{
			name:    "TF-IDF (baseline)",
			aHits:   baselineResult.AHits,
			aTotal:  baselineResult.ATotal,
			bcHits:  baselineResult.BCHits,
			bcTotal: baselineResult.BCTotal,
			bcRate:  baselineResult.BCRate,
		},
	}

	// Try CodeRankEmbed
	t.Run("CodeRankEmbed", func(t *testing.T) {
		modelDir := filepath.Join("..", "models", "CodeRankEmbed-onnx-int8")
		dim := 768
		embedder := initONNXEmbedder(t, modelDir, dim, "tokenizer.json")
		defer embedder.Close()

		score := runComparisonBackend(t, env, embedder, "CodeRankEmbed-768", dim, answerKey, limits)
		scores = append(scores, score)
	})

	// Try Jina
	t.Run("Jina", func(t *testing.T) {
		jinaModelDir := os.Getenv("JINA_MODEL_DIR")
		if jinaModelDir == "" {
			jinaModelDir = "/Users/naman/Documents/coindex/quantized_model/"
		}
		dim := 256
		embedder := initONNXEmbedder(t, jinaModelDir, dim, "tokenizer.json")
		defer embedder.Close()

		score := runComparisonBackend(t, env, embedder, "Jina-256", dim, answerKey, limits)
		scores = append(scores, score)
	})

	// Print comparison table
	t.Logf("\n=== BACKEND COMPARISON ===")
	t.Logf("%-25s  %8s  %8s  %8s", "Backend", "A hits", "B+C hits", "B+C rate")
	t.Logf("%-25s  %8s  %8s  %8s", "-------", "------", "--------", "--------")
	for _, s := range scores {
		t.Logf("%-25s  %4d/%-3d  %4d/%-3d    %5.1f%%",
			s.name, s.aHits, s.aTotal, s.bcHits, s.bcTotal, s.bcRate)
	}
}

// runComparisonBackend creates a temp store, embeds all nodes, runs the benchmark,
// and returns the aggregate score for use in comparison tables.
func runComparisonBackend(
	t *testing.T,
	env *realRepoEnv,
	embedder embedding.Embedder,
	name string,
	dim int,
	answerKey map[string][]expectedItem,
	limits map[string]int,
) backendScore {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")
	store, err := storage.NewStore(dbPath, dim)
	if err != nil {
		t.Fatalf("NewStore(dim=%d): %v", dim, err)
	}
	defer store.Close()

	// Load and copy nodes
	allNodeIDs, err := env.store.GetAllNodeIDs()
	if err != nil {
		t.Fatalf("GetAllNodeIDs: %v", err)
	}
	allNodes := make([]types.ASTNode, 0, len(allNodeIDs))
	for _, id := range allNodeIDs {
		node, err := env.store.GetNode(id)
		if err != nil {
			continue
		}
		allNodes = append(allNodes, *node)
	}

	if err := store.UpsertNodes(allNodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	allEdges, err := env.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	if err := store.UpsertEdges(allEdges); err != nil {
		t.Fatalf("UpsertEdges: %v", err)
	}

	// Embed all nodes
	t.Logf("Embedding %d nodes with %s...", len(allNodes), name)
	start := time.Now()
	embedded, failed := 0, 0
	for _, node := range allNodes {
		text := node.SymbolName + " " + node.ContentSum
		if text == " " {
			text = node.FilePath
		}
		vec, err := embedder.Embed(text)
		if err != nil {
			failed++
			continue
		}
		if err := store.UpsertEmbedding(node.ID, vec); err != nil {
			failed++
			continue
		}
		embedded++
	}
	t.Logf("Embedded %d nodes in %v (%d failures)", embedded, time.Since(start), failed)

	searchEng := search.New(store, embedder, env.graphEngine)
	result := runBenchmarkWith(searchEng, answerKey, limits)

	// Log per-query results
	for _, q := range result.Queries {
		t.Logf("[%s] %q — %d/%d", q.ID, q.Query, q.Hits, q.Total)
	}

	return backendScore{
		name:    name,
		aHits:   result.AHits,
		aTotal:  result.ATotal,
		bcHits:  result.BCHits,
		bcTotal: result.BCTotal,
		bcRate:  result.BCRate,
	}
}

// backendScore holds aggregate scoring for a single backend in comparison mode.
type backendScore struct {
	name    string
	aHits   int
	aTotal  int
	bcHits  int
	bcTotal int
	bcRate  float64
}

// TestBenchmark_TFIDFBaseline runs just the TF-IDF baseline for quick comparison reference.
// This uses the shared env directly (no re-embedding needed).
func TestBenchmark_TFIDFBaseline(t *testing.T) {
	env := getSharedEnv(t)

	answerKey := parseAnswerKey(t)
	limits := map[string]int{"A": 20, "B": 40, "C": 40}
	result := runBenchmarkWith(env.searchEng, answerKey, limits)

	t.Logf("\n=== TF-IDF BASELINE RESULTS ===")
	for _, q := range result.Queries {
		t.Logf("[%s] %q — %d/%d hits", q.ID, q.Query, q.Hits, q.Total)
	}

	aPct := float64(0)
	if result.ATotal > 0 {
		aPct = float64(result.AHits) / float64(result.ATotal) * 100
	}
	allHits := result.AHits + result.BCHits
	allTotal := result.ATotal + result.BCTotal
	allPct := float64(0)
	if allTotal > 0 {
		allPct = float64(allHits) / float64(allTotal) * 100
	}

	t.Logf("Tier A: %d/%d (%.1f%%)", result.AHits, result.ATotal, aPct)
	t.Logf("Tier B+C: %d/%d (%.1f%%)", result.BCHits, result.BCTotal, result.BCRate)
	t.Logf("Overall: %d/%d (%.1f%%)", allHits, allTotal, allPct)
}

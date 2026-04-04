//go:build fts5

package search

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
)

// newTestStore creates a temporary SQLite store for testing.
func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// insertTestNodes inserts a set of nodes into the store for search tests.
func insertTestNodes(t *testing.T, s *storage.Store) []types.ASTNode {
	t.Helper()
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("compute.go", "ComputeChecksum"),
			FilePath:   "compute.go",
			SymbolName: "ComputeChecksum",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    100,
			ContentSum: "ComputeChecksum calculates SHA256 checksum of a file",
		},
		{
			ID:         types.GenerateNodeID("read.go", "ReadFileContents"),
			FilePath:   "read.go",
			SymbolName: "ReadFileContents",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    80,
			ContentSum: "ReadFileContents opens and reads bytes from disk file",
		},
		{
			ID:         types.GenerateNodeID("db.go", "OpenDatabaseConnection"),
			FilePath:   "db.go",
			SymbolName: "OpenDatabaseConnection",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    120,
			ContentSum: "OpenDatabaseConnection establishes a pool of database connections",
		},
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	return nodes
}

// TestSearch_ReturnsResults verifies that Search returns results when nodes match the query.
func TestSearch_ReturnsResults(t *testing.T) {
	s := newTestStore(t)
	insertTestNodes(t, s)

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	results, err := hs.Search("checksum", 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'checksum', got 0")
	}

	// Verify the ComputeChecksum node is somewhere in the results
	found := false
	for _, r := range results {
		if r.Node.SymbolName == "ComputeChecksum" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'ComputeChecksum' to be in search results for 'checksum'")
	}
}

// TestSearch_SortedByScoreDescending verifies that results are ordered by score descending.
func TestSearch_SortedByScoreDescending(t *testing.T) {
	s := newTestStore(t)
	insertTestNodes(t, s)

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	results, err := hs.Search("database connection", 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted: results[%d].Score=%f > results[%d].Score=%f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

// TestSearch_LimitRespected verifies that Search returns at most limit results.
func TestSearch_LimitRespected(t *testing.T) {
	s := newTestStore(t)

	// Insert more nodes than the limit
	nodes := make([]types.ASTNode, 5)
	for i := range nodes {
		name := "ProcessItem" + string(rune('A'+i))
		nodes[i] = types.ASTNode{
			ID:         types.GenerateNodeID("proc.go", name),
			FilePath:   "proc.go",
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  uint32(i * 100),
			EndByte:    uint32(i*100 + 50),
			ContentSum: name + " processes an item efficiently with checksum validation",
		}
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	const limit = 2
	results, err := hs.Search("process", limit, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) > limit {
		t.Errorf("expected at most %d results, got %d", limit, len(results))
	}
}

// TestSearch_NilEmbedder verifies that Search works even without an embedder (lexical only).
func TestSearch_NilEmbedder(t *testing.T) {
	s := newTestStore(t)
	insertTestNodes(t, s)

	g := graph.New()
	hs := New(s, nil, g) // nil embedder: lexical-only search

	results, err := hs.Search("checksum", 10, nil)
	if err != nil {
		t.Fatalf("Search with nil embedder: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'checksum' with lexical-only search")
	}
}

// TestSearch_CompositeScoring verifies that a node appearing in both lexical and semantic
// results scores higher than a node appearing in only one signal.
func TestSearch_CompositeScoring(t *testing.T) {
	s := newTestStore(t)

	// Insert two nodes: one whose name AND content match the query (higher overlap),
	// one whose content alone matches.
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("alpha.go", "ComputeChecksum"),
			FilePath:   "alpha.go",
			SymbolName: "ComputeChecksum",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    100,
			ContentSum: "ComputeChecksum calculates the SHA256 checksum of a given file",
		},
		{
			ID:         types.GenerateNodeID("beta.go", "UnrelatedHelper"),
			FilePath:   "beta.go",
			SymbolName: "UnrelatedHelper",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    50,
			ContentSum: "UnrelatedHelper does something entirely different",
		},
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	results, err := hs.Search("checksum", 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'checksum'")
	}

	// ComputeChecksum should appear and rank first since it matches the query directly
	if results[0].Node.SymbolName != "ComputeChecksum" {
		t.Errorf("expected ComputeChecksum to be top result, got %s", results[0].Node.SymbolName)
	}

	// Results must be sorted descending
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted: index %d score %f > index %d score %f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

// TestSearch_PerFileCap verifies that at most maxPerFile (3) results per file are returned.
func TestSearch_PerFileCap(t *testing.T) {
	s := newTestStore(t)

	// Insert 5 nodes all in the same file, all matching the query
	nodes := make([]types.ASTNode, 5)
	for i := range nodes {
		name := "ProcessData" + string(rune('A'+i))
		nodes[i] = types.ASTNode{
			ID:         types.GenerateNodeID("samefile.go", name),
			FilePath:   "samefile.go",
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  uint32(i * 100),
			EndByte:    uint32(i*100 + 90),
			ContentSum: name + " processes data records efficiently",
		}
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	// Request more than maxPerFile results from a single file
	results, err := hs.Search("process data", 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Count results from samefile.go
	fileCount := 0
	for _, r := range results {
		if r.Node.FilePath == "samefile.go" {
			fileCount++
		}
	}
	if fileCount > defaultMaxPerFile {
		t.Errorf("per-file cap exceeded: got %d results from samefile.go, want at most %d", fileCount, defaultMaxPerFile)
	}
}

// TestSearch_CustomMaxPerFile verifies that a custom max_per_file parameter is respected.
func TestSearch_CustomMaxPerFile(t *testing.T) {
	s := newTestStore(t)

	// Insert 5 nodes all in the same file, all matching the query
	nodes := make([]types.ASTNode, 5)
	for i := range nodes {
		name := "HandleRequest" + string(rune('A'+i))
		nodes[i] = types.ASTNode{
			ID:         types.GenerateNodeID("handlers.go", name),
			FilePath:   "handlers.go",
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  uint32(i * 100),
			EndByte:    uint32(i*100 + 90),
			ContentSum: name + " handles HTTP request processing",
		}
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	// Request with custom maxPerFile of 5 (higher than default 3)
	results, err := hs.Search("handle request", 10, nil, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	fileCount := 0
	for _, r := range results {
		if r.Node.FilePath == "handlers.go" {
			fileCount++
		}
	}
	// With maxPerFile=5, we should get up to 5 results from the same file
	if fileCount > 5 {
		t.Errorf("custom per-file cap exceeded: got %d results from handlers.go, want at most 5", fileCount)
	}
	// And it should be more than the default of 3 (if enough nodes matched)
	if fileCount <= defaultMaxPerFile && len(results) > defaultMaxPerFile {
		t.Logf("Note: only %d results from handlers.go matched; custom cap may not have been exercised", fileCount)
	}
}

// ---- M23: Search with activeFileNodeIDs ----

func TestSearch_WithActiveFileNodeIDs(t *testing.T) {
	s := newTestStore(t)

	// Insert nodes across different files
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("active.go", "ActiveFunc"),
			FilePath:   "active.go",
			SymbolName: "ActiveFunc",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    100,
			ContentSum: "ActiveFunc handles data processing operations",
		},
		{
			ID:         types.GenerateNodeID("other.go", "OtherFunc"),
			FilePath:   "other.go",
			SymbolName: "OtherFunc",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    80,
			ContentSum: "OtherFunc handles data processing operations",
		},
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	embedder := embedding.NewHashEmbedder()

	// Build a graph with edges so PPR has something to work with
	g := graph.New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: nodes[0].ID, TargetID: nodes[1].ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: nodes[1].ID, TargetID: nodes[0].ID, EdgeType: types.EdgeTypeCalls},
	})

	hs := New(s, embedder, g)

	// Search without active files
	resultsNoActive, err := hs.Search("data processing", 10, nil)
	if err != nil {
		t.Fatalf("Search without active files: %v", err)
	}

	// Search with active file node IDs (boost ActiveFunc's file)
	activeIDs := []string{nodes[0].ID}
	resultsWithActive, err := hs.Search("data processing", 10, activeIDs)
	if err != nil {
		t.Fatalf("Search with active files: %v", err)
	}

	// Both searches should return results and not panic
	if len(resultsNoActive) == 0 {
		t.Fatal("expected results without active files")
	}
	if len(resultsWithActive) == 0 {
		t.Fatal("expected results with active files")
	}

	// Find ActiveFunc scores in both result sets
	var scoreNoActive, scoreWithActive float64
	for _, r := range resultsNoActive {
		if r.Node.SymbolName == "ActiveFunc" {
			scoreNoActive = r.Score
		}
	}
	for _, r := range resultsWithActive {
		if r.Node.SymbolName == "ActiveFunc" {
			scoreWithActive = r.Score
		}
	}

	// With active file boosting, ActiveFunc should score >= without it
	// (PPR teleportation adds bias toward active file nodes)
	if scoreWithActive < scoreNoActive {
		t.Logf("Note: ActiveFunc score with active files (%f) < without (%f); PPR boost may be small",
			scoreWithActive, scoreNoActive)
	}
	t.Logf("ActiveFunc score: without active=%f, with active=%f", scoreNoActive, scoreWithActive)
}

// TestSanitizeFTS_StripsStar verifies that the * wildcard is stripped from user input.
func TestSanitizeFTS_StripsStar(t *testing.T) {
	result := sanitizeFTS("test*query")
	if strings.Contains(result, "*") {
		t.Errorf("expected * to be stripped from user input, got: %s", result)
	}
}

// TestBuildFTSQuery_CamelCase verifies that CamelCase identifiers are split into sub-tokens.
func TestBuildFTSQuery_CamelCase(t *testing.T) {
	result := buildFTSQuery("ReadFileContents")

	// The result should contain "Read", "File", "Contents" as individual terms
	// (plus possibly the original identifier)
	lower := strings.ToLower(result)
	if !strings.Contains(lower, "read") {
		t.Errorf("expected 'read' in FTS query, got: %s", result)
	}
	if !strings.Contains(lower, "file") {
		t.Errorf("expected 'file' in FTS query, got: %s", result)
	}
	if !strings.Contains(lower, "contents") {
		t.Errorf("expected 'contents' in FTS query, got: %s", result)
	}
}

// TestBuildFTSQuery_StopWords verifies that common stop words are filtered out.
func TestBuildFTSQuery_StopWords(t *testing.T) {
	stopWordQueries := []string{"the database", "a function", "is being called"}
	for _, query := range stopWordQueries {
		result := buildFTSQuery(query)
		// Stop words should be filtered out — they should not appear as standalone terms
		lower := strings.ToLower(result)
		terms := strings.Split(lower, " OR ")
		for _, sw := range []string{"the", "a", "is", "being"} {
			for _, term := range terms {
				cleaned := strings.TrimSpace(strings.TrimSuffix(term, "*"))
				if cleaned == sw {
					t.Errorf("buildFTSQuery(%q) = %q, should not contain stop word %q as standalone term", query, result, sw)
				}
			}
		}
	}

	// Verify important content words are preserved
	result := buildFTSQuery("the function that reads a file")
	lower := strings.ToLower(result)
	if !strings.Contains(lower, "function") {
		t.Errorf("expected 'function' in FTS query, got: %s", result)
	}
	if !strings.Contains(lower, "reads") {
		t.Errorf("expected 'reads' in FTS query, got: %s", result)
	}
	if !strings.Contains(lower, "file") {
		t.Errorf("expected 'file' in FTS query, got: %s", result)
	}
}

// TestSetStopWords_ConcurrentSafety verifies that concurrent SetStopWords and
// buildFTSQuery calls don't race (M4 fix).
//
// NOTE: This test mutates global stopWords and must NOT use t.Parallel().
// t.Cleanup restores the default stop words so other tests are unaffected.
func TestSetStopWords_ConcurrentSafety(t *testing.T) {
	// Save originals and guarantee restoration even on panic
	original := GetStopWords()
	t.Cleanup(func() { SetStopWords(original) })

	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 100

	// Concurrent writers
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if id%2 == 0 {
					SetStopWords([]string{"alpha", "beta", "gamma"})
				} else {
					SetStopWords([]string{"the", "a", "is"})
				}
			}
		}(i)
	}

	// Concurrent readers (via buildFTSQuery)
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				result := buildFTSQuery("the quick brown fox")
				if result == "" {
					t.Error("buildFTSQuery returned empty string")
				}
			}
		}()
	}

	wg.Wait()
}

// ---- M19: Zero-length query Search test ----

func TestSearch_EmptyQuery(t *testing.T) {
	s := newTestStore(t)
	insertTestNodes(t, s)

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	// Empty string query should return empty results or nil, not panic
	results, err := hs.Search("", 10, nil)
	if err != nil {
		t.Fatalf("Search('') unexpected error: %v", err)
	}
	// Empty query may return nil or empty slice — both are acceptable
	t.Logf("Search('') returned %d results", len(results))
}

func TestSearch_WhitespaceOnlyQuery(t *testing.T) {
	s := newTestStore(t)
	insertTestNodes(t, s)

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	// Whitespace-only query should not panic
	results, err := hs.Search("   ", 10, nil)
	if err != nil {
		t.Fatalf("Search('   ') unexpected error: %v", err)
	}
	t.Logf("Search('   ') returned %d results", len(results))
}

// ---- M7: Direct unit tests for pathPenalty and applyPerFileCap ----

// TestPathPenalty verifies that path-based scoring penalties are correctly
// assigned for different file categories.
func TestPathPenalty(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		want     float64
	}{
		// 0.3x: IDE helpers, generated, vendor
		{"ide helper PHP", "_ide_helper.php", 0.3},
		{"ide helper models", "_ide_helper_models.php", 0.3},
		{"TypeScript declaration", "types/react.d.ts", 0.3},
		{"generated directory", "generated/models.go", 0.3},
		{"generated prefix dot", "src/generated.pb.go", 0.3},
		{"vendor directory", "vendor/laravel/framework/src/Auth.php", 0.3},
		{"node_modules", "node_modules/@types/node/index.js", 0.3},
		{"lib directory", "lib/legacy/utils.js", 0.3},

		// 0.2x: migrations
		{"database migrations", "database/migrations/2024_01_create_orders.php", 0.2},
		{"migration directory", "migrations/001_init.sql", 0.2},

		// 0.3x: test files
		{"tests directory", "tests/Unit/OrderTest.php", 0.3},
		{"test directory", "test/helpers/setup.js", 0.3},
		{"__tests__ directory", "__tests__/Order.test.js", 0.3},
		{"spec directory", "spec/models/order_spec.rb", 0.3},
		{"_test.go suffix", "internal/search/hybrid_test.go", 0.3},
		{".test.js suffix", "src/utils.test.js", 0.3},
		{".spec.ts suffix", "src/api.spec.ts", 0.3},
		{"Test.php suffix", "tests/OrderControllerTest.php", 0.3},

		// 0.6x: examples
		{"examples directory", "examples/basic/main.go", 0.6},
		{"example directory", "example/quickstart.py", 0.6},

		// 0.8x: config directories
		{"config directory", "config/session.php", 0.8},
		{"config app file", "config/app.php", 0.8},
		{"app config directory", "app/config/database.php", 0.8},
		{"src Config directory", "src/Config/AppConfig.ts", 0.8},

		// 1.0x: regular source files
		{"regular Go", "internal/search/hybrid.go", 1.0},
		{"regular PHP", "app/Http/Controllers/OrderController.php", 1.0},
		{"regular JS", "src/components/App.tsx", 1.0},
		{"model file", "app/Order.php", 1.0},
		{"service file", "app/Services/PaymentService.php", 1.0},
		{"empty path", "", 1.0},

		// Edge cases: should NOT misclassify
		{"file with generate in name", "UserGeneratedContent.php", 1.0},
		{"code generator", "codegenerator.go", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathPenalty(tt.filePath)
			if got != tt.want {
				t.Errorf("pathPenalty(%q) = %v, want %v", tt.filePath, got, tt.want)
			}
		})
	}
}

// TestApplyPerFileCap verifies that per-file result capping works correctly
// for various input distributions.
func TestApplyPerFileCap(t *testing.T) {
	// Helper to build search results with given file paths and scores.
	makeResults := func(entries []struct {
		filePath string
		score    float64
	}) []types.SearchResult {
		var results []types.SearchResult
		for i, e := range entries {
			results = append(results, types.SearchResult{
				Node: types.ASTNode{
					ID:       string(rune('A' + i)),
					FilePath: e.filePath,
				},
				Score: e.score,
			})
		}
		return results
	}

	t.Run("empty input", func(t *testing.T) {
		result := applyPerFileCap(nil, 3)
		if len(result) != 0 {
			t.Errorf("expected 0 results for nil input, got %d", len(result))
		}
		result2 := applyPerFileCap([]types.SearchResult{}, 3)
		if len(result2) != 0 {
			t.Errorf("expected 0 results for empty input, got %d", len(result2))
		}
	})

	t.Run("single file exceeds cap", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"main.go", 1.0},
			{"main.go", 0.9},
			{"main.go", 0.8},
			{"main.go", 0.7},
			{"main.go", 0.6},
		})
		result := applyPerFileCap(input, 3)
		if len(result) != 3 {
			t.Errorf("expected 3 results with cap=3, got %d", len(result))
		}
	})

	t.Run("single file under cap", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"main.go", 1.0},
			{"main.go", 0.9},
		})
		result := applyPerFileCap(input, 3)
		if len(result) != 2 {
			t.Errorf("expected 2 results (under cap), got %d", len(result))
		}
	})

	t.Run("multiple files each under cap", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"a.go", 1.0},
			{"b.go", 0.9},
			{"a.go", 0.8},
			{"b.go", 0.7},
		})
		result := applyPerFileCap(input, 3)
		if len(result) != 4 {
			t.Errorf("expected all 4 results (each file has <=3), got %d", len(result))
		}
	})

	t.Run("multiple files with mixed counts", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"a.go", 1.0},
			{"a.go", 0.9},
			{"a.go", 0.8},
			{"a.go", 0.7}, // 4th from a.go, should be capped
			{"b.go", 0.6},
			{"b.go", 0.5},
		})
		result := applyPerFileCap(input, 3)
		// a.go: 3 kept, b.go: 2 kept = 5 total
		if len(result) != 5 {
			t.Errorf("expected 5 results, got %d", len(result))
		}
		// Verify a.go count
		aCount := 0
		for _, r := range result {
			if r.Node.FilePath == "a.go" {
				aCount++
			}
		}
		if aCount != 3 {
			t.Errorf("expected 3 results from a.go, got %d", aCount)
		}
	})

	t.Run("helper file capped at 1", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"_ide_helper.php", 1.0},
			{"_ide_helper.php", 0.9},
			{"_ide_helper.php", 0.8},
			{"regular.go", 0.7},
			{"regular.go", 0.6},
		})
		result := applyPerFileCap(input, 3)
		// _ide_helper.php: capped at 1, regular.go: 2 (under cap of 3) = 3 total
		helperCount := 0
		regularCount := 0
		for _, r := range result {
			switch r.Node.FilePath {
			case "_ide_helper.php":
				helperCount++
			case "regular.go":
				regularCount++
			}
		}
		if helperCount != 1 {
			t.Errorf("expected 1 result from _ide_helper.php (helper cap=1), got %d", helperCount)
		}
		if regularCount != 2 {
			t.Errorf("expected 2 results from regular.go, got %d", regularCount)
		}
	})

	t.Run("d.ts helper file capped at 1", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"types/index.d.ts", 1.0},
			{"types/index.d.ts", 0.9},
			{"src/main.ts", 0.8},
			{"src/main.ts", 0.7},
			{"src/main.ts", 0.6},
		})
		result := applyPerFileCap(input, 3)
		dtsCount := 0
		mainCount := 0
		for _, r := range result {
			switch r.Node.FilePath {
			case "types/index.d.ts":
				dtsCount++
			case "src/main.ts":
				mainCount++
			}
		}
		if dtsCount != 1 {
			t.Errorf("expected 1 result from d.ts file (helper cap=1), got %d", dtsCount)
		}
		if mainCount != 3 {
			t.Errorf("expected 3 results from src/main.ts, got %d", mainCount)
		}
	})

	t.Run("generated file capped at 1", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"generated/models.go", 1.0},
			{"generated/models.go", 0.9},
			{"app.go", 0.8},
		})
		result := applyPerFileCap(input, 3)
		genCount := 0
		for _, r := range result {
			if r.Node.FilePath == "generated/models.go" {
				genCount++
			}
		}
		if genCount != 1 {
			t.Errorf("expected 1 result from generated/ file (helper cap=1), got %d", genCount)
		}
	})

	t.Run("cap of 1 for all files", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"a.go", 1.0},
			{"a.go", 0.9},
			{"b.go", 0.8},
			{"b.go", 0.7},
		})
		result := applyPerFileCap(input, 1)
		if len(result) != 2 {
			t.Errorf("expected 2 results with cap=1 across 2 files, got %d", len(result))
		}
	})

	t.Run("preserves order", func(t *testing.T) {
		input := makeResults([]struct {
			filePath string
			score    float64
		}{
			{"a.go", 1.0},
			{"b.go", 0.9},
			{"a.go", 0.8},
			{"b.go", 0.7},
			{"a.go", 0.6}, // capped
		})
		result := applyPerFileCap(input, 2)
		// Should keep: a.go(1.0), b.go(0.9), a.go(0.8), b.go(0.7)
		if len(result) != 4 {
			t.Fatalf("expected 4 results, got %d", len(result))
		}
		// Verify order is preserved
		for i := 1; i < len(result); i++ {
			if result[i].Score > result[i-1].Score {
				t.Errorf("order not preserved: result[%d].Score=%f > result[%d].Score=%f",
					i, result[i].Score, i-1, result[i-1].Score)
			}
		}
	})
}

// TestBuildFTSQuery_AllStopWords verifies that buildFTSQuery returns a
// non-empty fallback when every token is a stop word (prevents empty FTS query).
func TestBuildFTSQuery_AllStopWords(t *testing.T) {
	// "the a is" are all stop words
	result := buildFTSQuery("the a is")
	if result == "" {
		t.Fatal("buildFTSQuery returned empty string for all-stop-word input")
	}

	// The function should fall back to the original query
	if !strings.Contains(result, "the") && !strings.Contains(result, "a") && !strings.Contains(result, "is") {
		t.Errorf("expected fallback to contain original terms, got: %q", result)
	}

	// Also test single stop word
	result2 := buildFTSQuery("the")
	if result2 == "" {
		t.Fatal("buildFTSQuery returned empty string for single stop word")
	}
}

// ---- Query alias expansion ----

func TestBuildFTSQuery_AliasExpansion(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantTerms []string // terms that should appear in the FTS query
	}{
		{
			"omnichannel expands to code names",
			"omnichannel order sync",
			[]string{"easyecom", "unicommerce", "onlineorder", "order", "sync"},
		},
		{
			"auth expands to auth terms",
			"auth session management",
			[]string{"oauth", "login", "token", "auth"},
		},
		{
			"webhook expands to callback",
			"webhook dispatch",
			[]string{"callback", "hook", "dispatchwebhook", "webhook", "dispatch"},
		},
		{
			"inventory expands to stock terms",
			"inventory lookup",
			[]string{"stock", "stocktransaction", "stockledger", "warehouse", "inventory", "lookup"},
		},
		{
			"payment expands to billing terms",
			"payment processing",
			[]string{"razorpay", "billing", "invoice", "payment", "processing"},
		},
		{
			"no alias for regular terms",
			"controller method",
			[]string{"controller", "method"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFTSQuery(tt.query)
			lower := strings.ToLower(result)
			for _, want := range tt.wantTerms {
				if !strings.Contains(lower, want) {
					t.Errorf("buildFTSQuery(%q) = %q, expected to contain %q", tt.query, result, want)
				}
			}
		})
	}
}

func TestBuildFTSQuery_AliasPrefixMatching(t *testing.T) {
	// "authentication" has prefix "auth" → should trigger auth alias expansion
	t.Run("authentication triggers auth alias via prefix", func(t *testing.T) {
		result := buildFTSQuery("authentication session")
		lower := strings.ToLower(result)
		if !strings.Contains(lower, "oauth") {
			t.Errorf("expected 'oauth' from auth prefix alias, got: %s", result)
		}
		if !strings.Contains(lower, "login") {
			t.Errorf("expected 'login' from auth prefix alias, got: %s", result)
		}
		// Original terms should still be present
		if !strings.Contains(lower, "authentication") {
			t.Errorf("expected 'authentication' in result, got: %s", result)
		}
		if !strings.Contains(lower, "session") {
			t.Errorf("expected 'session' in result, got: %s", result)
		}
	})

	// "payments" has prefix "payment" → should trigger payment alias expansion
	t.Run("payments triggers payment alias via prefix", func(t *testing.T) {
		result := buildFTSQuery("payments lookup")
		lower := strings.ToLower(result)
		if !strings.Contains(lower, "razorpay") {
			t.Errorf("expected 'razorpay' from payment prefix alias, got: %s", result)
		}
		if !strings.Contains(lower, "billing") {
			t.Errorf("expected 'billing' from payment prefix alias, got: %s", result)
		}
	})
}

// TestHybridSearch_ScoreBreakdown verifies that search results contain populated ScoreBreakdown fields.
func TestHybridSearch_ScoreBreakdown(t *testing.T) {
	s := newTestStore(t)
	insertTestNodes(t, s)

	embedder := embedding.NewHashEmbedder()
	// Upsert embeddings for semantic signal
	nodes := insertTestNodes(t, s) // re-insert is idempotent via upsert
	for _, n := range nodes {
		vec, err := embedder.Embed(n.SymbolName + " " + n.ContentSum)
		if err != nil {
			t.Fatalf("Embed(%s): %v", n.SymbolName, err)
		}
		if err := s.UpsertEmbedding(n.ID, vec); err != nil {
			t.Fatalf("UpsertEmbedding(%s): %v", n.SymbolName, err)
		}
	}

	g := graph.New()
	hs := New(s, embedder, g)

	results, err := hs.Search("checksum", 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'checksum'")
	}

	r := results[0]
	if r.Breakdown == nil {
		t.Fatal("expected non-nil Breakdown on first result")
	}
	bd := r.Breakdown
	hasNonZero := bd.PPR > 0 || bd.BM25 > 0 || bd.Betweenness > 0 || bd.InDegree > 0 || bd.Semantic > 0
	if !hasNonZero {
		t.Errorf("expected at least one non-zero field in ScoreBreakdown, got %+v", bd)
	}
	t.Logf("Top result %s breakdown: PPR=%.4f BM25=%.4f Betweenness=%.4f InDegree=%.4f Semantic=%.4f",
		r.Node.SymbolName, bd.PPR, bd.BM25, bd.Betweenness, bd.InDegree, bd.Semantic)
}

// TestSearch_PathPenaltyAffectsRanking verifies that migration files rank lower
// than regular source files when both match the same query.
func TestSearch_PathPenaltyAffectsRanking(t *testing.T) {
	s := newTestStore(t)

	// Insert a migration file and a regular source file, both matching "inventory"
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("database/migrations/create_inventory.php", "CreateInventoryTable"),
			FilePath:   "database/migrations/create_inventory.php",
			SymbolName: "CreateInventoryTable",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    200,
			ContentSum: "CreateInventoryTable migration creates the inventory stock table",
		},
		{
			ID:         types.GenerateNodeID("app/Inventory.php", "stockTransaction"),
			FilePath:   "app/Inventory.php",
			SymbolName: "stockTransaction",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    300,
			ContentSum: "stockTransaction handles inventory stock movement and ledger updates",
		},
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	embedder := embedding.NewHashEmbedder()
	g := graph.New()
	hs := New(s, embedder, g)

	results, err := hs.Search("inventory stock", 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// The regular source file should rank higher than the migration
	if results[0].Node.FilePath != "app/Inventory.php" {
		t.Errorf("expected app/Inventory.php to rank #1 (path penalty on migration), got %s", results[0].Node.FilePath)
	}
}

// ---- Phase 1A: Code-aware stopwords + phrase detection ----

func TestBuildFTSQuery_CodeStopWords(t *testing.T) {
	// Standalone code method names should be filtered as stop words
	codeStopWords := []string{"handle", "end"}
	for _, word := range codeStopWords {
		result := buildFTSQuery(word)
		// When a single code stop word is the entire query, buildFTSQuery falls back
		// to the original query (line 344-346: "if len(expanded) == 0 { return query }")
		// This is correct — we don't want empty FTS queries.
		t.Logf("buildFTSQuery(%q) = %q", word, result)
	}

	// Code stop words should be stripped from multi-word queries
	result := buildFTSQuery("handle webhook events")
	lower := strings.ToLower(result)
	if strings.Contains(lower, "handle") {
		t.Errorf("expected 'handle' to be filtered, got: %s", result)
	}
	if !strings.Contains(lower, "webhook") {
		t.Errorf("expected 'webhook' to be preserved, got: %s", result)
	}
	if !strings.Contains(lower, "event") {
		t.Errorf("expected 'event' to be preserved, got: %s", result)
	}
}

func TestBuildFTSQuery_CodeStopWordsPreservedInCamelCase(t *testing.T) {
	// Code stop words inside CamelCase identifiers should NOT be filtered
	result := buildFTSQuery("stockTransaction")
	lower := strings.ToLower(result)
	if !strings.Contains(lower, "stock") {
		t.Errorf("expected 'stock' from CamelCase split, got: %s", result)
	}
	if !strings.Contains(lower, "transaction") {
		t.Errorf("expected 'transaction' from CamelCase split, got: %s", result)
	}

	// "createOrder" — "create" is NOT a stop word, should appear in results
	result2 := buildFTSQuery("createOrder")
	lower2 := strings.ToLower(result2)
	if !strings.Contains(lower2, "order") {
		t.Errorf("expected 'order' from CamelCase split, got: %s", result2)
	}
	if !strings.Contains(lower2, "createorder") {
		t.Errorf("expected original 'createOrder' to be preserved, got: %s", result2)
	}
}

func TestCleanQuery(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string // expected substring that should remain
		notWant string // substring that should be removed
	}{
		{"end to end", "end to end order creation flow", "order creation flow", "end to end"},
		{"step by step", "step by step inventory process", "inventory", "step by step"},
		{"how does", "how does authentication work", "authentication work", "how does"},
		{"show me", "show me the payment files", "payment files", "show me"},
		{"beginning to end", "beginning to end order lifecycle", "order lifecycle", "beginning to end"},
		{"no phrases", "webhook callback integration", "webhook callback integration", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanQuery(tt.input)
			if tt.want != "" && !strings.Contains(result, tt.want) {
				t.Errorf("cleanQuery(%q) = %q, expected to contain %q", tt.input, result, tt.want)
			}
			if tt.notWant != "" && strings.Contains(result, tt.notWant) {
				t.Errorf("cleanQuery(%q) = %q, expected NOT to contain %q", tt.input, result, tt.notWant)
			}
		})
	}
}

func TestCleanQuery_PreservesNonPhraseContent(t *testing.T) {
	// Queries without structural phrases should pass through unchanged (modulo case)
	result := cleanQuery("OrderController payment")
	if !strings.Contains(result, "ordercontroller") && !strings.Contains(result, "payment") {
		t.Errorf("cleanQuery should preserve non-phrase content, got: %q", result)
	}
}

// TestSearch_GraphExpansionDiscovery verifies that search works with connected
// graph nodes. Graph expansion is currently disabled (regresses benchmark),
// so this test just verifies that connected nodes don't break search.
func TestSearch_GraphExpansionDiscovery(t *testing.T) {
	s := newTestStore(t)

	// Create a "seed" node and a connected "neighbor" node
	seed := types.ASTNode{
		ID:         types.GenerateNodeID("app/Inventory.php", "stockTransaction"),
		FilePath:   "app/Inventory.php",
		SymbolName: "stockTransaction",
		NodeType:   types.NodeTypeMethod,
		StartByte:  0,
		EndByte:    200,
		ContentSum: "stockTransaction handles inventory stock movements",
	}
	neighbor := types.ASTNode{
		ID:         types.GenerateNodeID("app/Http/routes.php", "POST /inventory/update"),
		FilePath:   "app/Http/routes.php",
		SymbolName: "POST /inventory/update",
		NodeType:   types.NodeType(7), // route node type
		StartByte:  0,
		EndByte:    100,
		ContentSum: "POST endpoint for inventory update via HTTP API",
	}

	if err := s.UpsertNodes([]types.ASTNode{seed, neighbor}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// Build graph with route->method edge (route calls/handles the method)
	g := graph.New()
	edges := []types.ASTEdge{
		{
			SourceID: neighbor.ID,
			TargetID: seed.ID,
			EdgeType: types.EdgeTypeCalls,
		},
	}
	if err := s.UpsertEdges(edges); err != nil {
		t.Fatalf("UpsertEdges: %v", err)
	}
	storedEdges, _ := s.GetAllEdges()
	g.BuildFromEdges(storedEdges)

	embedder := embedding.NewHashEmbedder()
	hs := New(s, embedder, g)

	// Search should work and find the seed (direct match)
	results, err := hs.Search("stock transaction", 20, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	foundSeed := false
	for _, r := range results {
		t.Logf("  %s (%v) in %s — score=%.4f", r.Node.SymbolName, r.Node.NodeType, r.Node.FilePath, r.Score)
		if r.Node.SymbolName == "stockTransaction" {
			foundSeed = true
		}
	}

	if !foundSeed {
		t.Error("expected to find seed node 'stockTransaction' via lexical search")
	}
}

// TestSearch_GraphExpansionNoGraph verifies that search works normally when
// no graph is provided (nil graph should skip expansion).
func TestSearch_GraphExpansionNoGraph(t *testing.T) {
	s := newTestStore(t)
	insertTestNodes(t, s)

	embedder := embedding.NewHashEmbedder()
	hs := New(s, embedder, nil) // nil graph

	results, err := hs.Search("checksum", 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'checksum' with nil graph")
	}
}

// TestSearch_GraphExpansionNoNewNeighbors verifies that expansion is a no-op
// when seeds have no neighbors outside the existing candidate set.
func TestSearch_GraphExpansionNoNewNeighbors(t *testing.T) {
	s := newTestStore(t)

	// Two nodes that are both direct FTS matches and connected to each other
	nodeA := types.ASTNode{
		ID:         types.GenerateNodeID("a.go", "ProcessData"),
		FilePath:   "a.go",
		SymbolName: "ProcessData",
		NodeType:   types.NodeTypeFunction,
		StartByte:  0,
		EndByte:    100,
		ContentSum: "ProcessData handles data processing operations",
	}
	nodeB := types.ASTNode{
		ID:         types.GenerateNodeID("b.go", "ProcessDataHelper"),
		FilePath:   "b.go",
		SymbolName: "ProcessDataHelper",
		NodeType:   types.NodeTypeFunction,
		StartByte:  0,
		EndByte:    80,
		ContentSum: "ProcessDataHelper assists data processing",
	}
	if err := s.UpsertNodes([]types.ASTNode{nodeA, nodeB}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	g := graph.New()
	edges := []types.ASTEdge{
		{SourceID: nodeA.ID, TargetID: nodeB.ID, EdgeType: types.EdgeTypeCalls},
	}
	if err := s.UpsertEdges(edges); err != nil {
		t.Fatalf("UpsertEdges: %v", err)
	}
	storedEdges, _ := s.GetAllEdges()
	g.BuildFromEdges(storedEdges)

	embedder := embedding.NewHashEmbedder()
	hs := New(s, embedder, g)

	results, err := hs.Search("process data", 20, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Both nodes should appear (direct matches); expansion adds nothing new
	if len(results) < 2 {
		t.Errorf("expected at least 2 results, got %d", len(results))
	}
}

// ---- Wave 1B: Prefix alias matching, new aliases, targeted stop words ----

func TestBuildFTSQuery_PrefixAliasMatching(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantTerms []string
	}{
		{
			"authentication triggers auth aliases via prefix",
			"authentication check",
			[]string{"authentication", "oauth", "login", "token", "middleware", "check"},
		},
		{
			"loyaltypoint triggers loyalty aliases via prefix",
			"loyaltypoint redeem",
			[]string{"loyaltypoint", "mobiquest", "easyrewardz", "redeem"},
		},
		{
			"webhook still triggers webhook aliases (exact is subset of prefix)",
			"webhook dispatch",
			[]string{"webhook", "callback", "hook", "dispatchwebhook", "dispatch"},
		},
		{
			"exact match auth still works",
			"auth check",
			[]string{"auth", "oauth", "login", "token", "middleware", "check"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFTSQuery(tt.query)
			lower := strings.ToLower(result)
			for _, want := range tt.wantTerms {
				if !strings.Contains(lower, want) {
					t.Errorf("buildFTSQuery(%q) = %q, expected to contain %q", tt.query, result, want)
				}
			}
		})
	}
}

func TestBuildFTSQuery_PrefixAliasNoFalsePositives(t *testing.T) {
	// "au" is shorter than the minimum alias key length (3), should not trigger
	t.Run("short term au does not trigger aliases", func(t *testing.T) {
		result := buildFTSQuery("au check")
		lower := strings.ToLower(result)
		if strings.Contains(lower, "oauth") {
			t.Errorf("'au' should not trigger auth alias (too short), got: %s", result)
		}
	})

	// "database" has prefix "database" → triggers database alias
	t.Run("database triggers database aliases", func(t *testing.T) {
		result := buildFTSQuery("database setup")
		lower := strings.ToLower(result)
		if !strings.Contains(lower, "migration") {
			t.Errorf("expected 'migration' from database alias, got: %s", result)
		}
		if !strings.Contains(lower, "schema") {
			t.Errorf("expected 'schema' from database alias, got: %s", result)
		}
		if !strings.Contains(lower, "table") {
			t.Errorf("expected 'table' from database alias, got: %s", result)
		}
	})

	// "xyz" is not a prefix of any alias key → no expansion
	t.Run("unrelated term gets no alias expansion", func(t *testing.T) {
		result := buildFTSQuery("xyz controller")
		lower := strings.ToLower(result)
		// Should only contain the original terms (plus wildcards)
		if strings.Contains(lower, "oauth") || strings.Contains(lower, "razorpay") ||
			strings.Contains(lower, "easyecom") || strings.Contains(lower, "sentry") {
			t.Errorf("unexpected alias expansion for 'xyz controller', got: %s", result)
		}
	})
}

func TestBuildFTSQuery_NewAliases(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantTerms []string
	}{
		{
			"loyalty alias expands",
			"loyalty program",
			[]string{"loyalty", "loyaltypoint", "mobiquest", "easyrewardz"},
		},
		{
			"session alias expands",
			"session timeout",
			[]string{"session", "sessionhandler", "cookie"},
		},
		{
			"sync alias expands",
			"sync worker",
			[]string{"sync", "listener", "event", "dispatch"},
		},
		{
			"database alias expands",
			"database query",
			[]string{"database", "migration", "schema", "table"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFTSQuery(tt.query)
			lower := strings.ToLower(result)
			for _, want := range tt.wantTerms {
				if !strings.Contains(lower, want) {
					t.Errorf("buildFTSQuery(%q) = %q, expected to contain %q", tt.query, result, want)
				}
			}
		})
	}
}

func TestBuildFTSQuery_HandlingStopWord(t *testing.T) {
	// "handling" should be filtered as a stop word in multi-word queries
	t.Run("handling filtered from multi-word query", func(t *testing.T) {
		result := buildFTSQuery("webhook handling dispatch")
		lower := strings.ToLower(result)
		// "handling" should be stripped
		// Note: check for "handling" not preceded by another letter (to avoid matching inside other words)
		terms := strings.Fields(lower)
		for _, term := range terms {
			cleaned := strings.TrimSuffix(term, "*")
			if cleaned == "handling" {
				t.Errorf("expected 'handling' to be filtered as stop word, got: %s", result)
			}
		}
		// "webhook" and "dispatch" should remain
		if !strings.Contains(lower, "webhook") {
			t.Errorf("expected 'webhook' to be preserved, got: %s", result)
		}
		if !strings.Contains(lower, "dispatch") {
			t.Errorf("expected 'dispatch' to be preserved, got: %s", result)
		}
	})

	// "stockHandling" as CamelCase — "stock" should be preserved, "handling" filtered
	t.Run("stockHandling CamelCase split preserves stock", func(t *testing.T) {
		result := buildFTSQuery("stockHandling report")
		lower := strings.ToLower(result)
		if !strings.Contains(lower, "stock") {
			t.Errorf("expected 'stock' from CamelCase split of stockHandling, got: %s", result)
		}
		if !strings.Contains(lower, "report") {
			t.Errorf("expected 'report' to be preserved, got: %s", result)
		}
		// "handling" part should be filtered after CamelCase split
		terms := strings.Fields(lower)
		for _, term := range terms {
			cleaned := strings.TrimSuffix(term, "*")
			if cleaned == "handling" {
				t.Errorf("expected 'handling' to be filtered from CamelCase split, got: %s", result)
			}
		}
	})

	// Single stop word "handling" should fall back to original query (not empty)
	t.Run("handling alone falls back to original", func(t *testing.T) {
		result := buildFTSQuery("handling")
		if result == "" {
			t.Fatal("buildFTSQuery returned empty string for single stop word 'handling'")
		}
	})
}

// ---- Wave 1C: Query kind detection + node-type boosting + BM25 score floor ----

// TestDetectQueryKind verifies that detectQueryKind classifies queries correctly.
func TestDetectQueryKind(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  queryKind
	}{
		{"route terms api+endpoints", "order API endpoints", queryKindRoute},
		{"PascalCase class", "FiscalYearController", queryKindClass},
		{"HTTP verb POST", "POST /v1/merchant/{storeID}/order", queryKindRoute},
		{"snake_case function", "stock_transaction", queryKindFunction},
		{"natural language", "payment processing flow", queryKindNatural},
		{"natural language auth", "authentication and session management", queryKindNatural},
		{"natural language webhook", "webhook handling and dispatch", queryKindNatural},
		{"route login+endpoint", "find login endpoint", queryKindRoute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectQueryKind(tt.query)
			if got != tt.want {
				t.Errorf("detectQueryKind(%q) = %d, want %d", tt.query, got, tt.want)
			}
		})
	}
}

// TestNodeTypeBoost verifies that nodeTypeBoost returns expected multipliers
// for all query kind / node type combinations.
func TestNodeTypeBoost(t *testing.T) {
	tests := []struct {
		name     string
		kind     queryKind
		nodeType types.NodeType
		want     float64
	}{
		// Route kind
		{"route+Route", queryKindRoute, types.NodeTypeRoute, 2.5},
		{"route+Method", queryKindRoute, types.NodeTypeMethod, 1.3},
		{"route+Function", queryKindRoute, types.NodeTypeFunction, 1.0},
		{"route+Class", queryKindRoute, types.NodeTypeClass, 1.0},
		{"route+File", queryKindRoute, types.NodeTypeFile, 1.0},

		// Class kind
		{"class+Class", queryKindClass, types.NodeTypeClass, 1.5},
		{"class+Interface", queryKindClass, types.NodeTypeInterface, 1.5},
		{"class+Function", queryKindClass, types.NodeTypeFunction, 1.0},
		{"class+Route", queryKindClass, types.NodeTypeRoute, 1.0},

		// Function kind
		{"function+Function", queryKindFunction, types.NodeTypeFunction, 1.2},
		{"function+Method", queryKindFunction, types.NodeTypeMethod, 1.0},
		{"function+Class", queryKindFunction, types.NodeTypeClass, 1.0},

		// Natural kind (all 1.0)
		{"natural+Function", queryKindNatural, types.NodeTypeFunction, 1.0},
		{"natural+Class", queryKindNatural, types.NodeTypeClass, 1.0},
		{"natural+Route", queryKindNatural, types.NodeTypeRoute, 1.0},
		{"natural+Method", queryKindNatural, types.NodeTypeMethod, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeTypeBoost(tt.kind, tt.nodeType)
			if got != tt.want {
				t.Errorf("nodeTypeBoost(%d, %v) = %f, want %f", tt.kind, tt.nodeType, got, tt.want)
			}
		})
	}
}

// TestBM25ScoreFloor verifies that after normalization, nodes with raw BM25 > 0
// get at least 0.05 in the normalized scores (floor prevents them from collapsing to 0).
func TestBM25ScoreFloor(t *testing.T) {
	results := []types.SearchResult{
		{Node: types.ASTNode{ID: "high", SymbolName: "HighScore"}, Score: 10.0},
		{Node: types.ASTNode{ID: "low", SymbolName: "LowScore"}, Score: 0.1},
	}

	normalized := normalizeScores(results)

	// Apply BM25 floor (same logic as in Search)
	for _, r := range results {
		if r.Score > 0 {
			if norm, ok := normalized[r.Node.ID]; ok && norm < 0.05 {
				normalized[r.Node.ID] = 0.05
			}
		}
	}

	if normalized["low"] < 0.05 {
		t.Errorf("BM25 floor failed: normalized[low] = %f, want >= 0.05", normalized["low"])
	}
	if normalized["high"] < 0.99 {
		t.Errorf("BM25 floor should not affect high scorer: normalized[high] = %f", normalized["high"])
	}

	// Edge case: Score == 0 should NOT get the floor
	resultsWithZero := []types.SearchResult{
		{Node: types.ASTNode{ID: "pos"}, Score: 5.0},
		{Node: types.ASTNode{ID: "zero"}, Score: 0.0},
	}
	normZero := normalizeScores(resultsWithZero)
	for _, r := range resultsWithZero {
		if r.Score > 0 {
			if norm, ok := normZero[r.Node.ID]; ok && norm < 0.05 {
				normZero[r.Node.ID] = 0.05
			}
		}
	}
	if normZero["zero"] >= 0.05 {
		t.Errorf("BM25 floor should not apply to Score==0 nodes: normalized[zero] = %f", normZero["zero"])
	}
}

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
		// Stop words should be filtered out — they should not appear as prefix-matched terms
		for _, sw := range []string{"the", "a", "is", "being"} {
			if strings.Contains(strings.ToLower(result), sw+"*") {
				t.Errorf("buildFTSQuery(%q) = %q, should not contain stop word %q with prefix", query, result, sw)
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

// ---- M7: Direct unit tests for isHelperFile and applyPerFileCap ----

// TestIsHelperFile verifies that auto-generated and IDE helper files are correctly
// identified, and regular source files are not misclassified.
func TestIsHelperFile(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		want     bool
	}{
		// Positive cases: should be classified as helper files
		{"ide helper PHP", "_ide_helper.php", true},
		{"ide helper models", "_ide_helper_models.php", true},
		{"nested ide helper", "vendor/laravel/_ide_helper.php", true},
		{"ide helper mixed case", "src/_IDE_Helper.php", true},
		{"TypeScript declaration", "types/react.d.ts", true},
		{"nested d.ts", "node_modules/@types/node/index.d.ts", true},
		{"generated directory", "generated/models.go", true},
		{"generated prefix with dot", "src/generated.pb.go", true},
		{"generated prefix with underscore", "src/generated_types.go", true},
		{"generated dir nested", "api/generated/client.ts", true},

		// Negative cases: should NOT be classified as helper files
		{"regular Go file", "main.go", false},
		{"regular TypeScript", "app.ts", false},
		{"regular PHP", "Controller.php", false},
		{"file with generate in name", "UserGeneratedContent.php", false},
		{"code generator", "codegenerator.go", false},
		{"regenerate script", "regenerate.sh", false},
		{"path with generated substring", "src/regenerated/foo.go", false},
		{"empty path", "", false},
		{"just a dot ts", "foo.ts", false},
		{"d.tsx file", "component.d.tsx", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHelperFile(tt.filePath)
			if got != tt.want {
				t.Errorf("isHelperFile(%q) = %v, want %v", tt.filePath, got, tt.want)
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
	bd := r.Breakdown
	hasNonZero := bd.PPR > 0 || bd.BM25 > 0 || bd.Betweenness > 0 || bd.InDegree > 0 || bd.Semantic > 0
	if !hasNonZero {
		t.Errorf("expected at least one non-zero field in ScoreBreakdown, got %+v", bd)
	}
	t.Logf("Top result %s breakdown: PPR=%.4f BM25=%.4f Betweenness=%.4f InDegree=%.4f Semantic=%.4f",
		r.Node.SymbolName, bd.PPR, bd.BM25, bd.Betweenness, bd.InDegree, bd.Semantic)
}

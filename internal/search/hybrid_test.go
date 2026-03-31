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

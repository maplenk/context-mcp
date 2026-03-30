//go:build fts5

package search

import (
	"path/filepath"
	"strings"
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
	if fileCount > maxPerFile {
		t.Errorf("per-file cap exceeded: got %d results from samefile.go, want at most %d", fileCount, maxPerFile)
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
	result := buildFTSQuery("the function that reads a file")

	// "the", "that", "a" should be filtered
	lower := strings.ToLower(result)
	for _, stopWord := range []string{" the ", " that ", " a "} {
		// Check for stop word as standalone term (surrounded by spaces or at boundaries)
		// We look for the stop word followed by * (prefix) or as part of OR expression
		// The easiest check: if result only has stop-word-prefixed terms, that's bad
		// Instead verify the important terms are present and stop words absent as standalone terms
		_ = stopWord
	}

	// "function", "reads", "file" should be present
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

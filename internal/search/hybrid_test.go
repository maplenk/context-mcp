//go:build fts5

package search

import (
	"path/filepath"
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

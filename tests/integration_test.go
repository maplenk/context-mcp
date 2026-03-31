//go:build fts5

package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/parser"
	"github.com/naman/qb-context/internal/search"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
)

// TestFullPipeline tests the complete indexing and query pipeline
func TestFullPipeline(t *testing.T) {
	// 1. Create a temp directory with sample source files
	tmpDir := t.TempDir()

	// Create a sample Go file
	goContent := `package main

import "fmt"

// Calculator performs arithmetic operations
type Calculator struct {
	Result float64
}

// Add adds two numbers and returns the sum
func (c *Calculator) Add(a, b float64) float64 {
	c.Result = a + b
	return c.Result
}

// Multiply multiplies two numbers
func (c *Calculator) Multiply(a, b float64) float64 {
	c.Result = a * b
	return c.Result
}

func main() {
	calc := Calculator{}
	fmt.Println(calc.Add(1, 2))
	fmt.Println(calc.Multiply(3, 4))
}
`
	goFile := filepath.Join(tmpDir, "calc.go")
	if err := os.WriteFile(goFile, []byte(goContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a sample JS file
	jsContent := `
function validateEmail(email) {
	const regex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
	return regex.test(email);
}

const processUser = (user) => {
	if (validateEmail(user.email)) {
		return { valid: true, user: user };
	}
	return { valid: false, error: "Invalid email" };
}

class UserService {
	constructor() {
		this.users = [];
	}

	addUser(user) {
		const result = processUser(user);
		if (result.valid) {
			this.users.push(result.user);
		}
		return result;
	}
}
`
	jsFile := filepath.Join(tmpDir, "user.js")
	if err := os.WriteFile(jsFile, []byte(jsContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Initialize all components
	dbPath := filepath.Join(tmpDir, ".qb-context", "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	p := parser.New()
	embedder := embedding.NewHashEmbedder()
	defer embedder.Close()
	graphEngine := graph.New()

	// 3. Parse files
	goResult, err := p.ParseFile(goFile, tmpDir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}
	jsResult, err := p.ParseFile(jsFile, tmpDir)
	if err != nil {
		t.Fatalf("ParseFile JS: %v", err)
	}

	// 4. Store nodes and edges
	allNodes := append(goResult.Nodes, jsResult.Nodes...)
	allEdges := append(goResult.Edges, jsResult.Edges...)

	if err := store.UpsertNodes(allNodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// Build a set of known node IDs so we only store edges whose both endpoints exist.
	// The parser generates call edges to external symbols (e.g. fmt.Println) which are
	// not in the DB, and the foreign-key constraint rejects them.
	nodeIDSet := make(map[string]bool, len(allNodes))
	for _, n := range allNodes {
		nodeIDSet[n.ID] = true
	}
	var validEdges []types.ASTEdge
	for _, e := range allEdges {
		if nodeIDSet[e.SourceID] && nodeIDSet[e.TargetID] {
			validEdges = append(validEdges, e)
		}
	}

	if err := store.UpsertEdges(validEdges); err != nil {
		t.Fatalf("UpsertEdges: %v", err)
	}

	// Verify nodes were stored
	t.Logf("Stored %d nodes and %d edges", len(allNodes), len(allEdges))
	if len(allNodes) == 0 {
		t.Fatal("Expected at least 1 node")
	}

	// 5. Generate embeddings
	for _, node := range allNodes {
		vec, err := embedder.Embed(node.ContentSum)
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if err := store.UpsertEmbedding(node.ID, vec); err != nil {
			// vec0 may not be available, that's ok
			t.Logf("UpsertEmbedding skipped (vec0 unavailable): %v", err)
			break
		}
	}

	// 6. Build graph
	edges, err := store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	graphEngine.BuildFromEdges(edges)
	t.Logf("Graph: %d nodes, %d edges", graphEngine.NodeCount(), graphEngine.EdgeCount())

	// L3: Verify graph has edges (connectivity)
	if graphEngine.EdgeCount() == 0 {
		t.Error("expected graph to have edges (connectivity)")
	}

	// 7. Test hybrid search
	hybridSearch := search.New(store, embedder, graphEngine)
	results, err := hybridSearch.Search("calculator add", 5, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	t.Logf("Search 'calculator add' returned %d results", len(results))
	// We expect at least some results from FTS5
	if len(results) == 0 {
		t.Errorf("expected at least 1 search result for 'calculator add', got 0")
	}

	// 8. Test search for JS content
	jsResults, err := hybridSearch.Search("validate email", 5, nil)
	if err != nil {
		t.Fatalf("Search JS: %v", err)
	}
	t.Logf("Search 'validate email' returned %d results", len(jsResults))

	// 9. Test GetNode roundtrip
	for _, node := range allNodes[:1] {
		retrieved, err := store.GetNode(node.ID)
		if err != nil {
			t.Fatalf("GetNode: %v", err)
		}
		if retrieved.SymbolName != node.SymbolName {
			t.Errorf("GetNode name mismatch: got %q, want %q", retrieved.SymbolName, node.SymbolName)
		}
	}

	// 10. Test blast radius (if we have edges)
	// L5: Test multiple edges (removed the break) and verify blast radius doesn't panic
	// BlastRadius traverses INCOMING edges (who calls the given node), so we test
	// on the target (callee) to properly validate that callers are found.
	if graphEngine.EdgeCount() > 0 {
		for _, edge := range edges {
			affected := graphEngine.BlastRadius(edge.TargetID, 3)
			t.Logf("BlastRadius for target %s: %d affected nodes", edge.TargetID[:8], len(affected))
			// Just verifying it doesn't panic for each edge
		}
	}

	// 11. Test DeleteByFile
	if err := store.DeleteByFile("calc.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	// Verify Go nodes are gone
	for _, node := range goResult.Nodes {
		_, err := store.GetNode(node.ID)
		if err == nil {
			t.Errorf("Expected node %s to be deleted", node.SymbolName)
		}
	}
	t.Log("DeleteByFile verified — Go nodes removed")

	t.Log("Integration test passed!")
}

// TestCrossFileEdges verifies that parsing a Go file produces import edges (L2).
func TestCrossFileEdges(t *testing.T) {
	p := parser.New()

	dir := t.TempDir()

	// File with import statement
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`), 0644)

	result, err := p.ParseFile(filepath.Join(dir, "main.go"), dir)
	if err != nil {
		t.Fatal(err)
	}

	// Should have import edges
	hasImportEdge := false
	for _, edge := range result.Edges {
		if edge.EdgeType == types.EdgeTypeImports {
			hasImportEdge = true
			break
		}
	}
	if !hasImportEdge {
		t.Error("expected import edges from Go file")
	}
}

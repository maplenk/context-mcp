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

// testPipeline holds all components needed for incremental update tests.
type testPipeline struct {
	store       *storage.Store
	parser      *parser.Parser
	embedder    embedding.Embedder
	graphEngine *graph.GraphEngine
	search      *search.HybridSearch
	repoRoot    string
}

// newTestPipeline creates a fully initialized pipeline backed by a temp directory.
func newTestPipeline(t *testing.T) *testPipeline {
	t.Helper()
	dir := t.TempDir()

	dbPath := filepath.Join(dir, ".qb-context", "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	p := parser.New()
	embedder := embedding.NewHashEmbedder()
	t.Cleanup(func() { embedder.Close() })
	graphEngine := graph.New()
	hybridSearch := search.New(store, embedder, graphEngine)

	return &testPipeline{
		store:       store,
		parser:      p,
		embedder:    embedder,
		graphEngine: graphEngine,
		search:      hybridSearch,
		repoRoot:    dir,
	}
}

// indexFile simulates the incremental indexing of a single file:
// parse, store nodes/edges, generate embeddings, rebuild graph.
func (tp *testPipeline) indexFile(t *testing.T, relPath string) {
	t.Helper()
	absPath := filepath.Join(tp.repoRoot, relPath)

	result, err := tp.parser.ParseFile(absPath, tp.repoRoot)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", relPath, err)
	}

	if len(result.Nodes) > 0 {
		if err := tp.store.UpsertNodes(result.Nodes); err != nil {
			t.Fatalf("UpsertNodes: %v", err)
		}
	}

	// Filter edges to only those whose both endpoints are known nodes
	nodeIDs, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatalf("GetAllNodeIDs: %v", err)
	}
	knownNodes := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		knownNodes[id] = true
	}
	var validEdges []types.ASTEdge
	for _, e := range result.Edges {
		if knownNodes[e.SourceID] && knownNodes[e.TargetID] {
			validEdges = append(validEdges, e)
		}
	}
	if len(validEdges) > 0 {
		if err := tp.store.UpsertEdges(validEdges); err != nil {
			t.Fatalf("UpsertEdges: %v", err)
		}
	}

	// Generate embeddings
	for _, node := range result.Nodes {
		vec, err := tp.embedder.Embed(node.ContentSum)
		if err != nil {
			t.Logf("Embed warning: %v", err)
			continue
		}
		if err := tp.store.UpsertEmbedding(node.ID, vec); err != nil {
			t.Logf("UpsertEmbedding skipped: %v", err)
			break // vec0 might not be available
		}
	}

	tp.rebuildGraph(t)
}

// deleteFile simulates the incremental deletion of a file:
// remove data from store, rebuild graph.
func (tp *testPipeline) deleteFile(t *testing.T, relPath string) {
	t.Helper()
	if err := tp.store.DeleteByFile(relPath); err != nil {
		t.Fatalf("DeleteByFile(%s): %v", relPath, err)
	}
	tp.rebuildGraph(t)
}

// rebuildGraph loads all edges from the store and rebuilds the in-memory graph.
func (tp *testPipeline) rebuildGraph(t *testing.T) {
	t.Helper()
	edges, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	tp.graphEngine.BuildFromEdges(edges)
}

// writeGoFile writes a Go source file with the given content.
func writeGoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// --- Tests ---

func TestIncremental_AddFile(t *testing.T) {
	tp := newTestPipeline(t)

	// Start with one file
	writeGoFile(t, tp.repoRoot, "calc.go", `package main

// Calculator performs arithmetic
type Calculator struct {
	Result float64
}

// Add sums two numbers
func (c *Calculator) Add(a, b float64) float64 {
	c.Result = a + b
	return c.Result
}
`)
	tp.indexFile(t, "calc.go")

	initialNodeIDs, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	initialNodeCount := len(initialNodeIDs)
	if initialNodeCount == 0 {
		t.Fatal("expected at least 1 node after indexing calc.go")
	}
	t.Logf("After initial index: %d nodes", initialNodeCount)

	// Now add a second file
	writeGoFile(t, tp.repoRoot, "helper.go", `package main

// Helper provides utility functions
type Helper struct{}

// Double doubles a number
func (h *Helper) Double(x float64) float64 {
	return x * 2
}
`)
	tp.indexFile(t, "helper.go")

	afterNodeIDs, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	afterNodeCount := len(afterNodeIDs)
	if afterNodeCount <= initialNodeCount {
		t.Errorf("expected more nodes after adding helper.go: initial=%d, after=%d", initialNodeCount, afterNodeCount)
	}
	t.Logf("After adding helper.go: %d nodes", afterNodeCount)

	// Verify search can find symbols from the new file
	results, err := tp.search.Search("Double", 5, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Node.SymbolName == "Double" || r.Node.SymbolName == "Helper.Double" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected search to find 'Double' from helper.go; got %d results", len(results))
		for _, r := range results {
			t.Logf("  result: %s (%s)", r.Node.SymbolName, r.Node.FilePath)
		}
	}
}

func TestIncremental_ModifyFile(t *testing.T) {
	tp := newTestPipeline(t)

	// Index a file with a function named "Add"
	writeGoFile(t, tp.repoRoot, "calc.go", `package main

// Add sums two numbers
func Add(a, b float64) float64 {
	return a + b
}
`)
	tp.indexFile(t, "calc.go")

	// Verify "Add" exists
	addResults, err := tp.search.Search("Add", 5, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(addResults) == 0 {
		t.Fatal("expected search to find 'Add'")
	}

	// Now modify the file: rename Add to Sum
	writeGoFile(t, tp.repoRoot, "calc.go", `package main

// Sum returns the total of two numbers
func Sum(a, b float64) float64 {
	return a + b
}
`)

	// Simulate incremental update: delete old data, re-parse, re-store
	tp.deleteFile(t, "calc.go")
	tp.indexFile(t, "calc.go")

	// "Sum" should now be found
	sumResults, err := tp.search.Search("Sum", 5, nil)
	if err != nil {
		t.Fatalf("Search Sum: %v", err)
	}
	foundSum := false
	for _, r := range sumResults {
		if r.Node.SymbolName == "Sum" {
			foundSum = true
			break
		}
	}
	if !foundSum {
		t.Error("expected search to find 'Sum' after modification")
	}

	// "Add" should no longer be found (the node was deleted)
	addNodeID := types.GenerateNodeID("calc.go", "Add")
	_, err = tp.store.GetNode(addNodeID)
	if err == nil {
		t.Error("expected 'Add' node to be deleted after modification")
	}
}

func TestIncremental_DeleteFile(t *testing.T) {
	tp := newTestPipeline(t)

	// Index two files
	writeGoFile(t, tp.repoRoot, "calc.go", `package main

// Add sums
func Add(a, b int) int { return a + b }
`)
	writeGoFile(t, tp.repoRoot, "helper.go", `package main

// Triple triples a number
func Triple(x int) int { return x * 3 }
`)

	tp.indexFile(t, "calc.go")
	tp.indexFile(t, "helper.go")

	allNodesBefore, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	nodeCountBefore := len(allNodesBefore)
	t.Logf("Before delete: %d nodes, graph has %d nodes", nodeCountBefore, tp.graphEngine.NodeCount())

	// Delete calc.go from the store (simulating a file deletion event)
	tp.deleteFile(t, "calc.go")

	allNodesAfter, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	nodeCountAfter := len(allNodesAfter)
	if nodeCountAfter >= nodeCountBefore {
		t.Errorf("expected fewer nodes after deleting calc.go: before=%d, after=%d", nodeCountBefore, nodeCountAfter)
	}

	// Verify the "Add" node is gone
	addNodeID := types.GenerateNodeID("calc.go", "Add")
	_, err = tp.store.GetNode(addNodeID)
	if err == nil {
		t.Error("expected 'Add' node to be deleted")
	}

	// Verify edges for calc.go are gone
	edges, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	for _, e := range edges {
		if e.SourceID == addNodeID || e.TargetID == addNodeID {
			t.Error("found an edge referencing a deleted node")
		}
	}

	// Verify Triple is still there
	tripleNodeID := types.GenerateNodeID("helper.go", "Triple")
	tripleNode, err2 := tp.store.GetNode(tripleNodeID)
	if err2 != nil {
		t.Errorf("expected Triple to still exist: %v", err2)
	} else if tripleNode.SymbolName != "Triple" {
		t.Errorf("expected symbol name 'Triple', got %q", tripleNode.SymbolName)
	}
	t.Logf("After delete: %d nodes", nodeCountAfter)

	// M76: Verify FTS entries for deleted nodes are cleaned up
	ftsResults, ftsErr := tp.store.SearchLexical("Add", 10)
	if ftsErr != nil {
		t.Fatalf("SearchLexical after delete: %v", ftsErr)
	}
	for _, r := range ftsResults {
		if r.Node.FilePath == "calc.go" {
			t.Errorf("FTS still contains deleted node from calc.go: %s", r.Node.SymbolName)
		}
	}
}

func TestIncremental_GraphConsistency(t *testing.T) {
	tp := newTestPipeline(t)

	// Create files that reference each other
	writeGoFile(t, tp.repoRoot, "service.go", `package main

// Service handles business logic
type Service struct{}

// Process does processing
func (s *Service) Process() {
	h := Helper{}
	h.Help()
}
`)
	writeGoFile(t, tp.repoRoot, "helper.go", `package main

// Helper provides utilities
type Helper struct{}

// Help does helping
func (h *Helper) Help() {}
`)

	tp.indexFile(t, "service.go")
	tp.indexFile(t, "helper.go")

	// Check initial consistency
	storeNodeIDs, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	storeEdges, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Store: %d nodes, %d edges", len(storeNodeIDs), len(storeEdges))
	t.Logf("Graph: %d nodes, %d edges", tp.graphEngine.NodeCount(), tp.graphEngine.EdgeCount())

	// All edges in the store should reference existing nodes
	nodeSet := make(map[string]bool, len(storeNodeIDs))
	for _, id := range storeNodeIDs {
		nodeSet[id] = true
	}
	for _, e := range storeEdges {
		if !nodeSet[e.SourceID] {
			t.Errorf("edge has source_id %s not in store nodes", e.SourceID[:16])
		}
		if !nodeSet[e.TargetID] {
			t.Errorf("edge has target_id %s not in store nodes", e.TargetID[:16])
		}
	}

	// Now modify helper.go (rename Help to Assist)
	writeGoFile(t, tp.repoRoot, "helper.go", `package main

// Helper provides utilities
type Helper struct{}

// Assist does assisting
func (h *Helper) Assist() {}
`)
	tp.deleteFile(t, "helper.go")
	tp.indexFile(t, "helper.go")

	// Re-verify consistency after modification
	storeNodeIDs2, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	storeEdges2, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatal(err)
	}

	nodeSet2 := make(map[string]bool, len(storeNodeIDs2))
	for _, id := range storeNodeIDs2 {
		nodeSet2[id] = true
	}
	for _, e := range storeEdges2 {
		if !nodeSet2[e.SourceID] {
			t.Errorf("after modify: edge source_id %s not in store nodes", e.SourceID[:16])
		}
		if !nodeSet2[e.TargetID] {
			t.Errorf("after modify: edge target_id %s not in store nodes", e.TargetID[:16])
		}
	}

	// Now delete helper.go
	tp.deleteFile(t, "helper.go")

	storeNodeIDs3, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	storeEdges3, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatal(err)
	}

	nodeSet3 := make(map[string]bool, len(storeNodeIDs3))
	for _, id := range storeNodeIDs3 {
		nodeSet3[id] = true
	}
	for _, e := range storeEdges3 {
		if !nodeSet3[e.SourceID] {
			t.Errorf("after delete: edge source_id %s not in store nodes", e.SourceID[:16])
		}
		if !nodeSet3[e.TargetID] {
			t.Errorf("after delete: edge target_id %s not in store nodes", e.TargetID[:16])
		}
	}

	// Search should still return valid results
	results, err := tp.search.Search("Process Service", 5, nil)
	if err != nil {
		t.Fatalf("Search after modification cycle: %v", err)
	}
	for _, r := range results {
		if !nodeSet3[r.Node.ID] {
			t.Errorf("search returned node %s which is not in the store", r.Node.SymbolName)
		}
	}
	t.Logf("Final state: %d nodes, %d edges, search returned %d results", len(storeNodeIDs3), len(storeEdges3), len(results))
}

func TestIncremental_FullCycle(t *testing.T) {
	// End-to-end: create -> index -> modify -> re-index -> delete -> verify
	tp := newTestPipeline(t)

	// Phase 1: Create and index
	writeGoFile(t, tp.repoRoot, "app.go", `package main

func Start() {}
func Stop() {}
`)
	tp.indexFile(t, "app.go")

	nodeIDs1, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeIDs1) < 2 {
		t.Fatalf("expected at least 2 nodes (Start, Stop), got %d", len(nodeIDs1))
	}

	// Phase 2: Modify — replace Stop with Shutdown
	writeGoFile(t, tp.repoRoot, "app.go", `package main

func Start() {}
func Shutdown() {}
`)
	tp.deleteFile(t, "app.go")
	tp.indexFile(t, "app.go")

	// Verify Shutdown exists, Stop doesn't
	shutdownID := types.GenerateNodeID("app.go", "Shutdown")
	_, err = tp.store.GetNode(shutdownID)
	if err != nil {
		t.Error("expected Shutdown to exist after modification")
	}

	stopID := types.GenerateNodeID("app.go", "Stop")
	_, err = tp.store.GetNode(stopID)
	if err == nil {
		t.Error("expected Stop to be gone after modification")
	}

	// Phase 3: Delete the file entirely
	tp.deleteFile(t, "app.go")

	nodeIDs3, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeIDs3) != 0 {
		t.Errorf("expected 0 nodes after file deletion, got %d", len(nodeIDs3))
	}

	// Graph should be empty
	if tp.graphEngine.NodeCount() != 0 {
		t.Errorf("expected 0 graph nodes after file deletion, got %d", tp.graphEngine.NodeCount())
	}
}

// updateFile simulates processFileEvent for a modified file:
// saves incoming cross-file edges, deletes old data, re-indexes, restores valid incoming edges.
func (tp *testPipeline) updateFile(t *testing.T, relPath string) {
	t.Helper()

	// Save incoming cross-file edges before deletion (mirrors H1 fix in processFileEvent)
	incomingEdges, err := tp.store.GetIncomingCrossFileEdges(relPath)
	if err != nil {
		t.Fatalf("GetIncomingCrossFileEdges(%s): %v", relPath, err)
	}

	// Delete old data
	tp.deleteFile(t, relPath)

	// Re-index
	tp.indexFile(t, relPath)

	// Restore valid incoming cross-file edges
	if len(incomingEdges) > 0 {
		newNodeIDs, niErr := tp.store.GetNodeIDsByFile(relPath)
		if niErr != nil {
			t.Fatalf("GetNodeIDsByFile(%s): %v", relPath, niErr)
		}
		newNodeSet := make(map[string]bool, len(newNodeIDs))
		for _, id := range newNodeIDs {
			newNodeSet[id] = true
		}
		var validIncoming []types.ASTEdge
		for _, e := range incomingEdges {
			if newNodeSet[e.TargetID] {
				validIncoming = append(validIncoming, e)
			}
		}
		if len(validIncoming) > 0 {
			if err := tp.store.UpsertEdges(validIncoming); err != nil {
				t.Fatalf("UpsertEdges (restore incoming): %v", err)
			}
			// Rebuild graph to include restored edges
			tp.rebuildGraph(t)
		}
	}
}

func TestIncremental_CrossFileIncomingEdgesPreserved(t *testing.T) {
	tp := newTestPipeline(t)

	// File A: caller.go — defines a function that calls into target.go
	writeGoFile(t, tp.repoRoot, "caller.go", `package main

// Caller invokes Target
type Caller struct{}

// Run calls DoWork from target
func (c *Caller) Run() {
	t := Target{}
	t.DoWork()
}
`)
	// File B: target.go — defines a function that is called by caller.go
	writeGoFile(t, tp.repoRoot, "target.go", `package main

// Target does work
type Target struct{}

// DoWork does the actual work
func (t *Target) DoWork() {}
`)

	tp.indexFile(t, "caller.go")
	tp.indexFile(t, "target.go")

	// Manually create a cross-file edge: Caller.Run -> Target.DoWork
	callerRunID := types.GenerateNodeID("caller.go", "Caller.Run")
	targetDoWorkID := types.GenerateNodeID("target.go", "Target.DoWork")

	crossEdge := types.ASTEdge{
		SourceID:   callerRunID,
		TargetID:   targetDoWorkID,
		EdgeType:   types.EdgeTypeCalls,
	}
	if err := tp.store.UpsertEdges([]types.ASTEdge{crossEdge}); err != nil {
		t.Fatalf("UpsertEdges (cross-file): %v", err)
	}
	tp.rebuildGraph(t)

	// Verify the cross-file edge exists
	edgesBefore, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	foundCrossEdge := false
	for _, e := range edgesBefore {
		if e.SourceID == callerRunID && e.TargetID == targetDoWorkID {
			foundCrossEdge = true
			break
		}
	}
	if !foundCrossEdge {
		t.Fatal("cross-file edge Caller.Run -> Target.DoWork not found before update")
	}
	t.Logf("Before update: %d edges (cross-file edge present)", len(edgesBefore))

	// Now modify target.go (change DoWork's implementation but keep the symbol name)
	writeGoFile(t, tp.repoRoot, "target.go", `package main

// Target does work
type Target struct{}

// DoWork does the actual work with improvements
func (t *Target) DoWork() {
	// improved implementation
}
`)

	// Use updateFile which preserves incoming cross-file edges (H1 fix)
	tp.updateFile(t, "target.go")

	// Verify the cross-file edge survived the update
	edgesAfter, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges after update: %v", err)
	}
	foundCrossEdgeAfter := false
	for _, e := range edgesAfter {
		if e.SourceID == callerRunID && e.TargetID == targetDoWorkID {
			foundCrossEdgeAfter = true
			break
		}
	}
	if !foundCrossEdgeAfter {
		t.Error("H1 REGRESSION: cross-file edge Caller.Run -> Target.DoWork was lost after modifying target.go")
		t.Logf("After update: %d edges remain", len(edgesAfter))
		for _, e := range edgesAfter {
			t.Logf("  edge: %s -> %s (type %d)", e.SourceID[:16], e.TargetID[:16], e.EdgeType)
		}
	} else {
		t.Log("H1 fix verified: cross-file incoming edge preserved after file modification")
	}
}

func TestIncremental_CrossFileEdgesDroppedWhenSymbolRemoved(t *testing.T) {
	tp := newTestPipeline(t)

	// File A calls into file B
	writeGoFile(t, tp.repoRoot, "caller.go", `package main

func CallFoo() {
	Foo()
}
`)
	writeGoFile(t, tp.repoRoot, "target.go", `package main

func Foo() {}
`)

	tp.indexFile(t, "caller.go")
	tp.indexFile(t, "target.go")

	callerID := types.GenerateNodeID("caller.go", "CallFoo")
	fooID := types.GenerateNodeID("target.go", "Foo")

	crossEdge := types.ASTEdge{
		SourceID: callerID,
		TargetID: fooID,
		EdgeType: types.EdgeTypeCalls,
	}
	if err := tp.store.UpsertEdges([]types.ASTEdge{crossEdge}); err != nil {
		t.Fatalf("UpsertEdges: %v", err)
	}
	tp.rebuildGraph(t)

	// Rename Foo to Bar — the old target node ID no longer exists
	writeGoFile(t, tp.repoRoot, "target.go", `package main

func Bar() {}
`)
	tp.updateFile(t, "target.go")

	// The cross-file edge should NOT be restored because Foo no longer exists
	edgesAfter, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	for _, e := range edgesAfter {
		if e.SourceID == callerID && e.TargetID == fooID {
			t.Error("cross-file edge to renamed symbol should have been dropped, but was restored")
		}
	}
	t.Logf("After rename: %d edges (old cross-file edge correctly dropped)", len(edgesAfter))
}

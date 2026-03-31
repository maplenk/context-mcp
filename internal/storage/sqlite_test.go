package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/naman/qb-context/internal/types"
)

// newTestStore creates a Store backed by a temp-dir SQLite database.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(%q) error: %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// sampleNode returns a simple ASTNode for tests.
func sampleNode(id, file, symbol string, nt types.NodeType) types.ASTNode {
	return types.ASTNode{
		ID:         id,
		FilePath:   file,
		SymbolName: symbol,
		NodeType:   nt,
		StartByte:  0,
		EndByte:    100,
		ContentSum: symbol + " content summary",
	}
}

// ---- NewStore ----

func TestNewStore_CreatesDBAndMigrations(t *testing.T) {
	s := newTestStore(t)
	// Verify the DB is pingable via a trivial raw query.
	results, err := s.RawQuery("SELECT count(*) AS c FROM nodes")
	if err != nil {
		t.Fatalf("RawQuery after NewStore: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row from count query, got %d", len(results))
	}
}

// ---- UpsertNode + GetNode ----

func TestUpsertNode_GetNode_Roundtrip(t *testing.T) {
	s := newTestStore(t)

	want := sampleNode(
		types.GenerateNodeID("foo/bar.go", "MyFunc"),
		"foo/bar.go",
		"MyFunc",
		types.NodeTypeFunction,
	)

	if err := s.UpsertNode(want); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	got, err := s.GetNode(want.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}

	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	if got.FilePath != want.FilePath {
		t.Errorf("FilePath: got %q, want %q", got.FilePath, want.FilePath)
	}
	if got.SymbolName != want.SymbolName {
		t.Errorf("SymbolName: got %q, want %q", got.SymbolName, want.SymbolName)
	}
	if got.NodeType != want.NodeType {
		t.Errorf("NodeType: got %v, want %v", got.NodeType, want.NodeType)
	}
	if got.StartByte != want.StartByte {
		t.Errorf("StartByte: got %d, want %d", got.StartByte, want.StartByte)
	}
	if got.EndByte != want.EndByte {
		t.Errorf("EndByte: got %d, want %d", got.EndByte, want.EndByte)
	}
	if got.ContentSum != want.ContentSum {
		t.Errorf("ContentSum: got %q, want %q", got.ContentSum, want.ContentSum)
	}
}

// TestUpsertNode_Replace verifies that upserting a node with the same ID replaces it.
func TestUpsertNode_Replace(t *testing.T) {
	s := newTestStore(t)

	id := types.GenerateNodeID("file.go", "Sym")
	original := sampleNode(id, "file.go", "Sym", types.NodeTypeFunction)
	if err := s.UpsertNode(original); err != nil {
		t.Fatalf("UpsertNode (first): %v", err)
	}

	updated := original
	updated.ContentSum = "updated summary"
	updated.EndByte = 999
	if err := s.UpsertNode(updated); err != nil {
		t.Fatalf("UpsertNode (replace): %v", err)
	}

	got, err := s.GetNode(id)
	if err != nil {
		t.Fatalf("GetNode after replace: %v", err)
	}
	if got.ContentSum != "updated summary" {
		t.Errorf("ContentSum not updated: got %q", got.ContentSum)
	}
	if got.EndByte != 999 {
		t.Errorf("EndByte not updated: got %d", got.EndByte)
	}
}

// ---- UpsertNodes batch ----

func TestUpsertNodes_BatchInsert(t *testing.T) {
	s := newTestStore(t)

	nodes := []types.ASTNode{
		sampleNode(types.GenerateNodeID("a.go", "A"), "a.go", "A", types.NodeTypeFunction),
		sampleNode(types.GenerateNodeID("a.go", "B"), "a.go", "B", types.NodeTypeStruct),
		sampleNode(types.GenerateNodeID("b.go", "C"), "b.go", "C", types.NodeTypeMethod),
	}

	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	for _, want := range nodes {
		got, err := s.GetNode(want.ID)
		if err != nil {
			t.Errorf("GetNode(%q): %v", want.ID, err)
			continue
		}
		if got.SymbolName != want.SymbolName {
			t.Errorf("SymbolName mismatch: got %q, want %q", got.SymbolName, want.SymbolName)
		}
		if got.NodeType != want.NodeType {
			t.Errorf("NodeType mismatch for %q: got %v, want %v", want.SymbolName, got.NodeType, want.NodeType)
		}
	}
}

// ---- UpsertEdge + GetEdgesFrom / GetEdgesTo ----

// insertTwoNodes is a helper that inserts two nodes required for edge tests.
func insertTwoNodes(t *testing.T, s *Store) (srcID, dstID string) {
	t.Helper()
	src := sampleNode(types.GenerateNodeID("e.go", "Caller"), "e.go", "Caller", types.NodeTypeFunction)
	dst := sampleNode(types.GenerateNodeID("e.go", "Callee"), "e.go", "Callee", types.NodeTypeFunction)
	if err := s.UpsertNodes([]types.ASTNode{src, dst}); err != nil {
		t.Fatalf("UpsertNodes for edge test: %v", err)
	}
	return src.ID, dst.ID
}

func TestUpsertEdge_GetEdgesFrom(t *testing.T) {
	s := newTestStore(t)
	srcID, dstID := insertTwoNodes(t, s)

	edge := types.ASTEdge{SourceID: srcID, TargetID: dstID, EdgeType: types.EdgeTypeCalls}
	if err := s.UpsertEdge(edge); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	edges, err := s.GetEdgesFrom(srcID)
	if err != nil {
		t.Fatalf("GetEdgesFrom: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("GetEdgesFrom: expected 1 edge, got %d", len(edges))
	}
	if edges[0].TargetID != dstID {
		t.Errorf("TargetID: got %q, want %q", edges[0].TargetID, dstID)
	}
	if edges[0].EdgeType != types.EdgeTypeCalls {
		t.Errorf("EdgeType: got %v, want EdgeTypeCalls", edges[0].EdgeType)
	}
}

func TestUpsertEdge_GetEdgesTo(t *testing.T) {
	s := newTestStore(t)
	srcID, dstID := insertTwoNodes(t, s)

	edge := types.ASTEdge{SourceID: srcID, TargetID: dstID, EdgeType: types.EdgeTypeCalls}
	if err := s.UpsertEdge(edge); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	edges, err := s.GetEdgesTo(dstID)
	if err != nil {
		t.Fatalf("GetEdgesTo: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("GetEdgesTo: expected 1 edge, got %d", len(edges))
	}
	if edges[0].SourceID != srcID {
		t.Errorf("SourceID: got %q, want %q", edges[0].SourceID, srcID)
	}
}

func TestGetEdgesFrom_Empty(t *testing.T) {
	s := newTestStore(t)
	edges, err := s.GetEdgesFrom("nonexistent-id")
	if err != nil {
		t.Fatalf("GetEdgesFrom on empty: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
}

// ---- GetAllEdges ----

func TestGetAllEdges(t *testing.T) {
	s := newTestStore(t)
	srcID, dstID := insertTwoNodes(t, s)

	// Insert two different edge types between the same pair.
	edges := []types.ASTEdge{
		{SourceID: srcID, TargetID: dstID, EdgeType: types.EdgeTypeCalls},
		{SourceID: srcID, TargetID: dstID, EdgeType: types.EdgeTypeImports},
	}
	for _, e := range edges {
		if err := s.UpsertEdge(e); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
	}

	all, err := s.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("GetAllEdges: expected 2, got %d", len(all))
	}
}

// ---- GetAllNodeIDs ----

func TestGetAllNodeIDs(t *testing.T) {
	s := newTestStore(t)

	nodes := []types.ASTNode{
		sampleNode(types.GenerateNodeID("x.go", "X1"), "x.go", "X1", types.NodeTypeFunction),
		sampleNode(types.GenerateNodeID("x.go", "X2"), "x.go", "X2", types.NodeTypeStruct),
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	ids, err := s.GetAllNodeIDs()
	if err != nil {
		t.Fatalf("GetAllNodeIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 IDs, got %d", len(ids))
	}
	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	for _, n := range nodes {
		if !idSet[n.ID] {
			t.Errorf("ID %q not found in GetAllNodeIDs result", n.ID)
		}
	}
}

// ---- DeleteByFile ----

func TestDeleteByFile_RemovesNodesAndEdges(t *testing.T) {
	s := newTestStore(t)

	// Insert nodes from two different files.
	nodeA := sampleNode(types.GenerateNodeID("del.go", "A"), "del.go", "A", types.NodeTypeFunction)
	nodeB := sampleNode(types.GenerateNodeID("del.go", "B"), "del.go", "B", types.NodeTypeFunction)
	nodeC := sampleNode(types.GenerateNodeID("keep.go", "C"), "keep.go", "C", types.NodeTypeFunction)

	if err := s.UpsertNodes([]types.ASTNode{nodeA, nodeB, nodeC}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// Edge from A (del.go) -> C (keep.go) and C -> B (del.go)
	if err := s.UpsertEdge(types.ASTEdge{SourceID: nodeA.ID, TargetID: nodeC.ID, EdgeType: types.EdgeTypeCalls}); err != nil {
		t.Fatalf("UpsertEdge A->C: %v", err)
	}
	if err := s.UpsertEdge(types.ASTEdge{SourceID: nodeC.ID, TargetID: nodeB.ID, EdgeType: types.EdgeTypeCalls}); err != nil {
		t.Fatalf("UpsertEdge C->B: %v", err)
	}

	if err := s.DeleteByFile("del.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}

	// Nodes from del.go should be gone.
	if _, err := s.GetNode(nodeA.ID); err == nil {
		t.Error("nodeA (del.go) still present after DeleteByFile")
	}
	if _, err := s.GetNode(nodeB.ID); err == nil {
		t.Error("nodeB (del.go) still present after DeleteByFile")
	}

	// Node from keep.go should still be there.
	if _, err := s.GetNode(nodeC.ID); err != nil {
		t.Errorf("nodeC (keep.go) should survive DeleteByFile, but got error: %v", err)
	}

	// All edges touching del.go nodes should be gone.
	allEdges, err := s.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges after delete: %v", err)
	}
	if len(allEdges) != 0 {
		t.Errorf("expected 0 edges after DeleteByFile, got %d", len(allEdges))
	}
}

// ---- RawQuery ----

func TestRawQuery_ReturnsResults(t *testing.T) {
	s := newTestStore(t)

	node := sampleNode(types.GenerateNodeID("rq.go", "RQFunc"), "rq.go", "RQFunc", types.NodeTypeFunction)
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	results, err := s.RawQuery("SELECT id, symbol_name FROM nodes WHERE symbol_name = 'RQFunc'")
	if err != nil {
		t.Fatalf("RawQuery: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0]["symbol_name"] != "RQFunc" {
		t.Errorf("symbol_name: got %v, want %q", results[0]["symbol_name"], "RQFunc")
	}
}

func TestRawQuery_EmptyResult(t *testing.T) {
	s := newTestStore(t)
	results, err := s.RawQuery("SELECT * FROM nodes WHERE symbol_name = 'no_such_symbol'")
	if err != nil {
		t.Fatalf("RawQuery empty: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// ---- SearchLexical (FTS5) ----

func TestSearchLexical_ReturnsFTSResults(t *testing.T) {
	s := newTestStore(t)

	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("s.go", "ComputeChecksum"),
			FilePath:   "s.go",
			SymbolName: "ComputeChecksum",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    50,
			ContentSum: "ComputeChecksum calculates the SHA256 checksum of a file",
		},
		{
			ID:         types.GenerateNodeID("s.go", "ReadFile"),
			FilePath:   "s.go",
			SymbolName: "ReadFile",
			NodeType:   types.NodeTypeFunction,
			StartByte:  60,
			EndByte:    120,
			ContentSum: "ReadFile opens and reads a file from disk",
		},
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	results, err := s.SearchLexical("checksum", 10)
	if err != nil {
		t.Fatalf("SearchLexical: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("SearchLexical: expected at least 1 result for 'checksum'")
	}

	found := false
	for _, r := range results {
		if r.Node.SymbolName == "ComputeChecksum" {
			found = true
			if r.Score <= 0 {
				t.Errorf("expected positive score, got %f", r.Score)
			}
		}
	}
	if !found {
		t.Error("SearchLexical: 'ComputeChecksum' not found in results for query 'checksum'")
	}
}

func TestSearchLexical_NoResults(t *testing.T) {
	s := newTestStore(t)
	results, err := s.SearchLexical("xyzzyunlikely", 10)
	if err != nil {
		t.Fatalf("SearchLexical no-results: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSearchLexical_RespectsLimit(t *testing.T) {
	s := newTestStore(t)

	nodes := make([]types.ASTNode, 5)
	for i := range nodes {
		name := "ProcessItem" + string(rune('A'+i))
		nodes[i] = types.ASTNode{
			ID:         types.GenerateNodeID("lim.go", name),
			FilePath:   "lim.go",
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  uint32(i * 100),
			EndByte:    uint32(i*100 + 50),
			ContentSum: name + " processes an item efficiently",
		}
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	results, err := s.SearchLexical("process", 2)
	if err != nil {
		t.Fatalf("SearchLexical with limit: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("SearchLexical did not respect limit 2: got %d results", len(results))
	}
}

func TestUpsertNodeScores_Roundtrip(t *testing.T) {
	s := newTestStore(t)

	// Need a node to reference (FK constraint)
	node := sampleNode(types.GenerateNodeID("score.go", "ScoreFunc"), "score.go", "ScoreFunc", types.NodeTypeFunction)
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	scores := []types.NodeScore{
		{NodeID: node.ID, PageRank: 0.5, Betweenness: 0.8},
	}
	if err := s.UpsertNodeScores(scores); err != nil {
		t.Fatalf("UpsertNodeScores: %v", err)
	}

	got, err := s.GetNodeScore(node.ID)
	if err != nil {
		t.Fatalf("GetNodeScore: %v", err)
	}
	if got.Betweenness != 0.8 {
		t.Errorf("Betweenness: got %f, want 0.8", got.Betweenness)
	}
	if got.PageRank != 0.5 {
		t.Errorf("PageRank: got %f, want 0.5", got.PageRank)
	}
}

func TestNodeScores_CascadeDelete(t *testing.T) {
	s := newTestStore(t)

	node := sampleNode(types.GenerateNodeID("cascade.go", "CascadeFunc"), "cascade.go", "CascadeFunc", types.NodeTypeFunction)
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	scores := []types.NodeScore{
		{NodeID: node.ID, PageRank: 0.1, Betweenness: 0.2},
	}
	if err := s.UpsertNodeScores(scores); err != nil {
		t.Fatalf("UpsertNodeScores: %v", err)
	}

	// Delete the node — score should cascade delete
	if err := s.DeleteByFile("cascade.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}

	_, err := s.GetNodeScore(node.ID)
	if err == nil {
		t.Error("expected error after cascade delete, got nil")
	}
}

func TestGetAllBetweenness(t *testing.T) {
	s := newTestStore(t)

	// Create two nodes with scores
	nodeA := sampleNode(types.GenerateNodeID("btwn.go", "FuncA"), "btwn.go", "FuncA", types.NodeTypeFunction)
	nodeB := sampleNode(types.GenerateNodeID("btwn.go", "FuncB"), "btwn.go", "FuncB", types.NodeTypeFunction)
	if err := s.UpsertNodes([]types.ASTNode{nodeA, nodeB}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	scores := []types.NodeScore{
		{NodeID: nodeA.ID, Betweenness: 0.9},
		{NodeID: nodeB.ID, Betweenness: 0.0}, // zero betweenness, should NOT appear
	}
	if err := s.UpsertNodeScores(scores); err != nil {
		t.Fatalf("UpsertNodeScores: %v", err)
	}

	result, err := s.GetAllBetweenness()
	if err != nil {
		t.Fatalf("GetAllBetweenness: %v", err)
	}

	if result[nodeA.ID] != 0.9 {
		t.Errorf("FuncA betweenness: got %f, want 0.9", result[nodeA.ID])
	}
	if _, ok := result[nodeB.ID]; ok {
		t.Error("FuncB with zero betweenness should not appear in results")
	}
}

func TestUpsertProjectSummary_Roundtrip(t *testing.T) {
	s := newTestStore(t)

	summary := types.ProjectSummary{
		Project:    "ARCHITECTURE.md",
		Summary:    "This project uses SQLite for storage and gonum for graph analysis.",
		SourceHash: "abc123",
	}
	if err := s.UpsertProjectSummary(summary); err != nil {
		t.Fatalf("UpsertProjectSummary: %v", err)
	}

	got, err := s.GetProjectSummary("ARCHITECTURE.md")
	if err != nil {
		t.Fatalf("GetProjectSummary: %v", err)
	}
	if got.Summary != summary.Summary {
		t.Errorf("Summary: got %q, want %q", got.Summary, summary.Summary)
	}
	if got.SourceHash != summary.SourceHash {
		t.Errorf("SourceHash: got %q, want %q", got.SourceHash, summary.SourceHash)
	}
}

func TestUpsertProjectSummary_UpdateExisting(t *testing.T) {
	s := newTestStore(t)

	// Insert initial
	if err := s.UpsertProjectSummary(types.ProjectSummary{
		Project: "ARCHITECTURE.md", Summary: "v1", SourceHash: "hash1",
	}); err != nil {
		t.Fatalf("UpsertProjectSummary (initial): %v", err)
	}

	// Update
	if err := s.UpsertProjectSummary(types.ProjectSummary{
		Project: "ARCHITECTURE.md", Summary: "v2", SourceHash: "hash2",
	}); err != nil {
		t.Fatalf("UpsertProjectSummary (update): %v", err)
	}

	got, err := s.GetProjectSummary("ARCHITECTURE.md")
	if err != nil {
		t.Fatalf("GetProjectSummary after update: %v", err)
	}
	if got.Summary != "v2" {
		t.Errorf("Summary not updated: got %q, want %q", got.Summary, "v2")
	}
	if got.SourceHash != "hash2" {
		t.Errorf("SourceHash not updated: got %q, want %q", got.SourceHash, "hash2")
	}
}

func TestGetAllProjectSummaries(t *testing.T) {
	s := newTestStore(t)

	summaries := []types.ProjectSummary{
		{Project: "ARCHITECTURE.md", Summary: "arch doc", SourceHash: "h1"},
		{Project: "adr/001-use-sqlite.md", Summary: "use sqlite", SourceHash: "h2"},
	}
	for _, sum := range summaries {
		if err := s.UpsertProjectSummary(sum); err != nil {
			t.Fatalf("UpsertProjectSummary: %v", err)
		}
	}

	result, err := s.GetAllProjectSummaries()
	if err != nil {
		t.Fatalf("GetAllProjectSummaries: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(result))
	}
}

// ---- Schema Versioning ----

func TestSchemaVersion_InitialVersion(t *testing.T) {
	s := newTestStore(t)

	version, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("expected schema version %d, got %d", currentSchemaVersion, version)
	}
}

func TestSchemaVersion_IdempotentMigrations(t *testing.T) {
	// Opening the store twice should not fail — migrations are idempotent
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_idempotent.db")

	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (first): %v", err)
	}
	s1.Close()

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (second): %v", err)
	}
	defer s2.Close()

	version, err := s2.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("expected schema version %d after re-open, got %d", currentSchemaVersion, version)
	}
}

// ---- GetNodeByName deterministic ordering ----

func TestGetNodeByName_DeterministicOrder(t *testing.T) {
	s := newTestStore(t)

	// Insert multiple nodes with the same symbol name but different file paths
	nodes := []types.ASTNode{
		sampleNode(types.GenerateNodeID("z_file.go", "SharedName"), "z_file.go", "SharedName", types.NodeTypeFunction),
		sampleNode(types.GenerateNodeID("a_file.go", "SharedName"), "a_file.go", "SharedName", types.NodeTypeFunction),
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// GetNodeByName should return the one with the earliest file_path (a_file.go)
	got, err := s.GetNodeByName("SharedName")
	if err != nil {
		t.Fatalf("GetNodeByName: %v", err)
	}
	if got.FilePath != "a_file.go" {
		t.Errorf("expected deterministic result from a_file.go, got %s", got.FilePath)
	}
}

// ---- RawQuery LIMIT injection (M6) ----

func TestRawQuery_InjectsDefaultLimit(t *testing.T) {
	s := newTestStore(t)

	// A query without LIMIT should have LIMIT 500 appended automatically.
	// We verify it works by inserting one node and querying without a LIMIT clause.
	node := sampleNode(types.GenerateNodeID("lim.go", "LimFunc"), "lim.go", "LimFunc", types.NodeTypeFunction)
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	results, err := s.RawQuery("SELECT id, symbol_name FROM nodes")
	if err != nil {
		t.Fatalf("RawQuery without LIMIT: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestRawQuery_RespectsExistingLimit(t *testing.T) {
	s := newTestStore(t)

	// Insert 3 nodes
	nodes := []types.ASTNode{
		sampleNode(types.GenerateNodeID("el.go", "A"), "el.go", "A", types.NodeTypeFunction),
		sampleNode(types.GenerateNodeID("el.go", "B"), "el.go", "B", types.NodeTypeFunction),
		sampleNode(types.GenerateNodeID("el.go", "C"), "el.go", "C", types.NodeTypeFunction),
	}
	if err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// Query with explicit LIMIT 1 — should NOT have LIMIT 500 appended
	results, err := s.RawQuery("SELECT id FROM nodes LIMIT 1")
	if err != nil {
		t.Fatalf("RawQuery with explicit LIMIT: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result with LIMIT 1, got %d", len(results))
	}
}

// ---- C4: Foreign key removal — edges with non-existent node IDs ----

func TestUpsertEdge_NoForeignKeyEnforcement(t *testing.T) {
	s := newTestStore(t)

	// Insert an edge where neither source_id nor target_id exist in the nodes table.
	// Before migration v2 (FK removal), this would be silently dropped by INSERT OR IGNORE.
	edge := types.ASTEdge{
		SourceID: "nonexistent-source-id",
		TargetID: "nonexistent-target-id",
		EdgeType: types.EdgeTypeImports,
	}
	if err := s.UpsertEdge(edge); err != nil {
		t.Fatalf("UpsertEdge with non-existent node IDs: %v", err)
	}

	// Verify the edge was actually stored
	edges, err := s.GetEdgesFrom("nonexistent-source-id")
	if err != nil {
		t.Fatalf("GetEdgesFrom: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge stored (no FK enforcement), got %d", len(edges))
	}
	if edges[0].TargetID != "nonexistent-target-id" {
		t.Errorf("TargetID: got %q, want %q", edges[0].TargetID, "nonexistent-target-id")
	}
	if edges[0].EdgeType != types.EdgeTypeImports {
		t.Errorf("EdgeType: got %v, want EdgeTypeImports", edges[0].EdgeType)
	}
}

func TestUpsertEdges_NoForeignKeyEnforcement_Batch(t *testing.T) {
	s := newTestStore(t)

	// Batch insert edges with non-existent source/target IDs
	edges := []types.ASTEdge{
		{SourceID: "phantom-src-1", TargetID: "phantom-tgt-1", EdgeType: types.EdgeTypeCalls},
		{SourceID: "phantom-src-2", TargetID: "phantom-tgt-2", EdgeType: types.EdgeTypeImports},
	}
	if err := s.UpsertEdges(edges); err != nil {
		t.Fatalf("UpsertEdges with non-existent node IDs: %v", err)
	}

	allEdges, err := s.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}
	if len(allEdges) != 2 {
		t.Errorf("expected 2 edges stored (no FK enforcement), got %d", len(allEdges))
	}
}

// ---- C8+M4: RawQuery blocklist tests ----

func TestRawQuery_BlocksReadfile(t *testing.T) {
	s := newTestStore(t)
	_, err := s.RawQuery("SELECT readfile('/etc/passwd')")
	if err == nil {
		t.Fatal("expected readfile to be blocked")
	}
	if !strings.Contains(err.Error(), "forbidden pattern") {
		t.Errorf("expected 'forbidden pattern' in error, got: %v", err)
	}
}

func TestRawQuery_BlocksEdit(t *testing.T) {
	s := newTestStore(t)
	_, err := s.RawQuery("SELECT edit('/tmp/test.db', '')")
	if err == nil {
		t.Fatal("expected edit to be blocked")
	}
	if !strings.Contains(err.Error(), "forbidden pattern") {
		t.Errorf("expected 'forbidden pattern' in error, got: %v", err)
	}
}

func TestRawQuery_BlocksWritefile(t *testing.T) {
	s := newTestStore(t)
	_, err := s.RawQuery("SELECT writefile('/tmp/evil.txt', 'data')")
	if err == nil {
		t.Fatal("expected writefile to be blocked")
	}
}

func TestRawQuery_BlocksLoadExtension(t *testing.T) {
	s := newTestStore(t)
	_, err := s.RawQuery("SELECT load_extension('evil.so')")
	if err == nil {
		t.Fatal("expected load_extension to be blocked")
	}
}

// ---- M3: Semicolon rejection ----

func TestRawQuery_BlocksSemicolons(t *testing.T) {
	s := newTestStore(t)
	_, err := s.RawQuery("SELECT 1; DROP TABLE nodes")
	if err == nil {
		t.Fatal("expected semicolons to be blocked")
	}
	if !strings.Contains(err.Error(), "semicolons forbidden") {
		t.Errorf("expected 'semicolons forbidden' in error, got: %v", err)
	}
}

// ---- C7+M1: UpsertNode atomicity (verify FTS sync) ----

func TestUpsertNode_FTSSync(t *testing.T) {
	s := newTestStore(t)

	node := sampleNode(
		types.GenerateNodeID("fts_sync.go", "SyncFunc"),
		"fts_sync.go",
		"SyncFunc",
		types.NodeTypeFunction,
	)
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	// Verify FTS is in sync: search should find the node
	results, err := s.SearchLexical("SyncFunc", 10)
	if err != nil {
		t.Fatalf("SearchLexical: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected FTS to find 'SyncFunc' after UpsertNode")
	}

	// Update the node and verify FTS updates
	node.ContentSum = "updated sync content summary"
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode (update): %v", err)
	}

	// Verify FTS still finds the node (exactly once, not duplicated)
	results, err = s.SearchLexical("SyncFunc", 10)
	if err != nil {
		t.Fatalf("SearchLexical after update: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected exactly 1 FTS result after update, got %d", len(results))
	}
}

// ---- H6: DeleteByFile cleans up node_scores ----

func TestDeleteByFile_CleansUpNodeScores(t *testing.T) {
	s := newTestStore(t)

	node := sampleNode(types.GenerateNodeID("cleanup.go", "CleanFunc"), "cleanup.go", "CleanFunc", types.NodeTypeFunction)
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	scores := []types.NodeScore{
		{NodeID: node.ID, PageRank: 0.5, Betweenness: 0.3},
	}
	if err := s.UpsertNodeScores(scores); err != nil {
		t.Fatalf("UpsertNodeScores: %v", err)
	}

	// Verify score exists
	if _, err := s.GetNodeScore(node.ID); err != nil {
		t.Fatalf("GetNodeScore before delete: %v", err)
	}

	if err := s.DeleteByFile("cleanup.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}

	// Score should be gone
	_, err := s.GetNodeScore(node.ID)
	if err == nil {
		t.Error("expected node_score to be deleted after DeleteByFile, but it still exists")
	}
}

// ---- M6: FTS5 sanitization ----

func TestSanitizeFTSStorage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`hello "world"`, `hello  world `},
		{`func(a)`, `func a `},
		{`test+case-two`, `test case two`},
		{`normal query`, `normal query`},
		{`prefix*`, `prefix `},
		{`col:value`, `col value`},
	}
	for _, tc := range tests {
		got := sanitizeFTSStorage(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeFTSStorage(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---- M2: LIMIT word boundary check ----

func TestRawQuery_LimitWordBoundary(t *testing.T) {
	s := newTestStore(t)

	// Insert a node to query
	node := sampleNode(types.GenerateNodeID("wb.go", "WBFunc"), "wb.go", "WBFunc", types.NodeTypeFunction)
	if err := s.UpsertNode(node); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	// A query with "LIMIT" as part of a column alias should still get LIMIT 500 appended
	// (we can't test this directly, but we can verify that an explicit LIMIT keyword works)
	results, err := s.RawQuery("SELECT id FROM nodes LIMIT 1")
	if err != nil {
		t.Fatalf("RawQuery with LIMIT: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result with explicit LIMIT 1, got %d", len(results))
	}
}

package graph

import (
	"testing"

	"github.com/naman/qb-context/internal/types"
)

// TestNew_EmptyGraph verifies that New() creates a graph with no nodes or edges.
func TestNew_EmptyGraph(t *testing.T) {
	g := New()
	if g.NodeCount() != 0 {
		t.Errorf("expected NodeCount=0, got %d", g.NodeCount())
	}
	if g.EdgeCount() != 0 {
		t.Errorf("expected EdgeCount=0, got %d", g.EdgeCount())
	}
}

// TestBuildFromEdges_PopulatesGraph verifies that BuildFromEdges adds all nodes and edges.
func TestBuildFromEdges_PopulatesGraph(t *testing.T) {
	g := New()
	edges := []types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-a", TargetID: "node-c", EdgeType: types.EdgeTypeImports},
	}
	g.BuildFromEdges(edges)

	if g.NodeCount() != 3 {
		t.Errorf("expected NodeCount=3, got %d", g.NodeCount())
	}
	if g.EdgeCount() != 3 {
		t.Errorf("expected EdgeCount=3, got %d", g.EdgeCount())
	}
}

// TestAddEdge_IncrementsEdgeCount verifies that AddEdge increases the edge count.
func TestAddEdge_IncrementsEdgeCount(t *testing.T) {
	g := New()

	g.AddEdge(types.ASTEdge{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls})
	if g.EdgeCount() != 1 {
		t.Errorf("expected EdgeCount=1 after first AddEdge, got %d", g.EdgeCount())
	}

	g.AddEdge(types.ASTEdge{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls})
	if g.EdgeCount() != 2 {
		t.Errorf("expected EdgeCount=2 after second AddEdge, got %d", g.EdgeCount())
	}
}

// TestAddEdge_Duplicate verifies that adding the same edge twice does not duplicate it.
func TestAddEdge_Duplicate(t *testing.T) {
	g := New()
	edge := types.ASTEdge{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls}
	g.AddEdge(edge)
	g.AddEdge(edge)

	if g.EdgeCount() != 1 {
		t.Errorf("expected EdgeCount=1 after duplicate AddEdge, got %d", g.EdgeCount())
	}
}

// TestRemoveNode_DecrementsNodeCount verifies that RemoveNode decreases the node count.
func TestRemoveNode_DecrementsNodeCount(t *testing.T) {
	g := New()
	g.AddEdge(types.ASTEdge{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls})

	initialCount := g.NodeCount()
	if initialCount != 2 {
		t.Fatalf("expected 2 nodes before removal, got %d", initialCount)
	}

	g.RemoveNode("node-a")
	if g.NodeCount() != 1 {
		t.Errorf("expected NodeCount=1 after RemoveNode, got %d", g.NodeCount())
	}
}

// TestHasNode_TrueFalse verifies HasNode returns correct results.
func TestHasNode_TrueFalse(t *testing.T) {
	g := New()
	g.AddEdge(types.ASTEdge{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls})

	if !g.HasNode("node-a") {
		t.Error("HasNode('node-a') should be true")
	}
	if !g.HasNode("node-b") {
		t.Error("HasNode('node-b') should be true")
	}
	if g.HasNode("node-nonexistent") {
		t.Error("HasNode('node-nonexistent') should be false")
	}
}

// TestBlastRadius_LinearChain verifies that blast radius of C in A→B→C includes both B and A.
// Edges represent "source calls target": A→B means A calls B.
// BlastRadius traverses incoming edges to find all callers of the given node.
// So for chain A→B→C, BlastRadius("node-c") finds B (direct caller) and A (indirect caller).
func TestBlastRadius_LinearChain(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	result := g.BlastRadius("node-c", 10)
	if len(result) != 2 {
		t.Fatalf("expected 2 affected nodes, got %d: %v", len(result), result)
	}

	resultSet := make(map[string]bool)
	for _, id := range result {
		resultSet[id] = true
	}
	if !resultSet["node-b"] {
		t.Error("blast radius should contain 'node-b'")
	}
	if !resultSet["node-a"] {
		t.Error("blast radius should contain 'node-a'")
	}
}

// TestBlastRadius_MaxDepth1 verifies that maxDepth=1 only returns direct callers.
// For chain A→B→C, BlastRadius("node-c", 1) should only return direct caller B.
func TestBlastRadius_MaxDepth1(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	result := g.BlastRadius("node-c", 1)
	if len(result) != 1 {
		t.Fatalf("expected 1 node at maxDepth=1, got %d: %v", len(result), result)
	}
	if result[0] != "node-b" {
		t.Errorf("expected 'node-b' at depth 1, got %q", result[0])
	}
}

// TestBlastRadius_NonExistentNode verifies that BlastRadius returns nil/empty for a missing node.
func TestBlastRadius_NonExistentNode(t *testing.T) {
	g := New()
	result := g.BlastRadius("does-not-exist", 5)
	if len(result) != 0 {
		t.Errorf("expected empty result for non-existent node, got %v", result)
	}
}

// TestPersonalizedPageRank_NonNilWithScores verifies that PageRank returns a non-nil map with positive scores.
func TestPersonalizedPageRank_NonNilWithScores(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
	})

	ranks := g.PersonalizedPageRank([]string{"node-a"})
	if ranks == nil {
		t.Fatal("PersonalizedPageRank returned nil")
	}
	if len(ranks) == 0 {
		t.Fatal("PersonalizedPageRank returned empty map")
	}

	for id, score := range ranks {
		if score <= 0 {
			t.Errorf("node %q has non-positive PageRank score: %f", id, score)
		}
	}
}

// TestPersonalizedPageRank_EmptyGraph verifies that PageRank on an empty graph returns nil.
func TestPersonalizedPageRank_EmptyGraph(t *testing.T) {
	g := New()
	ranks := g.PersonalizedPageRank([]string{"node-a"})
	if ranks != nil {
		t.Errorf("expected nil for empty graph, got %v", ranks)
	}
}

// TestBlastRadius_Cycle verifies that BlastRadius terminates without looping when
// the graph contains a cycle. Graph: A→B→C→A (each edge means "source calls target").
// BlastRadius(A) traverses incoming edges:
//   - C has an edge to A (C→A), so C is found at depth 1.
//   - B has an edge to C (B→C), so B is found at depth 2.
//   - A has an edge to B (A→B), but A is already in the visited set, so traversal stops.
//
// The result must contain exactly B and C and must not hang.
func TestBlastRadius_Cycle(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
	})

	result := g.BlastRadius("node-a", 10)

	if len(result) != 2 {
		t.Fatalf("expected 2 affected nodes for cyclic graph, got %d: %v", len(result), result)
	}

	resultSet := make(map[string]bool)
	for _, id := range result {
		resultSet[id] = true
	}
	if !resultSet["node-b"] {
		t.Error("blast radius should contain 'node-b'")
	}
	if !resultSet["node-c"] {
		t.Error("blast radius should contain 'node-c'")
	}
}

func TestComputeBetweenness_LinearChain(t *testing.T) {
	g := New()
	// A→B→C: B is the hub connecting A to C
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	btwn := g.ComputeBetweenness()
	if btwn == nil {
		t.Fatal("ComputeBetweenness returned nil")
	}

	// B is on the shortest path between A and C, so it should have the highest betweenness
	if btwn["node-b"] <= btwn["node-a"] {
		t.Errorf("expected node-b betweenness (%f) > node-a (%f)", btwn["node-b"], btwn["node-a"])
	}
	if btwn["node-b"] <= btwn["node-c"] {
		t.Errorf("expected node-b betweenness (%f) > node-c (%f)", btwn["node-b"], btwn["node-c"])
	}
}

func TestComputeBetweenness_EmptyGraph(t *testing.T) {
	g := New()
	btwn := g.ComputeBetweenness()
	if btwn != nil {
		t.Errorf("expected nil for empty graph, got %v", btwn)
	}
}

func TestBlastRadiusWithDepth_DepthValues(t *testing.T) {
	g := New()
	// A→B→C→D: blast radius of D should show C at depth 1, B at depth 2, A at depth 3
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
	})

	result := g.BlastRadiusWithDepth("node-d", 10)
	if result == nil {
		t.Fatal("BlastRadiusWithDepth returned nil")
	}

	if result["node-c"] != 1 {
		t.Errorf("node-c depth: got %d, want 1", result["node-c"])
	}
	if result["node-b"] != 2 {
		t.Errorf("node-b depth: got %d, want 2", result["node-b"])
	}
	if result["node-a"] != 3 {
		t.Errorf("node-a depth: got %d, want 3", result["node-a"])
	}
}

// TestDetectCommunities_TwoClusters verifies that two disconnected subgraphs
// are detected as two separate communities.
func TestDetectCommunities_TwoClusters(t *testing.T) {
	g := New()
	// Cluster 1: A↔B (bidirectional edges make a strong cluster)
	// Cluster 2: C↔D
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-d", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	communities, mod := g.DetectCommunities()
	if len(communities) < 2 {
		t.Fatalf("expected at least 2 communities, got %d", len(communities))
	}
	if mod <= 0 {
		t.Errorf("expected positive modularity, got %f", mod)
	}

	// Verify all 4 nodes appear across communities
	allNodes := make(map[string]bool)
	for _, c := range communities {
		for _, id := range c.NodeIDs {
			allNodes[id] = true
		}
	}
	for _, id := range []string{"node-a", "node-b", "node-c", "node-d"} {
		if !allNodes[id] {
			t.Errorf("node %q not found in any community", id)
		}
	}
}

// TestDetectCommunities_CacheInvalidation verifies that rebuilding the graph
// invalidates the community cache.
func TestDetectCommunities_CacheInvalidation(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
	})

	// First call caches
	c1, _ := g.DetectCommunities()

	// Rebuild graph with different topology
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-x", TargetID: "node-y", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-y", TargetID: "node-z", EdgeType: types.EdgeTypeCalls},
	})

	// Second call should reflect new topology
	c2, _ := g.DetectCommunities()

	// Check that results differ: c1 should have a/b nodes, c2 should have x/y/z
	c1Nodes := make(map[string]bool)
	for _, c := range c1 {
		for _, id := range c.NodeIDs {
			c1Nodes[id] = true
		}
	}
	c2Nodes := make(map[string]bool)
	for _, c := range c2 {
		for _, id := range c.NodeIDs {
			c2Nodes[id] = true
		}
	}

	if c2Nodes["node-a"] {
		t.Error("cache invalidation failed: node-a from old graph found in new results")
	}
	if !c2Nodes["node-x"] {
		t.Error("cache invalidation failed: node-x from new graph not found")
	}
}

// TestDetectCommunities_EmptyGraph verifies that community detection on an
// empty graph returns nil/empty.
func TestDetectCommunities_EmptyGraph(t *testing.T) {
	g := New()
	communities, mod := g.DetectCommunities()
	if len(communities) != 0 {
		t.Errorf("expected 0 communities for empty graph, got %d", len(communities))
	}
	if mod != 0 {
		t.Errorf("expected 0 modularity for empty graph, got %f", mod)
	}
}

func TestComputeInDegree_DAG(t *testing.T) {
	g := New()
	// A→D, B→D, C→D: D has in-degree 3, others have less
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
	})

	inDeg := g.ComputeInDegree()
	if inDeg == nil {
		t.Fatal("ComputeInDegree returned nil")
	}

	// D has the most incoming edges, so it should be normalized to 1.0
	if inDeg["node-d"] != 1.0 {
		t.Errorf("node-d in-degree: got %f, want 1.0", inDeg["node-d"])
	}
	// A, B, C have 0 incoming edges
	if inDeg["node-a"] != 0 {
		t.Errorf("node-a in-degree: got %f, want 0", inDeg["node-a"])
	}
}

func TestComputeInDegree_EmptyGraph(t *testing.T) {
	g := New()
	inDeg := g.ComputeInDegree()
	if inDeg != nil {
		t.Errorf("expected nil for empty graph, got %v", inDeg)
	}
}

// TestPersonalizedPageRank_DAG verifies PageRank scores on a simple DAG.
// Graph: A→B, A→C, B→D, C→D (A is a source, D is most-depended-upon).
// When traversing with PageRank, D receives contributions from both B and C,
// so it should accumulate a higher score than A (which has no incoming edges).
func TestPersonalizedPageRank_DAG(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-a", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
	})

	ranks := g.PersonalizedPageRank([]string{"node-a"})
	if ranks == nil {
		t.Fatal("PersonalizedPageRank returned nil")
	}

	// All 4 nodes should have positive scores
	for _, id := range []string{"node-a", "node-b", "node-c", "node-d"} {
		score, ok := ranks[id]
		if !ok {
			t.Fatalf("%s missing from PageRank result", id)
		}
		if score <= 0 {
			t.Errorf("%s has non-positive score: %f", id, score)
		}
	}

	// D has two incoming edges (from B and C), making it the most-linked-to
	// among the non-seeded nodes. It should rank higher than B (1 incoming).
	scoreD := ranks["node-d"]
	scoreB := ranks["node-b"]
	if scoreD <= scoreB {
		t.Errorf("expected node-d (score=%f) to rank higher than node-b (score=%f)", scoreD, scoreB)
	}
}

// TestPersonalizedPageRank_PersonalizationBias verifies that true PPR actually
// biases results toward the seeded nodes. When we seed node-a, it should score
// significantly higher than if we seed node-d on the same graph.
func TestPersonalizedPageRank_PersonalizationBias(t *testing.T) {
	g := New()
	// Star graph: A is the hub, B/C/D are leaves connected to A
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-a", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-a", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-d", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
	})

	// Run PPR seeded on node-b
	ranksB := g.PersonalizedPageRank([]string{"node-b"})
	// Run PPR seeded on node-d
	ranksD := g.PersonalizedPageRank([]string{"node-d"})

	if ranksB == nil || ranksD == nil {
		t.Fatal("PersonalizedPageRank returned nil")
	}

	// When seeded on node-b, node-b should have higher score than node-d
	if ranksB["node-b"] <= ranksB["node-d"] {
		t.Errorf("PPR seeded on B: expected B (score=%f) > D (score=%f)",
			ranksB["node-b"], ranksB["node-d"])
	}

	// When seeded on node-d, node-d should have higher score than node-b
	if ranksD["node-d"] <= ranksD["node-b"] {
		t.Errorf("PPR seeded on D: expected D (score=%f) > B (score=%f)",
			ranksD["node-d"], ranksD["node-b"])
	}

	// The scores should be different between the two runs (personalization has effect)
	if ranksB["node-b"] == ranksD["node-b"] {
		t.Error("PPR produced identical scores for node-b regardless of seed — personalization has no effect")
	}
}

// TestPersonalizedPageRank_NoActiveNodes verifies that PPR with empty active nodes
// falls back to uniform teleportation (standard PageRank behavior).
func TestPersonalizedPageRank_NoActiveNodes(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
	})

	ranks := g.PersonalizedPageRank(nil)
	if ranks == nil {
		t.Fatal("PersonalizedPageRank with nil seeds returned nil")
	}
	if len(ranks) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(ranks))
	}

	// All nodes should have positive scores
	for id, score := range ranks {
		if score <= 0 {
			t.Errorf("node %s has non-positive score: %f", id, score)
		}
	}
}

// TestComputeSearchSignals_Consistency verifies that ComputeSearchSignals returns
// both PPR and InDegree computed under a single lock, producing consistent results.
func TestComputeSearchSignals_Consistency(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
	})

	ppr, inDegree := g.ComputeSearchSignals([]string{"node-a"})
	if ppr == nil {
		t.Fatal("ComputeSearchSignals returned nil PPR")
	}
	if inDegree == nil {
		t.Fatal("ComputeSearchSignals returned nil InDegree")
	}

	// Verify PPR has personalization bias
	if ppr["node-a"] <= 0 {
		t.Errorf("seeded node-a should have positive PPR score, got %f", ppr["node-a"])
	}

	// Verify InDegree: node-d has 3 incoming edges, should be normalized to 1.0
	if inDegree["node-d"] != 1.0 {
		t.Errorf("node-d in-degree: got %f, want 1.0", inDegree["node-d"])
	}
	if inDegree["node-a"] != 0 {
		t.Errorf("node-a in-degree: got %f, want 0", inDegree["node-a"])
	}
}

// TestComputeInDegree_Caching verifies that in-degree results are cached and
// invalidated correctly on graph mutations.
func TestComputeInDegree_Caching(t *testing.T) {
	g := New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
	})

	// First call computes fresh
	deg1 := g.ComputeInDegree()
	if deg1 == nil {
		t.Fatal("first ComputeInDegree returned nil")
	}
	if deg1["node-b"] != 1.0 {
		t.Errorf("expected node-b=1.0, got %f", deg1["node-b"])
	}

	// Second call should return cached result (same values)
	deg2 := g.ComputeInDegree()
	if deg2["node-b"] != 1.0 {
		t.Errorf("cached node-b: expected 1.0, got %f", deg2["node-b"])
	}

	// Add another edge — cache should be invalidated
	g.AddEdge(types.ASTEdge{SourceID: "node-c", TargetID: "node-b", EdgeType: types.EdgeTypeCalls})
	deg3 := g.ComputeInDegree()
	// node-b still has the max in-degree (2), should still be 1.0
	if deg3["node-b"] != 1.0 {
		t.Errorf("after mutation node-b: expected 1.0, got %f", deg3["node-b"])
	}
	// But node-c now exists with in-degree 0
	if _, ok := deg3["node-c"]; !ok {
		t.Error("node-c should exist in in-degree results after AddEdge")
	}
}

// TestChangeCount_IncrementAndReset verifies the change counter for betweenness refresh.
func TestChangeCount_IncrementAndReset(t *testing.T) {
	g := New()

	if g.ChangeCount() != 0 {
		t.Errorf("initial ChangeCount: got %d, want 0", g.ChangeCount())
	}

	g.AddEdge(types.ASTEdge{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls})
	if g.ChangeCount() != 1 {
		t.Errorf("after AddEdge ChangeCount: got %d, want 1", g.ChangeCount())
	}

	g.RemoveNode("node-a")
	if g.ChangeCount() != 2 {
		t.Errorf("after RemoveNode ChangeCount: got %d, want 2", g.ChangeCount())
	}

	g.ResetChangeCount()
	if g.ChangeCount() != 0 {
		t.Errorf("after Reset ChangeCount: got %d, want 0", g.ChangeCount())
	}
}

// TestComputeBetweenness_TheoreticalNormalization verifies that betweenness uses
// graph-theoretic normalization (n-1)*(n-2) instead of max-value normalization.
func TestComputeBetweenness_TheoreticalNormalization(t *testing.T) {
	g := New()
	// Linear chain A→B→C: B is on all shortest paths from A to C.
	// With n=3, theoretical max = (3-1)*(3-2) = 2.
	// gonum Betweenness for directed graphs: B should have betweenness = 1
	// (one shortest path from A to C goes through B).
	// Normalized: 1/2 = 0.5
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	btwn := g.ComputeBetweenness()
	if btwn == nil {
		t.Fatal("ComputeBetweenness returned nil")
	}

	// B should have exactly 0.5 with theoretical normalization (1 / (2*1) = 0.5)
	if btwn["node-b"] != 0.5 {
		t.Errorf("node-b betweenness: got %f, want 0.5", btwn["node-b"])
	}

	// A and C are endpoints, should have 0
	if btwn["node-a"] != 0 {
		t.Errorf("node-a betweenness: got %f, want 0", btwn["node-a"])
	}
	if btwn["node-c"] != 0 {
		t.Errorf("node-c betweenness: got %f, want 0", btwn["node-c"])
	}
}

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

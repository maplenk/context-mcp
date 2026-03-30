package graph

import (
	"sync"

	"github.com/naman/qb-context/internal/types"
	gonumgraph "gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/network"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/traverse"
)

// GraphEngine maintains an in-memory directed graph of code relationships
type GraphEngine struct {
	mu         sync.RWMutex
	dg         *simple.DirectedGraph
	idMap      map[string]int64 // node hash ID → gonum int64 ID
	reverseMap map[int64]string // gonum int64 ID → node hash ID
}

// New creates a new GraphEngine
func New() *GraphEngine {
	return &GraphEngine{
		dg:         simple.NewDirectedGraph(),
		idMap:      make(map[string]int64),
		reverseMap: make(map[int64]string),
	}
}

// ensureNode adds a node to the graph if it doesn't exist and returns its int64 ID.
// Must be called with the write lock held.
func (g *GraphEngine) ensureNode(hashID string) int64 {
	if id, exists := g.idMap[hashID]; exists {
		return id
	}
	node := g.dg.NewNode()
	g.dg.AddNode(node)
	id := node.ID()
	g.idMap[hashID] = id
	g.reverseMap[id] = hashID
	return id
}

// BuildFromEdges reconstructs the entire graph from a slice of edges
func (g *GraphEngine) BuildFromEdges(edges []types.ASTEdge) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Reset graph
	g.dg = simple.NewDirectedGraph()
	g.idMap = make(map[string]int64)
	g.reverseMap = make(map[int64]string)

	for _, edge := range edges {
		srcID := g.ensureNode(edge.SourceID)
		tgtID := g.ensureNode(edge.TargetID)
		if srcID != tgtID && !g.dg.HasEdgeFromTo(srcID, tgtID) {
			g.dg.SetEdge(g.dg.NewEdge(g.dg.Node(srcID), g.dg.Node(tgtID)))
		}
	}
}

// AddEdge adds a single edge to the graph
func (g *GraphEngine) AddEdge(edge types.ASTEdge) {
	g.mu.Lock()
	defer g.mu.Unlock()

	srcID := g.ensureNode(edge.SourceID)
	tgtID := g.ensureNode(edge.TargetID)
	if srcID != tgtID && !g.dg.HasEdgeFromTo(srcID, tgtID) {
		g.dg.SetEdge(g.dg.NewEdge(g.dg.Node(srcID), g.dg.Node(tgtID)))
	}
}

// RemoveEdge removes a single edge from the graph
func (g *GraphEngine) RemoveEdge(sourceHash, targetHash string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	srcID, srcOk := g.idMap[sourceHash]
	tgtID, tgtOk := g.idMap[targetHash]
	if srcOk && tgtOk {
		g.dg.RemoveEdge(srcID, tgtID)
	}
}

// RemoveNode removes a node and all its edges from the graph
func (g *GraphEngine) RemoveNode(hashID string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	id, ok := g.idMap[hashID]
	if !ok {
		return
	}
	g.dg.RemoveNode(id)
	delete(g.idMap, hashID)
	delete(g.reverseMap, id)
}

// BlastRadius performs BFS to find all downstream dependents of a node up to maxDepth.
// Uses gonum's traverse.BreadthFirst for traversal.
func (g *GraphEngine) BlastRadius(nodeHashID string, maxDepth int) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	startID, ok := g.idMap[nodeHashID]
	if !ok {
		return nil
	}

	startNode := g.dg.Node(startID)
	if startNode == nil {
		return nil
	}

	var affected []string
	depths := make(map[int64]int)
	depths[startID] = 0

	// Use gonum's BreadthFirst traversal.
	// Traverse is called for each edge considered; returning false prunes that branch.
	// The visit function is called for each node reached.
	bf := traverse.BreadthFirst{
		Traverse: func(e gonumgraph.Edge) bool {
			fromDepth, ok := depths[e.From().ID()]
			if !ok {
				return false
			}
			toDepth := fromDepth + 1
			if toDepth > maxDepth {
				return false
			}
			// Record depth for the destination node so the visit callback can use it.
			// BreadthFirst calls Traverse before visiting the destination, so this is safe.
			depths[e.To().ID()] = toDepth
			return true
		},
	}

	bf.Walk(g.dg, startNode, func(n gonumgraph.Node, d int) bool {
		if n.ID() != startID {
			if hashID, ok := g.reverseMap[n.ID()]; ok {
				affected = append(affected, hashID)
			}
		}
		return false // returning false continues the walk
	})

	return affected
}

// PersonalizedPageRank computes PageRank with teleportation biased toward active files.
// activeNodeIDs are the hash IDs of nodes in the currently edited files.
func (g *GraphEngine) PersonalizedPageRank(activeNodeIDs []string) map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.dg.Nodes().Len() == 0 {
		return nil
	}

	// Build personalization vector: higher weight for active nodes
	personalization := make(map[int64]float64)
	totalNodes := g.dg.Nodes().Len()

	if len(activeNodeIDs) > 0 {
		activeWeight := 0.85 / float64(len(activeNodeIDs))
		passiveWeight := 0.15 / float64(totalNodes)

		nodes := g.dg.Nodes()
		for nodes.Next() {
			personalization[nodes.Node().ID()] = passiveWeight
		}
		for _, hashID := range activeNodeIDs {
			if id, ok := g.idMap[hashID]; ok {
				personalization[id] = activeWeight
			}
		}
	}

	// Run standard PageRank first
	ranks := network.PageRankSparse(g.dg, 0.85, 1e-6)

	// Apply personalization bias: blend standard ranks with the personalization vector.
	// For each node, adjust rank proportionally to its personalization weight.
	if len(personalization) > 0 {
		for id := range ranks {
			if w, ok := personalization[id]; ok {
				ranks[id] = ranks[id]*0.5 + w*0.5
			}
		}
	}

	// Convert back to hash IDs
	result := make(map[string]float64)
	for id, rank := range ranks {
		if hashID, ok := g.reverseMap[id]; ok {
			result[hashID] = rank
		}
	}

	return result
}

// NodeCount returns the number of nodes in the graph
func (g *GraphEngine) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.dg.Nodes().Len()
}

// EdgeCount returns the number of edges in the graph
func (g *GraphEngine) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.dg.Edges().Len()
}

// HasNode checks if a node exists in the graph
func (g *GraphEngine) HasNode(hashID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.idMap[hashID]
	return ok
}

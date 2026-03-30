package graph

import (
	"sync"

	"github.com/naman/qb-context/internal/types"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/network"
	"gonum.org/v1/gonum/graph/simple"
)

// GraphEngine maintains an in-memory directed graph of code relationships
type GraphEngine struct {
	mu             sync.RWMutex
	dg             *simple.DirectedGraph
	idMap          map[string]int64 // node hash ID → gonum int64 ID
	reverseMap     map[int64]string // gonum int64 ID → node hash ID
	communities    [][]string       // cached community node hash IDs
	modularity     float64          // cached Q score
	communityValid bool             // false after BuildFromEdges
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
	g.communityValid = false

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

// BlastRadius performs BFS over incoming edges to find all nodes that depend on
// (directly or transitively call) the given node, up to maxDepth hops away.
// Edges represent "A calls B" (source=caller, target=callee), so to find who
// calls A we must traverse in reverse — following edges that point TO each node.
// g.dg.To(nodeID) returns the predecessors of nodeID (nodes with edges into it).
func (g *GraphEngine) BlastRadius(nodeHashID string, maxDepth int) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	startID, ok := g.idMap[nodeHashID]
	if !ok {
		return nil
	}

	if g.dg.Node(startID) == nil {
		return nil
	}

	// BFS traversing incoming edges (who depends on this node)
	var affected []string
	visited := map[int64]bool{startID: true}
	currentLevel := []int64{startID}

	for depth := 0; depth < maxDepth && len(currentLevel) > 0; depth++ {
		var nextLevel []int64
		for _, nodeID := range currentLevel {
			// g.dg.To(nodeID) returns predecessors — nodes with edges TO this node
			preds := g.dg.To(nodeID)
			for preds.Next() {
				predID := preds.Node().ID()
				if !visited[predID] {
					visited[predID] = true
					nextLevel = append(nextLevel, predID)
					if hashID, ok := g.reverseMap[predID]; ok {
						affected = append(affected, hashID)
					}
				}
			}
		}
		currentLevel = nextLevel
	}

	return affected
}

// PersonalizedPageRank approximates personalized PageRank by computing standard
// PageRank and then boosting scores for nodes connected to active files.
// activeNodeIDs are the hash IDs of nodes in the currently edited files.
// Note: This is an approximation — true PPR would modify the power iteration's
// teleportation vector, which gonum's PageRankSparse doesn't support.
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

// ComputeBetweenness computes betweenness centrality for all nodes using
// Brandes' algorithm (via gonum), normalized to [0,1].
// Returns a map of hash ID → betweenness score.
func (g *GraphEngine) ComputeBetweenness() map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.dg.Nodes().Len() == 0 {
		return nil
	}

	// Brandes' algorithm via gonum
	raw := network.Betweenness(g.dg)

	// Find max for normalization
	var maxVal float64
	for _, v := range raw {
		if v > maxVal {
			maxVal = v
		}
	}

	result := make(map[string]float64)
	if maxVal == 0 {
		// All betweenness is zero — return zeros
		for id := range raw {
			if hashID, ok := g.reverseMap[id]; ok {
				result[hashID] = 0
			}
		}
		return result
	}

	// Normalize to [0,1]
	for id, v := range raw {
		if hashID, ok := g.reverseMap[id]; ok {
			result[hashID] = v / maxVal
		}
	}

	return result
}

// BlastRadiusWithDepth performs BFS over incoming edges (same as BlastRadius)
// but returns a map of hash ID → hop depth instead of a flat list.
func (g *GraphEngine) BlastRadiusWithDepth(nodeHashID string, maxDepth int) map[string]int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	startID, ok := g.idMap[nodeHashID]
	if !ok {
		return nil
	}

	if g.dg.Node(startID) == nil {
		return nil
	}

	result := make(map[string]int)
	visited := map[int64]bool{startID: true}
	currentLevel := []int64{startID}

	for depth := 1; depth <= maxDepth && len(currentLevel) > 0; depth++ {
		var nextLevel []int64
		for _, nodeID := range currentLevel {
			preds := g.dg.To(nodeID)
			for preds.Next() {
				predID := preds.Node().ID()
				if !visited[predID] {
					visited[predID] = true
					nextLevel = append(nextLevel, predID)
					if hashID, ok := g.reverseMap[predID]; ok {
						result[hashID] = depth
					}
				}
			}
		}
		currentLevel = nextLevel
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

// DetectCommunities uses Louvain community detection to find tightly coupled
// clusters of code symbols. Results are cached and invalidated on graph rebuild.
func (g *GraphEngine) DetectCommunities() ([]types.Community, float64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.communityValid {
		// Return cached results
		var result []types.Community
		for i, nodeIDs := range g.communities {
			result = append(result, types.Community{
				ID:      i,
				NodeIDs: nodeIDs,
			})
		}
		return result, g.modularity
	}

	if g.dg.Nodes().Len() == 0 {
		g.communityValid = true
		g.communities = nil
		g.modularity = 0
		return nil, 0
	}

	// Use gonum's Louvain community detection (Modularize)
	// The undirected graph is needed for community detection — build one from edges
	undirected := simple.NewUndirectedGraph()
	edges := g.dg.Edges()
	nodeSet := make(map[int64]bool)
	for edges.Next() {
		e := edges.Edge()
		from := e.From().ID()
		to := e.To().ID()
		nodeSet[from] = true
		nodeSet[to] = true
		if from != to && !undirected.HasEdgeBetween(from, to) {
			undirected.SetEdge(undirected.NewEdge(simple.Node(from), simple.Node(to)))
		}
	}
	// Ensure all nodes are in the undirected graph
	for id := range nodeSet {
		if undirected.Node(id) == nil {
			undirected.AddNode(simple.Node(id))
		}
	}

	// Run Louvain
	reduced := community.Modularize(undirected, 1.0, nil)
	communities := reduced.Communities()
	mod := community.Q(undirected, communities, 1.0)

	// Convert gonum int64 IDs back to hash IDs
	g.communities = make([][]string, len(communities))
	for i, comm := range communities {
		var nodeIDs []string
		for _, node := range comm {
			if hashID, ok := g.reverseMap[node.ID()]; ok {
				nodeIDs = append(nodeIDs, hashID)
			}
		}
		g.communities[i] = nodeIDs
	}
	g.modularity = mod
	g.communityValid = true

	var result []types.Community
	for i, nodeIDs := range g.communities {
		result = append(result, types.Community{
			ID:      i,
			NodeIDs: nodeIDs,
		})
	}

	return result, mod
}

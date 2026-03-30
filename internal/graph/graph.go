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
	inDegreeCache  map[string]float64 // cached in-degree scores
	inDegreeValid  bool               // false after graph mutations
	changeCount    int                // incremental change counter for betweenness refresh
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
	g.inDegreeValid = false

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
		g.communityValid = false
		g.inDegreeValid = false
		g.changeCount++
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
		g.communityValid = false
		g.inDegreeValid = false
		g.changeCount++
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
	g.communityValid = false
	g.inDegreeValid = false
	g.changeCount++
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

// PersonalizedPageRank implements true Personalized PageRank via custom power iteration.
// The teleportation vector is seeded with the active nodes instead of uniform distribution,
// so random walks that "restart" teleport back to the active context rather than random nodes.
// activeNodeIDs are the hash IDs of nodes in the currently edited files / top FTS results.
func (g *GraphEngine) PersonalizedPageRank(activeNodeIDs []string) map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.personalizedPageRankLocked(activeNodeIDs)
}

// personalizedPageRankLocked is the lock-free inner implementation of PPR.
// Caller must hold at least g.mu.RLock().
func (g *GraphEngine) personalizedPageRankLocked(activeNodeIDs []string) map[string]float64 {
	n := g.dg.Nodes().Len()
	if n == 0 {
		return nil
	}

	const (
		alpha   = 0.85  // damping factor
		epsilon = 1e-6  // convergence threshold
		maxIter = 100   // max iterations
	)

	// Build teleportation vector (personalization)
	teleport := make(map[int64]float64)
	if len(activeNodeIDs) > 0 {
		weight := 1.0 / float64(len(activeNodeIDs))
		for _, hashID := range activeNodeIDs {
			if id, ok := g.idMap[hashID]; ok {
				teleport[id] += weight
			}
		}
	}
	// If no active nodes mapped, fall back to uniform teleportation
	if len(teleport) == 0 {
		nodes := g.dg.Nodes()
		weight := 1.0 / float64(n)
		for nodes.Next() {
			teleport[nodes.Node().ID()] = weight
		}
	}

	// Initialize rank vector uniformly
	rank := make(map[int64]float64)
	nodes := g.dg.Nodes()
	initVal := 1.0 / float64(n)
	for nodes.Next() {
		rank[nodes.Node().ID()] = initVal
	}

	// Power iteration
	for iter := 0; iter < maxIter; iter++ {
		newRank := make(map[int64]float64)

		// Teleportation component: (1 - alpha) * teleport[id]
		for id, t := range teleport {
			newRank[id] = (1 - alpha) * t
		}

		// Random walk component
		allNodes := g.dg.Nodes()
		for allNodes.Next() {
			nodeID := allNodes.Node().ID()
			succs := g.dg.From(nodeID)
			outDeg := succs.Len()
			if outDeg == 0 {
				// Dangling node: distribute its rank to the teleportation vector
				for id, t := range teleport {
					newRank[id] += alpha * rank[nodeID] * t
				}
			} else {
				// Distribute rank equally to successors
				succs.Reset()
				share := alpha * rank[nodeID] / float64(outDeg)
				for succs.Next() {
					succID := succs.Node().ID()
					newRank[succID] += share
				}
			}
		}

		// Check convergence (L1 norm of difference)
		diff := 0.0
		for id, r := range newRank {
			d := r - rank[id]
			if d < 0 {
				d = -d
			}
			diff += d
		}
		rank = newRank
		if diff < epsilon {
			break
		}
	}

	// Convert to hash IDs
	result := make(map[string]float64)
	for id, r := range rank {
		if hashID, ok := g.reverseMap[id]; ok {
			result[hashID] = r
		}
	}
	return result
}

// ComputeBetweenness computes betweenness centrality for all nodes using
// Brandes' algorithm (via gonum), normalized to [0,1] using the graph-theoretic
// maximum for directed graphs: (n-1)*(n-2), where n = number of nodes.
// This makes scores comparable across different graph states.
func (g *GraphEngine) ComputeBetweenness() map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	n := g.dg.Nodes().Len()
	if n == 0 {
		return nil
	}

	// Brandes' algorithm via gonum
	raw := network.Betweenness(g.dg)

	result := make(map[string]float64)

	// For directed graphs, the theoretical maximum betweenness is (n-1)*(n-2).
	// This is the number of ordered pairs (s,t) where s != t != v that could
	// route through a single node v. Using this instead of max-value normalization
	// makes scores comparable across different graph sizes/states.
	theoreticalMax := float64(n-1) * float64(n-2)
	if theoreticalMax <= 0 {
		// Graph with 0 or 1 nodes: all betweenness is zero
		for id := range raw {
			if hashID, ok := g.reverseMap[id]; ok {
				result[hashID] = 0
			}
		}
		return result
	}

	// Normalize to [0,1] using graph-theoretic maximum
	for id, v := range raw {
		if hashID, ok := g.reverseMap[id]; ok {
			normalized := v / theoreticalMax
			// Clamp to [0,1] for safety
			if normalized > 1.0 {
				normalized = 1.0
			}
			result[hashID] = normalized
		}
	}

	return result
}

// ComputeInDegree computes the in-degree authority for all nodes, normalized to [0,1].
// In-degree counts how many other nodes have edges pointing TO each node.
// Results are cached and invalidated on graph mutations.
func (g *GraphEngine) ComputeInDegree() map[string]float64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.inDegreeValid && g.inDegreeCache != nil {
		// Return a copy of the cache to avoid races
		result := make(map[string]float64, len(g.inDegreeCache))
		for k, v := range g.inDegreeCache {
			result[k] = v
		}
		return result
	}

	return g.computeInDegreeLocked()
}

// computeInDegreeLocked computes in-degree and updates the cache.
// Caller must hold g.mu (write lock).
func (g *GraphEngine) computeInDegreeLocked() map[string]float64 {
	if g.dg.Nodes().Len() == 0 {
		g.inDegreeCache = nil
		g.inDegreeValid = true
		return nil
	}

	raw := make(map[int64]int)
	var maxVal int

	nodes := g.dg.Nodes()
	for nodes.Next() {
		id := nodes.Node().ID()
		inDeg := g.dg.To(id).Len()
		raw[id] = inDeg
		if inDeg > maxVal {
			maxVal = inDeg
		}
	}

	result := make(map[string]float64)
	if maxVal == 0 {
		for id := range raw {
			if hashID, ok := g.reverseMap[id]; ok {
				result[hashID] = 0
			}
		}
	} else {
		for id, deg := range raw {
			if hashID, ok := g.reverseMap[id]; ok {
				result[hashID] = float64(deg) / float64(maxVal)
			}
		}
	}

	g.inDegreeCache = result
	g.inDegreeValid = true
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

// ChangeCount returns the number of incremental graph mutations since last reset.
// Used to decide when to trigger async betweenness recomputation.
func (g *GraphEngine) ChangeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.changeCount
}

// ResetChangeCount resets the incremental change counter to zero.
func (g *GraphEngine) ResetChangeCount() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.changeCount = 0
}

// ComputeSearchSignals computes PPR and InDegree under a SINGLE lock acquisition
// to ensure consistency during search. This prevents race conditions where a
// concurrent graph rebuild could give inconsistent results between the two signals.
func (g *GraphEngine) ComputeSearchSignals(activeNodeIDs []string) (ppr map[string]float64, inDegree map[string]float64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ppr = g.personalizedPageRankLocked(activeNodeIDs)

	// Reuse cached in-degree if valid, otherwise compute and cache
	if g.inDegreeValid && g.inDegreeCache != nil {
		inDegree = make(map[string]float64, len(g.inDegreeCache))
		for k, v := range g.inDegreeCache {
			inDegree[k] = v
		}
	} else {
		inDegree = g.computeInDegreeLocked()
	}

	return ppr, inDegree
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
	for edges.Next() {
		e := edges.Edge()
		from := e.From().ID()
		to := e.To().ID()
		if from != to && !undirected.HasEdgeBetween(from, to) {
			undirected.SetEdge(undirected.NewEdge(simple.Node(from), simple.Node(to)))
		}
	}
	// Ensure ALL nodes from the directed graph are in the undirected graph
	// (isolated nodes with no edges would otherwise be silently dropped)
	allNodes := g.dg.Nodes()
	for allNodes.Next() {
		id := allNodes.Node().ID()
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

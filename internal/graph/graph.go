package graph

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/naman/qb-context/internal/types"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/network"
	"gonum.org/v1/gonum/graph/simple"
)

// noParent is a sentinel value for the BFS parent map indicating a node has
// no parent (i.e., it is the BFS root). Using math.MinInt64 avoids any
// possible collision with valid gonum node IDs, which are non-negative.
const noParent = math.MinInt64

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
		if !g.dg.HasEdgeFromTo(srcID, tgtID) {
			g.dg.SetEdge(g.dg.NewEdge(g.dg.Node(srcID), g.dg.Node(tgtID)))
		}
	}

	// Reset changeCount after a full rebuild so that incremental mutation
	// tracking starts fresh. Without this, the counter retains its old
	// value and could trigger a spurious betweenness recomputation.
	g.changeCount = 0
}

// AddEdge adds a single edge to the graph
func (g *GraphEngine) AddEdge(edge types.ASTEdge) {
	g.mu.Lock()
	defer g.mu.Unlock()

	srcID := g.ensureNode(edge.SourceID)
	tgtID := g.ensureNode(edge.TargetID)
	if !g.dg.HasEdgeFromTo(srcID, tgtID) {
		g.dg.SetEdge(g.dg.NewEdge(g.dg.Node(srcID), g.dg.Node(tgtID)))
		g.communityValid = false
		g.inDegreeValid = false
		g.changeCount++
	}
}

// RemoveEdge removes a single edge from the graph. If removing the edge leaves
// either the source or target node with zero remaining edges (both in-degree
// and out-degree are zero), the orphaned node is also removed from the graph
// and the ID maps to prevent unbounded memory growth in long-running sessions.
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

		// Clean up orphaned nodes (zero in-degree AND zero out-degree)
		g.removeIfOrphaned(srcID, sourceHash)
		if srcID != tgtID {
			g.removeIfOrphaned(tgtID, targetHash)
		}
	}
}

// removeIfOrphaned removes a node from the graph and ID maps if it has no
// remaining edges (both in-degree and out-degree are zero).
// Must be called with the write lock held.
func (g *GraphEngine) removeIfOrphaned(id int64, hashID string) {
	if g.dg.From(id).Len() == 0 && g.dg.To(id).Len() == 0 {
		g.dg.RemoveNode(id)
		delete(g.idMap, hashID)
		delete(g.reverseMap, id)
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
		// Also count mass lost from nodes in rank but not in newRank
		for id, r := range rank {
			if _, ok := newRank[id]; !ok {
				diff += r
			}
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

// PersonalizedPageRankSubgraph runs PPR on a subgraph induced by candidateIDs.
// Only candidate nodes participate in the random walk, dramatically reducing
// computation for large graphs (12K nodes → ~100 candidate nodes).
func (g *GraphEngine) PersonalizedPageRankSubgraph(seedIDs, candidateIDs []string) map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.personalizedPageRankSubgraphLocked(seedIDs, candidateIDs)
}

// personalizedPageRankSubgraphLocked runs PPR on the subgraph induced by candidateIDs.
// Caller must hold at least g.mu.RLock().
func (g *GraphEngine) personalizedPageRankSubgraphLocked(seedIDs, candidateIDs []string) map[string]float64 {
	if len(candidateIDs) == 0 {
		return nil
	}

	// Build candidate set for O(1) lookup
	candidateSet := make(map[int64]bool, len(candidateIDs))
	for _, hashID := range candidateIDs {
		if id, ok := g.idMap[hashID]; ok {
			candidateSet[id] = true
		}
	}

	n := len(candidateSet)
	if n == 0 {
		return nil
	}

	// Intentionally relaxed parameters compared to full-graph PPR (epsilon=1e-6,
	// maxIter=100). The subgraph typically contains ~100 candidate nodes vs 12K+
	// in the full graph, so convergence is much faster. Looser epsilon (1e-4) and
	// fewer iterations (15) deliver comparable ranking quality on small subgraphs
	// while keeping latency low for interactive search queries.
	const (
		alpha   = 0.85
		epsilon = 1e-4
		maxIter = 15
	)

	// Build teleportation vector from seeds (only those in candidate set)
	teleport := make(map[int64]float64)
	for _, hashID := range seedIDs {
		if id, ok := g.idMap[hashID]; ok {
			if candidateSet[id] {
				teleport[id] += 1.0
			}
		}
	}
	// Normalize teleport vector
	if len(teleport) == 0 {
		// Seeds are outside candidate set — use their neighbors in candidates as proxies
		for _, hashID := range seedIDs {
			if id, ok := g.idMap[hashID]; ok {
				succs := g.dg.From(id)
				for succs.Next() {
					succID := succs.Node().ID()
					if candidateSet[succID] {
						teleport[succID] += 1.0
					}
				}
				preds := g.dg.To(id)
				for preds.Next() {
					predID := preds.Node().ID()
					if candidateSet[predID] {
						teleport[predID] += 1.0
					}
				}
			}
		}
		// If still empty, fall back to uniform
		if len(teleport) == 0 {
			weight := 1.0 / float64(n)
			for id := range candidateSet {
				teleport[id] = weight
			}
		}
	} else {
		sum := 0.0
		for _, v := range teleport {
			sum += v
		}
		for id := range teleport {
			teleport[id] /= sum
		}
	}

	// Initialize rank uniformly over candidates
	rank := make(map[int64]float64, n)
	initVal := 1.0 / float64(n)
	for id := range candidateSet {
		rank[id] = initVal
	}

	// Power iteration on subgraph
	for iter := 0; iter < maxIter; iter++ {
		newRank := make(map[int64]float64, n)

		// Teleportation component
		for id, t := range teleport {
			newRank[id] = (1 - alpha) * t
		}

		// Random walk: only traverse edges within candidate set
		for nodeID := range candidateSet {
			succs := g.dg.From(nodeID)
			// Count out-degree within candidate subgraph
			var subgraphSuccs []int64
			for succs.Next() {
				succID := succs.Node().ID()
				if candidateSet[succID] {
					subgraphSuccs = append(subgraphSuccs, succID)
				}
			}
			if len(subgraphSuccs) == 0 {
				// Dangling node in subgraph: distribute to teleport
				for id, t := range teleport {
					newRank[id] += alpha * rank[nodeID] * t
				}
			} else {
				share := alpha * rank[nodeID] / float64(len(subgraphSuccs))
				for _, succID := range subgraphSuccs {
					newRank[succID] += share
				}
			}
		}

		// Check convergence
		diff := 0.0
		for id, r := range newRank {
			d := r - rank[id]
			if d < 0 {
				d = -d
			}
			diff += d
		}
		// Also count mass lost from nodes in rank but not in newRank
		for id, r := range rank {
			if _, ok := newRank[id]; !ok {
				diff += r
			}
		}
		rank = newRank
		if diff < epsilon {
			break
		}
	}

	// Convert to hash IDs
	result := make(map[string]float64, n)
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
// Uses double-checked locking: first checks cache under a read lock to avoid
// blocking other readers, then acquires a write lock only on cache miss.
func (g *GraphEngine) ComputeInDegree() map[string]float64 {
	// Fast path: check cache with read lock (does not block other readers)
	g.mu.RLock()
	if g.inDegreeValid && g.inDegreeCache != nil {
		result := make(map[string]float64, len(g.inDegreeCache))
		for k, v := range g.inDegreeCache {
			result[k] = v
		}
		g.mu.RUnlock()
		return result
	}
	g.mu.RUnlock()

	// Slow path: acquire write lock and recheck (double-checked locking)
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.inDegreeValid && g.inDegreeCache != nil {
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

	// Return a copy to prevent caller mutations from corrupting the cache
	copyResult := make(map[string]float64, len(result))
	for k, v := range result {
		copyResult[k] = v
	}
	return copyResult
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

// ComputeSearchSignals computes PPR and InDegree for search under a single write lock
// to prevent race conditions where BuildFromEdges could replace the graph between
// the PPR computation and in-degree read.
func (g *GraphEngine) ComputeSearchSignals(activeNodeIDs []string) (ppr map[string]float64, inDegree map[string]float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ppr = g.personalizedPageRankLocked(activeNodeIDs)
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

// ComputeSearchSignalsSubgraph computes PPR on the candidate subgraph and returns
// InDegree from cache. Uses a single write lock to prevent race conditions where
// BuildFromEdges could replace the graph between PPR and in-degree computations.
func (g *GraphEngine) ComputeSearchSignalsSubgraph(seedIDs, candidateIDs []string) (ppr map[string]float64, inDegree map[string]float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ppr = g.personalizedPageRankSubgraphLocked(seedIDs, candidateIDs)
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
	return g.detectCommunitiesLocked()
}

// detectCommunitiesLocked is the inner implementation of DetectCommunities.
// Caller must hold g.mu (write lock).
func (g *GraphEngine) detectCommunitiesLocked() ([]types.Community, float64) {
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

// TraceCallPath performs bidirectional BFS to find call paths between two symbols.
// It searches forward from the source (outgoing edges) and backward from the target
// (incoming edges) until the frontiers meet, then reconstructs all found paths.
// Returns a list of paths (each path is a list of hash IDs) up to maxDepth total hops.
func (g *GraphEngine) TraceCallPath(fromHash, toHash string, maxDepth int) [][]string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	fromID, fromOk := g.idMap[fromHash]
	toID, toOk := g.idMap[toHash]
	if !fromOk || !toOk {
		return nil
	}
	if fromID == toID {
		return [][]string{{fromHash}}
	}

	// BFS forward from source (following outgoing edges)
	// parentFwd maps each visited int64 node to its predecessor on the forward side
	parentFwd := map[int64][]int64{fromID: nil}
	frontierFwd := []int64{fromID}

	// BFS backward from target (following incoming edges)
	parentBwd := map[int64][]int64{toID: nil}
	frontierBwd := []int64{toID}

	// meetingNodes are nodes found in both forward and backward visited sets
	var meetingNodes []int64

	halfDepth := (maxDepth + 1) / 2
	for depth := 0; depth < halfDepth && len(meetingNodes) == 0; depth++ {
		// Expand forward frontier
		if len(frontierFwd) > 0 {
			var nextFwd []int64
			for _, nodeID := range frontierFwd {
				succs := g.dg.From(nodeID) // outgoing edges: node -> successors
				for succs.Next() {
					succID := succs.Node().ID()
					if _, visited := parentFwd[succID]; !visited {
						parentFwd[succID] = []int64{nodeID}
						nextFwd = append(nextFwd, succID)
						if _, inBwd := parentBwd[succID]; inBwd {
							meetingNodes = append(meetingNodes, succID)
						}
					} else {
						parentFwd[succID] = append(parentFwd[succID], nodeID)
					}
				}
			}
			frontierFwd = nextFwd
		}

		if len(meetingNodes) > 0 {
			break
		}

		// Expand backward frontier
		if len(frontierBwd) > 0 {
			var nextBwd []int64
			for _, nodeID := range frontierBwd {
				preds := g.dg.To(nodeID) // incoming edges: predecessors -> node
				for preds.Next() {
					predID := preds.Node().ID()
					if _, visited := parentBwd[predID]; !visited {
						parentBwd[predID] = []int64{nodeID}
						nextBwd = append(nextBwd, predID)
						if _, inFwd := parentFwd[predID]; inFwd {
							meetingNodes = append(meetingNodes, predID)
						}
					} else {
						parentBwd[predID] = append(parentBwd[predID], nodeID)
					}
				}
			}
			frontierBwd = nextBwd
		}
	}

	if len(meetingNodes) == 0 {
		// Also try a simple forward-only BFS as fallback
		return g.traceForwardBFS(fromID, toID, maxDepth)
	}

	// Reconstruct paths through meeting nodes
	var allPaths [][]string
	for _, mid := range meetingNodes {
		// Build forward path from source to meeting node
		fwdPath := g.reconstructPath(parentFwd, fromID, mid)
		// Build backward path from meeting node to target
		bwdPath := g.reconstructPath(parentBwd, toID, mid)
		if fwdPath == nil || bwdPath == nil {
			continue // skip this meeting node — no valid path
		}

		// Reverse the backward path and combine
		for i, j := 0, len(bwdPath)-1; i < j; i, j = i+1, j-1 {
			bwdPath[i], bwdPath[j] = bwdPath[j], bwdPath[i]
		}

		// Combine: fwdPath ends with mid, bwdPath starts with mid, so skip first of bwd
		var fullPath []string
		for _, id := range fwdPath {
			if h, ok := g.reverseMap[id]; ok {
				fullPath = append(fullPath, h)
			}
		}
		if len(bwdPath) > 1 {
			for _, id := range bwdPath[1:] {
				if h, ok := g.reverseMap[id]; ok {
					fullPath = append(fullPath, h)
				}
			}
		}

		if len(fullPath) > 0 {
			allPaths = append(allPaths, fullPath)
		}
	}

	// Deduplicate paths
	seen := make(map[string]bool)
	var uniquePaths [][]string
	for _, p := range allPaths {
		key := fmt.Sprintf("%v", p)
		if !seen[key] {
			seen[key] = true
			uniquePaths = append(uniquePaths, p)
		}
	}

	return uniquePaths
}

// traceForwardBFS performs a simple forward BFS to find a path from source to target.
func (g *GraphEngine) traceForwardBFS(fromID, toID int64, maxDepth int) [][]string {
	parent := map[int64]int64{fromID: noParent}
	frontier := []int64{fromID}

	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []int64
		for _, nodeID := range frontier {
			succs := g.dg.From(nodeID)
			for succs.Next() {
				succID := succs.Node().ID()
				if _, visited := parent[succID]; !visited {
					parent[succID] = nodeID
					next = append(next, succID)
					if succID == toID {
						// Reconstruct path
						var path []string
						cur := toID
						for cur != noParent {
							if h, ok := g.reverseMap[cur]; ok {
								path = append(path, h)
							}
							cur = parent[cur]
						}
						// Reverse
						for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
							path[i], path[j] = path[j], path[i]
						}
						return [][]string{path}
					}
				}
			}
		}
		frontier = next
	}
	return nil
}

// reconstructPath traces back from target to source using the parent map from BFS.
func (g *GraphEngine) reconstructPath(parentMap map[int64][]int64, source, target int64) []int64 {
	if source == target {
		return []int64{source}
	}

	// BFS backwards through the parent map
	type state struct {
		node int64
		path []int64
	}
	queue := []state{{node: target, path: []int64{target}}}
	visited := map[int64]bool{target: true}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		parents := parentMap[cur.node]
		for _, p := range parents {
			newPath := make([]int64, len(cur.path)+1)
			copy(newPath, cur.path)
			newPath[len(cur.path)] = p

			if p == source {
				// Reverse to get source->target order
				for i, j := 0, len(newPath)-1; i < j; i, j = i+1, j-1 {
					newPath[i], newPath[j] = newPath[j], newPath[i]
				}
				return newPath
			}

			if !visited[p] {
				visited[p] = true
				queue = append(queue, state{node: p, path: newPath})
			}
		}
	}

	return nil // No path found
}

// PageRank computes standard (non-personalized) PageRank and returns a map of
// hash ID to score.
func (g *GraphEngine) PageRank() map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.dg.Nodes().Len() == 0 {
		return nil
	}

	ranks := network.PageRankSparse(g.dg, 0.85, 1e-6)
	result := make(map[string]float64)
	for id, rank := range ranks {
		if hashID, ok := g.reverseMap[id]; ok {
			result[hashID] = rank
		}
	}
	return result
}

// GetInDegree returns the in-degree (number of incoming edges) for a node.
func (g *GraphEngine) GetInDegree(hashID string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	id, ok := g.idMap[hashID]
	if !ok {
		return 0
	}
	return g.dg.To(id).Len()
}

// GetOutDegree returns the out-degree (number of outgoing edges) for a node.
func (g *GraphEngine) GetOutDegree(hashID string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	id, ok := g.idMap[hashID]
	if !ok {
		return 0
	}
	return g.dg.From(id).Len()
}

// GetEntryPoints returns nodes with zero in-degree (nobody calls them; they initiate).
func (g *GraphEngine) GetEntryPoints() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var entryPoints []string
	nodes := g.dg.Nodes()
	for nodes.Next() {
		id := nodes.Node().ID()
		if g.dg.To(id).Len() == 0 {
			if hashID, ok := g.reverseMap[id]; ok {
				entryPoints = append(entryPoints, hashID)
			}
		}
	}
	return entryPoints
}

// GetHubs returns nodes with the highest out-degree (they call many things).
// Returns up to limit nodes sorted by out-degree descending.
func (g *GraphEngine) GetHubs(limit int) []struct {
	HashID    string
	OutDegree int
} {
	g.mu.RLock()
	defer g.mu.RUnlock()

	type hubEntry struct {
		HashID    string
		OutDegree int
	}

	var hubs []hubEntry
	nodes := g.dg.Nodes()
	for nodes.Next() {
		id := nodes.Node().ID()
		outDeg := g.dg.From(id).Len()
		if outDeg > 0 {
			if hashID, ok := g.reverseMap[id]; ok {
				hubs = append(hubs, hubEntry{HashID: hashID, OutDegree: outDeg})
			}
		}
	}

	// Sort by out-degree descending
	sort.Slice(hubs, func(i, j int) bool {
		return hubs[i].OutDegree > hubs[j].OutDegree
	})

	if limit > 0 && len(hubs) > limit {
		hubs = hubs[:limit]
	}

	// Convert to return type
	result := make([]struct {
		HashID    string
		OutDegree int
	}, len(hubs))
	for i, h := range hubs {
		result[i].HashID = h.HashID
		result[i].OutDegree = h.OutDegree
	}
	return result
}

// GetConnectors returns nodes that bridge communities — they have high betweenness
// and edges to nodes in multiple communities. Returns hash IDs.
// Uses a single write lock to prevent race conditions where BuildFromEdges could
// replace the graph between community detection check and the read of graph data.
func (g *GraphEngine) GetConnectors(betweenness map[string]float64, limit int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.communityValid {
		g.detectCommunitiesLocked()
	}

	// Build community membership map from cached data
	communityOf := make(map[string]int)
	for i, comm := range g.communities {
		for _, nodeID := range comm {
			communityOf[nodeID] = i
		}
	}

	type connectorEntry struct {
		HashID          string
		Betweenness     float64
		CommunitiesSpan int
	}

	var connectors []connectorEntry
	for hashID, btwn := range betweenness {
		if btwn <= 0 {
			continue
		}

		id, ok := g.idMap[hashID]
		if !ok {
			continue
		}

		// Count how many different communities this node's neighbors belong to
		neighborComms := make(map[int]bool)
		succs := g.dg.From(id)
		for succs.Next() {
			succHash := g.reverseMap[succs.Node().ID()]
			if c, ok := communityOf[succHash]; ok {
				neighborComms[c] = true
			}
		}
		preds := g.dg.To(id)
		for preds.Next() {
			predHash := g.reverseMap[preds.Node().ID()]
			if c, ok := communityOf[predHash]; ok {
				neighborComms[c] = true
			}
		}

		if len(neighborComms) >= 2 {
			connectors = append(connectors, connectorEntry{
				HashID:          hashID,
				Betweenness:     btwn,
				CommunitiesSpan: len(neighborComms),
			})
		}
	}

	sort.Slice(connectors, func(i, j int) bool {
		return connectors[i].Betweenness > connectors[j].Betweenness
	})

	if limit > 0 && len(connectors) > limit {
		connectors = connectors[:limit]
	}

	result := make([]string, len(connectors))
	for i, c := range connectors {
		result[i] = c.HashID
	}
	return result
}

// GetCallees returns the hash IDs of all nodes that the given node calls (outgoing edges).
func (g *GraphEngine) GetCallees(hashID string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	id, ok := g.idMap[hashID]
	if !ok {
		return nil
	}

	var callees []string
	succs := g.dg.From(id)
	for succs.Next() {
		if h, ok := g.reverseMap[succs.Node().ID()]; ok {
			callees = append(callees, h)
		}
	}
	return callees
}

// GetCallers returns the hash IDs of all nodes that call the given node (incoming edges).
func (g *GraphEngine) GetCallers(hashID string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	id, ok := g.idMap[hashID]
	if !ok {
		return nil
	}

	var callers []string
	preds := g.dg.To(id)
	for preds.Next() {
		if h, ok := g.reverseMap[preds.Node().ID()]; ok {
			callers = append(callers, h)
		}
	}
	return callers
}

// CollectDeps collects callees (outgoing) and callers (incoming) up to a given depth
// from a starting node. Returns two sets: dependencies (callees) and dependents (callers).
func (g *GraphEngine) CollectDeps(hashID string, depth int) (deps []string, dependents []string) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	startID, ok := g.idMap[hashID]
	if !ok {
		return nil, nil
	}

	// BFS forward (callees / dependencies)
	visitedFwd := map[int64]bool{startID: true}
	frontierFwd := []int64{startID}
	for d := 0; d < depth && len(frontierFwd) > 0; d++ {
		var next []int64
		for _, nid := range frontierFwd {
			succs := g.dg.From(nid)
			for succs.Next() {
				sid := succs.Node().ID()
				if !visitedFwd[sid] {
					visitedFwd[sid] = true
					next = append(next, sid)
					if h, ok := g.reverseMap[sid]; ok {
						deps = append(deps, h)
					}
				}
			}
		}
		frontierFwd = next
	}

	// BFS backward (callers / dependents)
	visitedBwd := map[int64]bool{startID: true}
	frontierBwd := []int64{startID}
	for d := 0; d < depth && len(frontierBwd) > 0; d++ {
		var next []int64
		for _, nid := range frontierBwd {
			preds := g.dg.To(nid)
			for preds.Next() {
				pid := preds.Node().ID()
				if !visitedBwd[pid] {
					visitedBwd[pid] = true
					next = append(next, pid)
					if h, ok := g.reverseMap[pid]; ok {
						dependents = append(dependents, h)
					}
				}
			}
		}
		frontierBwd = next
	}

	return deps, dependents
}

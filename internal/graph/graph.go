package graph

import (
	"fmt"
	"sort"
	"sync"

	"github.com/maplenk/context-mcp/internal/types"
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

// BuildFromEdges reconstructs the entire graph from a slice of edges.
// If validNodeIDs is non-nil, edges referencing unknown nodes are skipped
// to prevent ghost nodes from orphaned database rows.
func (g *GraphEngine) BuildFromEdges(edges []types.ASTEdge, validNodeIDs ...map[string]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Reset graph
	g.dg = simple.NewDirectedGraph()
	g.idMap = make(map[string]int64)
	g.reverseMap = make(map[int64]string)
	g.communityValid = false
	g.inDegreeValid = false

	var valid map[string]bool
	if len(validNodeIDs) > 0 {
		valid = validNodeIDs[0]
	}

	for _, edge := range edges {
		// Skip edges referencing unknown nodes to prevent ghost nodes
		if valid != nil {
			if !valid[edge.SourceID] || !valid[edge.TargetID] {
				continue
			}
		}
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
	const maxVisited = 10000
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
			if len(visited) >= maxVisited {
				break
			}
		}
		if len(visited) >= maxVisited {
			break
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
		// If still empty, fall back to uniform; otherwise normalize proxy weights
		if len(teleport) == 0 {
			weight := 1.0 / float64(n)
			for id := range candidateSet {
				teleport[id] = weight
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
func (g *GraphEngine) ComputeInDegree() map[string]float64 {
	// Fast path: return cached data under read lock
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

	// Slow path: recompute under write lock
	g.mu.Lock()
	defer g.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have computed)
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

// ComputeSearchSignals computes PPR and InDegree for search. Takes a brief read
// lock to snapshot graph data and in-degree cache, then releases the lock before
// running the iterative PPR computation. This avoids serializing concurrent searches.
func (g *GraphEngine) ComputeSearchSignals(activeNodeIDs []string) (ppr map[string]float64, inDegree map[string]float64) {
	// Phase 1: snapshot full graph data and in-degree under a read lock
	snap, inDeg, needInDegree := g.snapshotFullGraphData(activeNodeIDs)

	// Phase 2: if in-degree cache was not populated, compute under write lock
	if needInDegree {
		g.mu.Lock()
		if g.inDegreeValid && g.inDegreeCache != nil {
			inDeg = make(map[string]float64, len(g.inDegreeCache))
			for k, v := range g.inDegreeCache {
				inDeg[k] = v
			}
		} else {
			inDeg = g.computeInDegreeLocked()
		}
		g.mu.Unlock()
	}
	inDegree = inDeg

	// Phase 3: run PPR on the snapshot with no lock held
	ppr = computeFullPPROnSnapshot(snap)
	return ppr, inDegree
}

// fullGraphSnapshot holds a deep-copied graph needed for full PPR computation.
type fullGraphSnapshot struct {
	// allNodeIDs: every node ID in the graph
	allNodeIDs []int64
	// adjacency: for each node, its list of successor node IDs
	adjacency map[int64][]int64
	// outDegree: for each node, its out-degree (needed to distinguish 0-successors from missing)
	outDegree map[int64]int
	// activeInt64s: the active/seed node IDs mapped to int64
	activeInt64s []int64
	// reverseMap: int64 → hash ID for all nodes
	reverseMap map[int64]string
	// totalNodes: total node count
	totalNodes int
}

// snapshotFullGraphData takes a read lock to deep-copy the full graph structure.
func (g *GraphEngine) snapshotFullGraphData(activeNodeIDs []string) (fullGraphSnapshot, map[string]float64, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	n := g.dg.Nodes().Len()
	snap := fullGraphSnapshot{
		allNodeIDs: make([]int64, 0, n),
		adjacency:  make(map[int64][]int64, n),
		outDegree:  make(map[int64]int, n),
		reverseMap: make(map[int64]string, n),
		totalNodes: n,
	}

	if n == 0 {
		// Copy in-degree
		var inDegree map[string]float64
		needInDegree := false
		if g.inDegreeValid && g.inDegreeCache != nil {
			inDegree = make(map[string]float64, len(g.inDegreeCache))
			for k, v := range g.inDegreeCache {
				inDegree[k] = v
			}
		} else {
			needInDegree = true
		}
		return snap, inDegree, needInDegree
	}

	// Copy all node IDs, reverse map, and adjacency lists
	nodes := g.dg.Nodes()
	for nodes.Next() {
		id := nodes.Node().ID()
		snap.allNodeIDs = append(snap.allNodeIDs, id)
		if hashID, ok := g.reverseMap[id]; ok {
			snap.reverseMap[id] = hashID
		}
		succs := g.dg.From(id)
		outDeg := succs.Len()
		snap.outDegree[id] = outDeg
		if outDeg > 0 {
			succList := make([]int64, 0, outDeg)
			succs.Reset()
			for succs.Next() {
				succList = append(succList, succs.Node().ID())
			}
			snap.adjacency[id] = succList
		}
	}

	// Map active node hash IDs to int64
	for _, hashID := range activeNodeIDs {
		if id, ok := g.idMap[hashID]; ok {
			snap.activeInt64s = append(snap.activeInt64s, id)
		}
	}

	// Copy in-degree cache if valid
	var inDegree map[string]float64
	needInDegree := false
	if g.inDegreeValid && g.inDegreeCache != nil {
		inDegree = make(map[string]float64, len(g.inDegreeCache))
		for k, v := range g.inDegreeCache {
			inDegree[k] = v
		}
	} else {
		needInDegree = true
	}

	return snap, inDegree, needInDegree
}

// computeFullPPROnSnapshot runs personalized PageRank on a pre-snapshotted full graph.
// No locks are needed — all data is owned by the caller.
func computeFullPPROnSnapshot(snap fullGraphSnapshot) map[string]float64 {
	n := snap.totalNodes
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
	if len(snap.activeInt64s) > 0 {
		weight := 1.0 / float64(len(snap.activeInt64s))
		for _, id := range snap.activeInt64s {
			teleport[id] += weight
		}
	}
	// If no active nodes mapped, fall back to uniform teleportation
	if len(teleport) == 0 {
		weight := 1.0 / float64(n)
		for _, id := range snap.allNodeIDs {
			teleport[id] = weight
		}
	}

	// Initialize rank vector uniformly
	rank := make(map[int64]float64, n)
	initVal := 1.0 / float64(n)
	for _, id := range snap.allNodeIDs {
		rank[id] = initVal
	}

	// Power iteration
	for iter := 0; iter < maxIter; iter++ {
		newRank := make(map[int64]float64, n)

		// Teleportation component: (1 - alpha) * teleport[id]
		for id, t := range teleport {
			newRank[id] = (1 - alpha) * t
		}

		// Random walk component
		for _, nodeID := range snap.allNodeIDs {
			outDeg := snap.outDegree[nodeID]
			if outDeg == 0 {
				// Dangling node: distribute its rank to the teleportation vector
				for id, t := range teleport {
					newRank[id] += alpha * rank[nodeID] * t
				}
			} else {
				// Distribute rank equally to successors
				share := alpha * rank[nodeID] / float64(outDeg)
				for _, succID := range snap.adjacency[nodeID] {
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
	result := make(map[string]float64, n)
	for id, r := range rank {
		if hashID, ok := snap.reverseMap[id]; ok {
			result[hashID] = r
		}
	}
	return result
}

// ComputeSearchSignalsSubgraph computes PPR on the candidate subgraph and returns
// InDegree from cache. Takes a brief read lock to snapshot the subgraph data and
// in-degree cache, then releases the lock before running PPR computation.
// This avoids holding any lock during the iterative PPR walk, which would serialize
// all concurrent searches unnecessarily.
func (g *GraphEngine) ComputeSearchSignalsSubgraph(seedIDs, candidateIDs []string) (ppr map[string]float64, inDegree map[string]float64) {
	// Phase 1: snapshot subgraph data and in-degree under a read lock
	snap, inDeg, needInDegree := g.snapshotSubgraphData(seedIDs, candidateIDs)

	// Phase 2: if in-degree cache was not populated, compute it under a write lock
	// (computeInDegreeLocked mutates the cache). This is rare — only on first call
	// after a graph rebuild.
	if needInDegree {
		var fullInDeg map[string]float64
		g.mu.Lock()
		if g.inDegreeValid && g.inDegreeCache != nil {
			// Another goroutine populated it while we waited
			fullInDeg = g.inDegreeCache
		} else {
			fullInDeg = g.computeInDegreeLocked()
		}
		g.mu.Unlock()

		// Filter to candidates only (matching snapshotSubgraphData behavior)
		candidateHashSet := make(map[string]bool, len(candidateIDs))
		for _, hashID := range candidateIDs {
			candidateHashSet[hashID] = true
		}
		inDeg = make(map[string]float64, len(candidateIDs))
		for k, v := range fullInDeg {
			if candidateHashSet[k] {
				inDeg[k] = v
			}
		}
	}
	inDegree = inDeg

	// Phase 3: run PPR on the snapshot with no lock held
	ppr = computePPROnSnapshot(snap)
	return ppr, inDegree
}

// subgraphSnapshot holds a deep-copied subset of the graph needed for PPR computation.
type subgraphSnapshot struct {
	// candidateSet: int64 node IDs that are candidates
	candidateSet map[int64]bool
	// adjacency: for each candidate node, the list of successor node IDs within the candidate set
	adjacency map[int64][]int64
	// seedIDs mapped to int64
	seedInt64s []int64
	// seedSuccessors: for seeds not in candidate set, their successors in candidate set
	seedSuccessors map[int64][]int64
	// seedPredecessors: for seeds not in candidate set, their predecessors in candidate set
	seedPredecessors map[int64][]int64
	// reverseMap subset: int64 → hash ID for candidates only
	reverseMap map[int64]string
}

// snapshotSubgraphData takes a read lock, deep-copies the subgraph data needed
// for PPR computation, copies the in-degree cache, and returns everything.
// Returns (snapshot, inDegree, needInDegree). If needInDegree is true, the caller
// must compute in-degree separately (requires a write lock).
func (g *GraphEngine) snapshotSubgraphData(seedIDs, candidateIDs []string) (subgraphSnapshot, map[string]float64, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	snap := subgraphSnapshot{
		candidateSet:     make(map[int64]bool, len(candidateIDs)),
		adjacency:        make(map[int64][]int64),
		seedSuccessors:   make(map[int64][]int64),
		seedPredecessors: make(map[int64][]int64),
		reverseMap:       make(map[int64]string),
	}

	// Map candidate hash IDs to int64 IDs
	for _, hashID := range candidateIDs {
		if id, ok := g.idMap[hashID]; ok {
			snap.candidateSet[id] = true
			// Copy reverse map entry
			snap.reverseMap[id] = hashID
		}
	}

	// Map seed hash IDs to int64 IDs
	for _, hashID := range seedIDs {
		if id, ok := g.idMap[hashID]; ok {
			snap.seedInt64s = append(snap.seedInt64s, id)
		}
	}

	// Copy adjacency lists for each candidate node (successors within candidate set)
	for nodeID := range snap.candidateSet {
		succs := g.dg.From(nodeID)
		var subgraphSuccs []int64
		for succs.Next() {
			succID := succs.Node().ID()
			if snap.candidateSet[succID] {
				subgraphSuccs = append(subgraphSuccs, succID)
			}
		}
		// Always store the entry (even if nil) so we can distinguish "no successors" from "missing"
		snap.adjacency[nodeID] = subgraphSuccs
	}

	// Copy seed neighbor lists (for seeds not in candidate set, used for teleport proxy)
	for _, seedID := range snap.seedInt64s {
		if !snap.candidateSet[seedID] {
			succs := g.dg.From(seedID)
			var filteredSuccs []int64
			for succs.Next() {
				succID := succs.Node().ID()
				if snap.candidateSet[succID] {
					filteredSuccs = append(filteredSuccs, succID)
				}
			}
			snap.seedSuccessors[seedID] = filteredSuccs

			preds := g.dg.To(seedID)
			var filteredPreds []int64
			for preds.Next() {
				predID := preds.Node().ID()
				if snap.candidateSet[predID] {
					filteredPreds = append(filteredPreds, predID)
				}
			}
			snap.seedPredecessors[seedID] = filteredPreds
		}
	}

	// Copy in-degree cache filtered to candidates only, so that downstream
	// normalizeMap uses the candidate-local max rather than the full-graph max.
	candidateHashSet := make(map[string]bool, len(candidateIDs))
	for _, hashID := range candidateIDs {
		candidateHashSet[hashID] = true
	}

	var inDegree map[string]float64
	needInDegree := false
	if g.inDegreeValid && g.inDegreeCache != nil {
		inDegree = make(map[string]float64, len(candidateIDs))
		for k, v := range g.inDegreeCache {
			if candidateHashSet[k] {
				inDegree[k] = v
			}
		}
	} else {
		needInDegree = true
	}

	return snap, inDegree, needInDegree
}

// computePPROnSnapshot runs personalized PageRank on a pre-snapshotted subgraph.
// No locks are needed — all data is owned by the caller.
func computePPROnSnapshot(snap subgraphSnapshot) map[string]float64 {
	if len(snap.candidateSet) == 0 {
		return nil
	}

	n := len(snap.candidateSet)

	const (
		alpha   = 0.85
		epsilon = 1e-4
		maxIter = 15
	)

	// Build teleportation vector from seeds (only those in candidate set)
	teleport := make(map[int64]float64)
	for _, id := range snap.seedInt64s {
		if snap.candidateSet[id] {
			teleport[id] += 1.0
		}
	}

	// Normalize teleport vector
	if len(teleport) == 0 {
		// Seeds are outside candidate set — use their neighbors in candidates as proxies
		for _, seedID := range snap.seedInt64s {
			for _, succID := range snap.seedSuccessors[seedID] {
				teleport[succID] += 1.0
			}
			for _, predID := range snap.seedPredecessors[seedID] {
				teleport[predID] += 1.0
			}
		}
		// If still empty, fall back to uniform; otherwise normalize proxy weights
		if len(teleport) == 0 {
			weight := 1.0 / float64(n)
			for id := range snap.candidateSet {
				teleport[id] = weight
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
	for id := range snap.candidateSet {
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
		for nodeID := range snap.candidateSet {
			subgraphSuccs := snap.adjacency[nodeID]
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
		if hashID, ok := snap.reverseMap[id]; ok {
			result[hashID] = r
		}
	}
	return result
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
	// Fast path: return cached results under read lock
	g.mu.RLock()
	if g.communityValid {
		var result []types.Community
		for i, nodeIDs := range g.communities {
			result = append(result, types.Community{
				ID:      i,
				NodeIDs: nodeIDs,
			})
		}
		mod := g.modularity
		g.mu.RUnlock()
		return result, mod
	}
	g.mu.RUnlock()

	// Slow path: snapshot under read lock, compute without lock, publish under write lock

	// Step 1: Snapshot the graph data and reverseMap under read lock
	undirected, snapshotReverseMap := g.snapshotUndirectedGraph()

	// Step 2: Run Louvain on snapshot (no lock held — readers are not blocked)
	if undirected.Nodes().Len() == 0 {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.communityValid {
			g.communityValid = true
			g.communities = nil
			g.modularity = 0
		}
		var result []types.Community
		for i, nodeIDs := range g.communities {
			result = append(result, types.Community{ID: i, NodeIDs: nodeIDs})
		}
		return result, g.modularity
	}

	reduced := community.Modularize(undirected, 1.0, nil)
	communities := reduced.Communities()
	mod := community.Q(undirected, communities, 1.0)

	// Step 3: Publish results under write lock
	g.mu.Lock()
	defer g.mu.Unlock()

	// Another goroutine may have populated the cache while we were computing
	if g.communityValid {
		var result []types.Community
		for i, nodeIDs := range g.communities {
			result = append(result, types.Community{ID: i, NodeIDs: nodeIDs})
		}
		return result, g.modularity
	}

	// Convert gonum IDs back to hash IDs using the snapshotted reverseMap
	// (not g.reverseMap, which may have changed if BuildFromEdges ran concurrently)
	g.communities = make([][]string, len(communities))
	for i, comm := range communities {
		var nodeIDs []string
		for _, node := range comm {
			if hashID, ok := snapshotReverseMap[node.ID()]; ok {
				nodeIDs = append(nodeIDs, hashID)
			}
		}
		g.communities[i] = nodeIDs
	}
	g.modularity = mod
	g.communityValid = true

	var result []types.Community
	for i, nodeIDs := range g.communities {
		result = append(result, types.Community{ID: i, NodeIDs: nodeIDs})
	}
	return result, mod
}

// snapshotUndirectedGraph builds an undirected copy of the directed graph under a read lock.
// It also snapshots the reverseMap so callers can convert gonum IDs back to hash IDs
// using the same mapping that was in place when the graph was copied.
// The returned graph and map are independent and safe to use without holding any lock.
func (g *GraphEngine) snapshotUndirectedGraph() (*simple.UndirectedGraph, map[int64]string) {
	g.mu.RLock()
	defer g.mu.RUnlock()

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

	allNodes := g.dg.Nodes()
	for allNodes.Next() {
		id := allNodes.Node().ID()
		if undirected.Node(id) == nil {
			undirected.AddNode(simple.Node(id))
		}
	}

	// Snapshot reverseMap so community detection uses consistent ID mappings
	revMapCopy := make(map[int64]string, len(g.reverseMap))
	for k, v := range g.reverseMap {
		revMapCopy[k] = v
	}

	return undirected, revMapCopy
}

// detectCommunitiesLocked is the inner implementation used by GetConnectors.
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
	parent := map[int64]int64{fromID: -1}
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
						for cur != -1 {
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
			succHash, ok := g.reverseMap[succs.Node().ID()]
			if !ok {
				continue
			}
			if c, ok := communityOf[succHash]; ok {
				neighborComms[c] = true
			}
		}
		preds := g.dg.To(id)
		for preds.Next() {
			predHash, ok := g.reverseMap[preds.Node().ID()]
			if !ok {
				continue
			}
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

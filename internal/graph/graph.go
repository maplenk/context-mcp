package graph

import (
	"fmt"
	"sort"
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
		g.communityValid = false
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

// ComputeInDegree computes the in-degree authority for all nodes, normalized to [0,1].
// In-degree counts how many other nodes have edges pointing TO each node.
func (g *GraphEngine) ComputeInDegree() map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.dg.Nodes().Len() == 0 {
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
		return result
	}

	for id, deg := range raw {
		if hashID, ok := g.reverseMap[id]; ok {
			result[hashID] = float64(deg) / float64(maxVal)
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

	for depth := 0; depth < maxDepth && len(meetingNodes) == 0; depth++ {
		// Expand forward frontier
		if len(frontierFwd) > 0 {
			var nextFwd []int64
			for _, nodeID := range frontierFwd {
				succs := g.dg.From(nodeID) // outgoing edges: node -> successors
				for succs.Next() {
					succID := succs.Node().ID()
					if _, visited := parentFwd[succID]; !visited {
						parentFwd[succID] = append(parentFwd[succID], nodeID)
						nextFwd = append(nextFwd, succID)
						if _, inBwd := parentBwd[succID]; inBwd {
							meetingNodes = append(meetingNodes, succID)
						}
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
						parentBwd[predID] = append(parentBwd[predID], nodeID)
						nextBwd = append(nextBwd, predID)
						if _, inFwd := parentFwd[predID]; inFwd {
							meetingNodes = append(meetingNodes, predID)
						}
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

	return []int64{target}
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
func (g *GraphEngine) GetConnectors(betweenness map[string]float64, limit int) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Build a community membership map
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

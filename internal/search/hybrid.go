package search

import (
	"sort"

	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
)

const rrfK = 60 // RRF constant

// HybridSearch combines lexical, semantic, and structural search
type HybridSearch struct {
	store    *storage.Store
	embedder embedding.Embedder
	graph    *graph.GraphEngine
}

// New creates a new HybridSearch engine
func New(store *storage.Store, embedder embedding.Embedder, graph *graph.GraphEngine) *HybridSearch {
	return &HybridSearch{
		store:    store,
		embedder: embedder,
		graph:    graph,
	}
}

// Search performs hybrid search combining lexical FTS5, semantic KNN, and PageRank
func (h *HybridSearch) Search(query string, limit int, activeFileNodeIDs []string) ([]types.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	// Fetch more candidates than needed for fusion
	candidateLimit := limit * 3
	if candidateLimit < 20 {
		candidateLimit = 20
	}

	// Path 1: Lexical search via FTS5 BM25
	lexicalResults, err := h.store.SearchLexical(query, candidateLimit)
	if err != nil {
		// FTS5 query syntax errors are non-fatal; return empty lexical results
		lexicalResults = nil
	}

	// Path 2: Semantic search via KNN (if embedder is available)
	var semanticResults []types.SearchResult
	if h.embedder != nil {
		queryVec, err := h.embedder.Embed(query)
		if err == nil {
			semanticResults, err = h.store.SearchSemantic(queryVec, candidateLimit)
			if err != nil {
				// Semantic search failure is non-fatal
				semanticResults = nil
			}
		}
	}

	// Reciprocal Rank Fusion
	fused := reciprocalRankFusion(lexicalResults, semanticResults)

	// Path 3: PageRank boost (if active files provided)
	if h.graph != nil && len(activeFileNodeIDs) > 0 {
		ranks := h.graph.PersonalizedPageRank(activeFileNodeIDs)
		if ranks != nil {
			applyPageRankBoost(fused, ranks)
		}
	}

	// Sort by final score descending
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].Score > fused[j].Score
	})

	// Trim to limit
	if len(fused) > limit {
		fused = fused[:limit]
	}

	return fused, nil
}

// reciprocalRankFusion merges two ranked result lists using RRF
// Score = sum of 1/(k + rank) across all lists where the item appears
func reciprocalRankFusion(lists ...[]types.SearchResult) []types.SearchResult {
	scores := make(map[string]float64)      // nodeID -> fused score
	nodes := make(map[string]types.ASTNode) // nodeID -> node data

	for _, list := range lists {
		for rank, result := range list {
			id := result.Node.ID
			scores[id] += 1.0 / float64(rrfK+rank+1) // rank is 0-indexed, RRF uses 1-indexed
			if _, exists := nodes[id]; !exists {
				nodes[id] = result.Node
			}
		}
	}

	results := make([]types.SearchResult, 0, len(scores))
	for id, score := range scores {
		results = append(results, types.SearchResult{
			Node:  nodes[id],
			Score: score,
		})
	}

	return results
}

// applyPageRankBoost multiplies each result's score by (1 + pagerank_weight)
func applyPageRankBoost(results []types.SearchResult, ranks map[string]float64) {
	for i := range results {
		if rank, ok := ranks[results[i].Node.ID]; ok {
			results[i].Score *= (1.0 + rank*100) // Scale PageRank up since values are typically very small
		}
	}
}

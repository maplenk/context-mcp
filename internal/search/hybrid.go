package search

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
)

// Composite scoring weights
const (
	weightPPR         = 0.35
	weightBM25        = 0.25
	weightBetweenness = 0.15
	weightInDegree    = 0.10
	weightSemantic    = 0.15

	defaultMaxPerFile = 3 // default max results per unique file_path
)

// stopWords are common English words filtered from search queries.
// Override via SetStopWords() to customize for other languages or domains.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "shall": true, "can": true,
	"and": true, "but": true, "or": true, "nor": true, "not": true,
	"to": true, "of": true, "in": true, "for": true, "on": true,
	"at": true, "by": true, "with": true, "from": true, "as": true,
	"into": true, "about": true, "between": true, "through": true,
	"this": true, "that": true, "these": true, "those": true,
	"it": true, "its": true, "if": true, "then": true, "else": true,
	"when": true, "where": true, "how": true, "what": true, "which": true,
	"who": true, "whom": true, "why": true,
}

// SetStopWords replaces the default stop word list with a custom set.
// Pass nil or an empty slice to disable stop word filtering entirely.
func SetStopWords(words []string) {
	newSet := make(map[string]bool, len(words))
	for _, w := range words {
		newSet[w] = true
	}
	stopWords = newSet
}

// camelCaseRe splits CamelCase identifiers into words.
// Matches: sequences of uppercase+lowercase (e.g. "Read"), all-lowercase runs, or all-uppercase runs.
var camelCaseRe = regexp.MustCompile(`[A-Z][a-z]*|[a-z]+|[A-Z]+`)

// fts5SpecialRe matches FTS5 special characters that must be sanitized before query construction.
var fts5SpecialRe = regexp.MustCompile(`[":(){}^+\-*]`)

// sanitizeFTS replaces FTS5 special characters with spaces to prevent query injection.
func sanitizeFTS(s string) string {
	return fts5SpecialRe.ReplaceAllString(s, " ")
}

// HybridSearch combines lexical, semantic, and structural search with composite scoring
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

// Search performs multi-signal composite search.
// maxPerFile controls how many results per unique file_path are returned (0 uses default of 3).
func (h *HybridSearch) Search(query string, limit int, activeFileNodeIDs []string, maxPerFile ...int) ([]types.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	perFileCap := defaultMaxPerFile
	if len(maxPerFile) > 0 && maxPerFile[0] > 0 {
		perFileCap = maxPerFile[0]
	}

	candidateLimit := limit * 5
	if candidateLimit < 30 {
		candidateLimit = 30
	}

	// Enhance query for FTS5
	ftsQuery := buildFTSQuery(query)

	// Path 1: Lexical search via FTS5 BM25
	lexicalResults, err := h.store.SearchLexical(ftsQuery, candidateLimit)
	if err != nil {
		// FTS5 query syntax errors — try original query as fallback
		lexicalResults, err = h.store.SearchLexical(query, candidateLimit)
		if err != nil {
			lexicalResults = nil
		}
	}

	// Path 2: Semantic search via KNN
	var semanticResults []types.SearchResult
	if h.embedder != nil {
		queryVec, err := h.embedder.Embed(query)
		if err == nil {
			semanticResults, err = h.store.SearchSemantic(queryVec, candidateLimit)
			if err != nil {
				semanticResults = nil
			}
		}
	}

	// Collect all candidate nodes
	candidates := collectCandidates(lexicalResults, semanticResults)
	if len(candidates) == 0 {
		return nil, nil
	}

	// Normalize BM25 scores to [0,1]
	bm25Scores := normalizeScores(lexicalResults)

	// Normalize semantic scores to [0,1]
	semanticScores := normalizeScores(semanticResults)

	// Query-time PPR seeded from top 10 FTS results
	var pprScores map[string]float64
	if h.graph != nil && len(lexicalResults) > 0 {
		seedCount := 10
		if seedCount > len(lexicalResults) {
			seedCount = len(lexicalResults)
		}
		seeds := make([]string, seedCount)
		for i := 0; i < seedCount; i++ {
			seeds[i] = lexicalResults[i].Node.ID
		}
		// Also include active file nodes as seeds
		seeds = append(seeds, activeFileNodeIDs...)
		raw := h.graph.PersonalizedPageRank(seeds)
		pprScores = normalizeMap(raw)
	}

	// Load betweenness scores (already [0,1] from index time)
	betweennessScores, _ := h.store.GetAllBetweenness()

	// Compute in-degree authority
	var inDegreeScores map[string]float64
	if h.graph != nil {
		inDegreeScores = h.graph.ComputeInDegree()
	}

	// Compute composite scores
	var results []types.SearchResult
	for nodeID, node := range candidates {
		bm25 := bm25Scores[nodeID]
		semantic := semanticScores[nodeID]
		ppr := safeGet(pprScores, nodeID)
		betweenness := safeGet(betweennessScores, nodeID)
		inDegree := safeGet(inDegreeScores, nodeID)

		composite := weightPPR*ppr +
			weightBM25*bm25 +
			weightBetweenness*betweenness +
			weightInDegree*inDegree +
			weightSemantic*semantic

		results = append(results, types.SearchResult{
			Node:  node,
			Score: composite,
		})
	}

	// Sort by composite score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Apply per-file cap
	results = applyPerFileCap(results, perFileCap)

	// Trim to limit
	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// buildFTSQuery enhances a query for FTS5 with CamelCase splitting, prefix matching, and stop word filtering
func buildFTSQuery(query string) string {
	// Sanitize FTS5 special characters to prevent query injection
	query = sanitizeFTS(query)

	// Split CamelCase tokens
	words := strings.Fields(query)
	var expanded []string

	for _, word := range words {
		// Check if it's CamelCase
		if isCamelCase(word) {
			parts := camelCaseRe.FindAllString(word, -1)
			if len(parts) > 1 {
				// Add both the original and split parts
				expanded = append(expanded, word)
				for _, p := range parts {
					lower := strings.ToLower(p)
					if !stopWords[lower] {
						expanded = append(expanded, p)
					}
				}
				continue
			}
		}

		lower := strings.ToLower(word)
		if stopWords[lower] {
			continue
		}
		expanded = append(expanded, word)
	}

	if len(expanded) == 0 {
		return query // fallback to original if everything was filtered
	}

	// Add prefix matching with *
	var terms []string
	for _, term := range expanded {
		// Only add prefix if term is at least 3 chars
		if len(term) >= 3 {
			terms = append(terms, term+"*")
		} else {
			terms = append(terms, term)
		}
	}

	return strings.Join(terms, " OR ")
}

// isCamelCase checks if a string contains CamelCase transitions
func isCamelCase(s string) bool {
	hasLower := false
	hasUpper := false
	for _, r := range s {
		if unicode.IsLower(r) {
			hasLower = true
		}
		if unicode.IsUpper(r) {
			hasUpper = true
		}
	}
	return hasLower && hasUpper
}

// collectCandidates merges all result lists into a single map of unique nodes
func collectCandidates(lists ...[]types.SearchResult) map[string]types.ASTNode {
	candidates := make(map[string]types.ASTNode)
	for _, list := range lists {
		for _, r := range list {
			if _, exists := candidates[r.Node.ID]; !exists {
				candidates[r.Node.ID] = r.Node
			}
		}
	}
	return candidates
}

// normalizeScores extracts scores from results and normalizes to [0,1]
func normalizeScores(results []types.SearchResult) map[string]float64 {
	if len(results) == 0 {
		return nil
	}

	scores := make(map[string]float64)
	var maxScore float64
	for _, r := range results {
		scores[r.Node.ID] = r.Score
		if r.Score > maxScore {
			maxScore = r.Score
		}
	}

	if maxScore <= 0 {
		return scores
	}

	normalized := make(map[string]float64)
	for id, score := range scores {
		normalized[id] = score / maxScore
	}
	return normalized
}

// normalizeMap normalizes a map of scores to [0,1]
func normalizeMap(m map[string]float64) map[string]float64 {
	if len(m) == 0 {
		return nil
	}

	var maxVal float64
	for _, v := range m {
		if v > maxVal {
			maxVal = v
		}
	}

	if maxVal <= 0 {
		return m
	}

	result := make(map[string]float64)
	for k, v := range m {
		result[k] = v / maxVal
	}
	return result
}

// safeGet returns the value from a map or 0 if nil or missing
func safeGet(m map[string]float64, key string) float64 {
	if m == nil {
		return 0
	}
	return m[key]
}

// applyPerFileCap limits results to maxPerFile entries per unique file_path
func applyPerFileCap(results []types.SearchResult, maxPerFile int) []types.SearchResult {
	fileCounts := make(map[string]int)
	var capped []types.SearchResult

	for _, r := range results {
		count := fileCounts[r.Node.FilePath]
		if count < maxPerFile {
			capped = append(capped, r)
			fileCounts[r.Node.FilePath] = count + 1
		}
	}

	return capped
}

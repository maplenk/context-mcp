package search

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
)

// Search Fusion Algorithm: Weighted Linear Combination
//
// The blueprint specified Reciprocal Rank Fusion (RRF): score = Sum(1/(k + rank_i)).
// This implementation uses weighted linear combination instead:
//   composite = 0.35*PPR + 0.25*BM25 + 0.15*Betweenness + 0.10*InDegree + 0.15*Semantic
//
// Rationale: The multi-signal composite approach (from the C reference project v0.8.0)
// outperforms RRF on the internal 15-case benchmark (30→123 improvement). RRF is
// rank-based and discards score magnitude; weighted composition preserves relative
// signal strength and enables per-signal tuning that RRF cannot express.

// Composite scoring weights
const (
	weightPPR         = 0.35
	weightBM25        = 0.25
	weightBetweenness = 0.15
	weightInDegree    = 0.10
	weightSemantic    = 0.15

	defaultMaxPerFile = 3 // default max results per unique file_path
)

func init() {
	sum := weightPPR + weightBM25 + weightBetweenness + weightInDegree + weightSemantic
	if math.Abs(sum-1.0) > 1e-9 {
		panic("composite scoring weights do not sum to 1.0")
	}
}

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
	// Domain stop words: query-structure noise that doesn't help code search
	"complete": true, "flow": true, "end": true, "logic": true,
	"entire": true, "full": true, "whole": true,
}

// stopWordsMu protects concurrent access to the stopWords map.
var stopWordsMu sync.RWMutex

// SetStopWords replaces the default stop word list with a custom set.
// Pass nil or an empty slice to disable stop word filtering entirely.
func SetStopWords(words []string) {
	newSet := make(map[string]bool, len(words))
	for _, w := range words {
		newSet[w] = true
	}
	stopWordsMu.Lock()
	stopWords = newSet
	stopWordsMu.Unlock()
}

// GetStopWords returns a copy of the current stop word set.
func GetStopWords() []string {
	stopWordsMu.RLock()
	defer stopWordsMu.RUnlock()
	result := make([]string, 0, len(stopWords))
	for w := range stopWords {
		result = append(result, w)
	}
	return result
}

// camelCaseRe splits CamelCase identifiers into words.
// Matches: sequences of uppercase+lowercase (e.g. "Read"), all-lowercase runs, or digit runs.
// Note: [A-Z]+ is unreachable because [A-Z][a-z0-9]* matches single uppercase letters first.
// The splitCamelCase function compensates by merging consecutive single-uppercase tokens.
var camelCaseRe = regexp.MustCompile(`[A-Z][a-z0-9]*|[a-z][a-z0-9]*|[A-Z]+|[0-9]+`)

// splitCamelCase splits a CamelCase identifier into words, correctly handling
// consecutive uppercase runs like "HTTPClient" → ["HTTP", "Client"].
// Go's RE2 lacks lookaheads, so we use a two-pass approach:
//  1. Split with camelCaseRe (which yields ["H","T","T","P","Client"] for "HTTPClient")
//  2. Merge consecutive single-uppercase-letter tokens into one string
func splitCamelCase(s string) []string {
	parts := camelCaseRe.FindAllString(s, -1)
	if len(parts) <= 1 {
		return parts
	}

	var merged []string
	i := 0
	for i < len(parts) {
		// Check if this is a single uppercase letter that starts a run
		if len(parts[i]) == 1 && parts[i][0] >= 'A' && parts[i][0] <= 'Z' {
			// Collect consecutive single-uppercase-letter tokens
			run := parts[i]
			j := i + 1
			for j < len(parts) && len(parts[j]) == 1 && parts[j][0] >= 'A' && parts[j][0] <= 'Z' {
				run += parts[j]
				j++
			}
			// If the next token starts with an uppercase letter followed by lowercase
			// (e.g., "Client" after "H","T","T","P"), the last uppercase letter of
			// the run belongs to that token: "HTTP" → "HTTP" stays, "Client" stays.
			// But the regex already split "Client" with uppercase start, so peel is
			// only needed when the next token starts with lowercase AND the run is
			// exactly 1 char (a single uppercase letter starting a CamelCase word).
			//
			// When the next token starts with lowercase (e.g., "element" after
			// "H","T","M","L"), the entire uppercase run is an acronym and the
			// lowercase token is a separate word: "HTML" + "element".
			if j < len(parts) && len(parts[j]) > 0 && parts[j][0] >= 'a' && parts[j][0] <= 'z' {
				if len(run) == 1 {
					// Single uppercase letter followed by lowercase token — combine them
					// (e.g., the "B" + "ar" from a split like "FooBar" edge case)
					merged = append(merged, run+parts[j])
					j++
				} else {
					// Multi-char uppercase run followed by lowercase token — keep
					// the acronym intact and treat the lowercase token separately
					// (e.g., "HTML" + "element" from "HTMLelement")
					merged = append(merged, run)
				}
			} else {
				merged = append(merged, run)
			}
			i = j
		} else {
			merged = append(merged, parts[i])
			i++
		}
	}
	return merged
}

// fts5SpecialRe matches FTS5 special characters that must be sanitized before query construction.
var fts5SpecialRe = regexp.MustCompile("[\"':(){}^+\\-*/`]")

// sanitizeFTS replaces FTS5 special characters with spaces and neutralizes boolean
// operators to prevent query injection. FTS5 only recognizes uppercase OR, AND, NOT,
// NEAR as operators, so lowercasing them makes them regular search terms.
func sanitizeFTS(s string) string {
	// First strip special characters
	s = fts5SpecialRe.ReplaceAllString(s, " ")
	// Neutralize FTS5 boolean operators by lowercasing them
	words := strings.Fields(s)
	for i, w := range words {
		upper := strings.ToUpper(w)
		if upper == "OR" || upper == "AND" || upper == "NOT" || upper == "NEAR" {
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
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
	if candidateLimit < 100 {
		candidateLimit = 100
	}

	// Enhance query for FTS5
	ftsQuery := buildFTSQuery(query)

	// Path 1: Lexical search via FTS5 BM25
	// Use SearchLexicalRaw because buildFTSQuery already sanitizes via sanitizeFTS
	// and constructs valid FTS5 syntax (OR operators, * prefix wildcards).
	// SearchLexical would double-sanitize, destroying OR→or and stripping *.
	lexicalResults, err := h.store.SearchLexicalRaw(ftsQuery, candidateLimit)
	if err != nil {
		// FTS5 query syntax errors — try sanitized original as fallback
		sanitizedFallback := sanitizeFTS(query)
		lexicalResults, err = h.store.SearchLexicalRaw(sanitizedFallback, candidateLimit)
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

	// Compute graph-derived signals (PPR + InDegree).
	// PPR runs on the candidate subgraph only (not the full graph) for speed.
	// Use the union of lexical AND semantic results as candidates so that
	// graph signals are computed even when only one search path has results.
	var pprScores map[string]float64
	var inDegreeScores map[string]float64
	if h.graph != nil && len(candidates) > 0 {
		// Build seed list from top lexical and semantic results
		seedCount := 10
		// Deduplicate seeds to prevent double-weighting in the PPR teleportation vector
		seeds := make([]string, 0, seedCount+len(activeFileNodeIDs))
		seedSet := make(map[string]bool)
		// Add top lexical results as seeds
		lexSeedCount := seedCount
		if lexSeedCount > len(lexicalResults) {
			lexSeedCount = len(lexicalResults)
		}
		for i := 0; i < lexSeedCount; i++ {
			id := lexicalResults[i].Node.ID
			if !seedSet[id] {
				seeds = append(seeds, id)
				seedSet[id] = true
			}
		}
		// Add top semantic results as seeds (fill remaining slots)
		for i := 0; i < len(semanticResults) && len(seeds) < seedCount; i++ {
			id := semanticResults[i].Node.ID
			if !seedSet[id] {
				seeds = append(seeds, id)
				seedSet[id] = true
			}
		}
		for _, id := range activeFileNodeIDs {
			if !seedSet[id] {
				seeds = append(seeds, id)
				seedSet[id] = true
			}
		}

		// Collect candidate IDs from lexical + semantic results
		candidateIDs := make([]string, 0, len(candidates))
		for id := range candidates {
			candidateIDs = append(candidateIDs, id)
		}

		rawPPR, rawInDegree := h.graph.ComputeSearchSignalsSubgraph(seeds, candidateIDs)
		pprScores = normalizeMap(rawPPR)
		// Note: rawInDegree comes from ComputeSearchSignalsSubgraph which returns
		// full-graph InDegree values (cached), not candidate-scoped counts.
		// normalizeMap rescales these to [0,1] relative to the candidate set's
		// min/max, which partially mitigates full-graph score compression.
		inDegreeScores = normalizeMap(rawInDegree)
	} else if h.graph != nil {
		inDegreeScores = normalizeMap(h.graph.ComputeInDegree())
	}

	// Load betweenness scores (already [0,1] from index time)
	betweennessScores, _ := h.store.GetAllBetweenness()

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

		// Penalize auto-generated/helper files to prevent them dominating results
		if isHelperFile(node.FilePath) {
			composite *= 0.3
		}

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

	// Snapshot stopWords under read lock for thread safety (M4)
	stopWordsMu.RLock()
	currentStopWords := stopWords
	stopWordsMu.RUnlock()

	// Split CamelCase tokens
	words := strings.Fields(query)
	var expanded []string

	for _, word := range words {
		// Check if it's CamelCase
		if isCamelCase(word) {
			parts := splitCamelCase(word)
			if len(parts) > 1 {
				// Add both the original and split parts
				expanded = append(expanded, word)
				for _, p := range parts {
					lower := strings.ToLower(p)
					if !currentStopWords[lower] {
						expanded = append(expanded, p)
					}
				}
				continue
			}
		}

		lower := strings.ToLower(word)
		if currentStopWords[lower] {
			continue
		}
		expanded = append(expanded, word)
	}

	if len(expanded) == 0 {
		// All terms were stop words — fall back to original terms with same
		// prefix matching logic applied so the fallback query is consistent.
		fallbackWords := strings.Fields(query)
		var fallbackTerms []string
		for _, term := range fallbackWords {
			if len(term) >= 3 {
				fallbackTerms = append(fallbackTerms, term+"*")
			} else {
				fallbackTerms = append(fallbackTerms, term)
			}
		}
		if len(fallbackTerms) == 0 {
			return query
		}
		return strings.Join(fallbackTerms, " OR ")
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

// normalizeScores extracts scores from results and normalizes to [0,1] using min-max
// normalization. This correctly handles negative scores (e.g., cosine distance can
// produce Score = 1.0 - distance, which ranges from -1 to 1).
func normalizeScores(results []types.SearchResult) map[string]float64 {
	if len(results) == 0 {
		return nil
	}

	scores := make(map[string]float64)
	var minScore, maxScore float64
	first := true
	for _, r := range results {
		// When duplicate IDs exist, keep the maximum score rather than
		// last-write-wins to avoid silently discarding better matches.
		if existing, ok := scores[r.Node.ID]; ok {
			if r.Score > existing {
				scores[r.Node.ID] = r.Score
			}
		} else {
			scores[r.Node.ID] = r.Score
		}
		if first {
			minScore = r.Score
			maxScore = r.Score
			first = false
		} else {
			if r.Score < minScore {
				minScore = r.Score
			}
			if r.Score > maxScore {
				maxScore = r.Score
			}
		}
	}

	scoreRange := maxScore - minScore
	if scoreRange <= 0 {
		// All scores identical — assign 1.0 so single-element or uniform
		// results are not undervalued in composite scoring.
		normalized := make(map[string]float64)
		for id := range scores {
			normalized[id] = 1.0
		}
		return normalized
	}

	normalized := make(map[string]float64)
	for id, score := range scores {
		normalized[id] = (score - minScore) / scoreRange
	}
	return normalized
}

// normalizeMap normalizes a map of scores to [0,1] using min-max normalization.
// This produces comparable scores where the range reflects actual significance
// rather than always mapping the maximum to 1.0.
func normalizeMap(m map[string]float64) map[string]float64 {
	if len(m) == 0 {
		return nil
	}

	var minVal, maxVal float64
	first := true
	for _, v := range m {
		if first {
			minVal = v
			maxVal = v
			first = false
		} else {
			if v < minVal {
				minVal = v
			}
			if v > maxVal {
				maxVal = v
			}
		}
	}

	valRange := maxVal - minVal
	if valRange <= 0 {
		// All values identical — assign 1.0 to avoid undervaluing uniform results
		result := make(map[string]float64)
		for k := range m {
			result[k] = 1.0
		}
		return result
	}

	result := make(map[string]float64)
	for k, v := range m {
		result[k] = (v - minVal) / valRange
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

// applyPerFileCap limits results to maxPerFile entries per unique file_path.
// Helper files are already penalized via the 0.3× score multiplier in Search(),
// so they use the same per-file cap as regular files to avoid double-penalizing.
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

// isHelperFile returns true for auto-generated or helper files that should
// have reduced per-file caps in search results.
func isHelperFile(filePath string) bool {
	lower := strings.ToLower(filePath)
	if strings.Contains(lower, "_ide_helper") || strings.HasSuffix(lower, ".d.ts") {
		return true
	}
	// Match "generated" only as a path component or file prefix to avoid false positives
	// like "UserGeneratedContent.php"
	for _, part := range strings.Split(lower, "/") {
		if part == "generated" || strings.HasPrefix(part, "generated.") ||
			strings.HasPrefix(part, "generated_") {
			return true
		}
	}
	return false
}

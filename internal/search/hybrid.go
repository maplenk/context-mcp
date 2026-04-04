package search

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/maplenk/context-mcp/internal/embedding"
	"github.com/maplenk/context-mcp/internal/graph"
	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/tokenutil"
	"github.com/maplenk/context-mcp/internal/types"
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

// defaultConfig is a package-level default used by backward-compatible free functions.
var defaultConfig = DefaultConfig()

// defaultMaxPerFile is a backward-compatible package-level variable for tests.
var defaultMaxPerFile = defaultConfig.MaxPerFile

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
	"entire": true, "whole": true,
	// Natural-language noise words that don't help code search
	// (management, complete, lifecycle removed per review — too aggressive)
	"flow": true, "happens": true, "places": true,
	"every": true, "there": true, "also": true,
	"each": true, "them": true, "they": true, "up": true, "than": true,
	"handling": true,
	// Targeted code method-name stop words: these specific terms generate
	// excessive noise when used as standalone query terms (e.g. "handle" matches
	// every middleware .handle() method, "end" matches every .end() call,
	// "handling" with Porter stemming becomes "handl" matching middleware noise).
	// Preserved inside CamelCase identifiers by the isCamelCase guard in buildFTSQuery.
	"handle": true, "end": true,
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

// queryAliases maps common developer/business vocabulary to code-level identifiers.
// When a query term matches a key, the alias terms are appended to the FTS query
// with OR, broadening lexical search to cover vocabulary gaps.
var queryAliases = map[string][]string{
	"omnichannel": {"easyecom", "unicommerce", "onlineorder"},
	"auth":        {"oauth", "login", "token", "session", "authenticate", "middleware"},
	"webhook":     {"callback", "hook", "dispatchwebhook"},
	"payment":     {"razorpay", "billing", "invoice"},
	"inventory":   {"stock", "stocktransaction", "stockledger", "warehouse"},
	"schema":      {"migration", "updateschema"},
	"logging":     {"sentry", "log", "errortracker"},
	"error":       {"exception", "handler", "sentry"},
	"loyalty":     {"loyaltypoint", "mobiquest", "easyrewardz"},
	"session":     {"sessionhandler", "cookie"},
	"sync":        {"listener", "event", "dispatch"},
	"database":    {"migration", "schema", "table"},
}

// camelCaseRe splits CamelCase identifiers into words.
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
	config   SearchConfig
}

// New creates a new HybridSearch engine with default configuration.
func New(store *storage.Store, embedder embedding.Embedder, graph *graph.GraphEngine) *HybridSearch {
	return NewWithConfig(store, embedder, graph, DefaultConfig())
}

// NewWithConfig creates a new HybridSearch engine with a custom configuration.
func NewWithConfig(store *storage.Store, embedder embedding.Embedder, graph *graph.GraphEngine, config SearchConfig) *HybridSearch {
	return &HybridSearch{
		store:    store,
		embedder: embedder,
		graph:    graph,
		config:   config,
	}
}

// Search performs multi-signal composite search.
// maxPerFile controls how many results per unique file_path are returned (0 uses default of 3).
func (h *HybridSearch) Search(query string, limit int, activeFileNodeIDs []string, maxPerFile ...int) ([]types.SearchResult, error) {
	// Empty/whitespace queries have nothing to search — return early
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	if limit <= 0 {
		limit = 10
	}

	perFileCap := h.config.MaxPerFile
	if len(maxPerFile) > 0 && maxPerFile[0] > 0 {
		perFileCap = maxPerFile[0]
	}

	candidateLimit := limit * h.config.CandidateMultiplier
	if candidateLimit < h.config.CandidateMinimum {
		candidateLimit = h.config.CandidateMinimum
	}

	// M5: Track signal source failures to return an error when ALL sources fail
	var ftsErr, semanticErr error

	// Clean structural phrases before FTS processing
	cleanedQuery := cleanQuery(query)

	// Enhance query for FTS5
	ftsQuery := buildFTSQuery(cleanedQuery)

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
			ftsErr = err
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
				semanticErr = err
				semanticResults = nil
			}
		} else {
			semanticErr = err
		}
	}

	// Collect all candidate nodes
	candidates := collectCandidates(lexicalResults, semanticResults)
	if len(candidates) == 0 {
		// M5: If no candidates and at least one source had an error, report it
		var failures []string
		if ftsErr != nil {
			failures = append(failures, fmt.Sprintf("FTS: %v", ftsErr))
		}
		if semanticErr != nil {
			failures = append(failures, fmt.Sprintf("semantic: %v", semanticErr))
		}
		if len(failures) > 0 {
			return nil, fmt.Errorf("all signal sources failed: %s", strings.Join(failures, "; "))
		}
		return nil, nil
	}

	// Normalize BM25 scores to [0,1]
	bm25Scores := normalizeScores(lexicalResults)

	// BM25 score floor: prevent low-frequency node types from collapsing to 0
	// after min-max normalization. If a node had a positive raw BM25 score but
	// normalized to < 0.05, give it a minimum floor so it participates in
	// composite scoring.
	for _, r := range lexicalResults {
		if r.Score > 0 {
			if norm, ok := bm25Scores[r.Node.ID]; ok && norm < h.config.BM25ScoreFloor {
				bm25Scores[r.Node.ID] = h.config.BM25ScoreFloor
			}
		}
	}

	// Normalize semantic scores to [0,1]
	semanticScores := normalizeScores(semanticResults)

	// Compute graph-derived signals (PPR + InDegree).
	// PPR runs on the candidate subgraph only (not the full graph) for speed.
	var pprScores map[string]float64
	var inDegreeScores map[string]float64
	if h.graph != nil {
		// Determine seed source: prefer lexical results, fall back to semantic
		seedSource := lexicalResults
		if len(seedSource) == 0 {
			seedSource = semanticResults
		}

		if len(seedSource) > 0 {
			seedCount := 10
			if seedCount > len(seedSource) {
				seedCount = len(seedSource)
			}
			// Deduplicate seeds to prevent double-weighting in the PPR teleportation vector
			seeds := make([]string, 0, seedCount+len(activeFileNodeIDs))
			seedSet := make(map[string]bool)
			for i := 0; i < seedCount; i++ {
				id := seedSource[i].Node.ID
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
			// rawInDegree contains global in-degree values filtered to candidate IDs only.
			// normalizeMap divides by the candidate-local max, so scores reflect
			// relative importance among candidates rather than full-graph popularity.
			inDegreeScores = normalizeMap(rawInDegree)
		} else {
			inDegreeScores = normalizeMap(h.graph.ComputeInDegree())
		}
	}

	// Load betweenness scores (already [0,1] from index time)
	betweennessScores, _ := h.store.GetAllBetweenness()

	// Detect query kind once for node-type boosting (uses original query, not cleanedQuery)
	kind := detectQueryKind(query)

	// Compute composite scores
	var results []types.SearchResult
	for nodeID, node := range candidates {
		bm25 := bm25Scores[nodeID]
		semantic := semanticScores[nodeID]
		ppr := safeGet(pprScores, nodeID)
		betweenness := safeGet(betweennessScores, nodeID)
		inDegree := safeGet(inDegreeScores, nodeID)

		composite := h.config.WeightPPR*ppr +
			h.config.WeightBM25*bm25 +
			h.config.WeightBetweenness*betweenness +
			h.config.WeightInDegree*inDegree +
			h.config.WeightSemantic*semantic

		// Apply path-based penalty: migrations, tests, vendor, etc. score lower
		composite *= h.pathPenalty(node.FilePath)

		// Apply node-type boost based on detected query intent
		composite *= h.nodeTypeBoost(kind, node.NodeType)

		results = append(results, types.SearchResult{
			Node:  node,
			Score: composite,
			Breakdown: &types.ScoreBreakdown{
				PPR:         ppr,
				BM25:        bm25,
				Betweenness: betweenness,
				InDegree:    inDegree,
				Semantic:    semantic,
			},
		})
	}

	// Graph-neighborhood expansion: expand 1-hop from top seeds, filtered
	// by query relevance to avoid flooding results with irrelevant neighbors.
	if h.graph != nil {
		results = h.expandFromSeeds(results, candidates, bm25Scores, semanticScores,
			pprScores, betweennessScores, inDegreeScores, limit, cleanedQuery)
	}

	// Sort by composite score descending, with stable ID tiebreak for determinism
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Node.ID < results[j].Node.ID
	})

	// Apply per-file cap
	results = h.applyPerFileCap(results, perFileCap)

	// Trim to limit
	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// structuralPhrases are multi-word phrases that appear in natural-language queries
// but carry no code-search value. They are stripped before FTS query construction
// to prevent their individual tokens from polluting results.
var structuralPhrases = []string{
	"end to end",
	"start to finish",
	"step by step",
	"front to back",
	"beginning to end",
	"how does",
	"how do",
	"how is",
	"how are",
	"what is",
	"what are",
	"where is",
	"where are",
	"show me",
	"find me",
	"look for",
	"looking for",
}

// cleanQuery strips structural/conversational phrases from a query before
// FTS processing. This prevents phrase fragments (e.g., "end" from "end to end")
// from polluting search results with unrelated code symbols.
func cleanQuery(query string) string {
	lower := strings.ToLower(query)
	for _, phrase := range structuralPhrases {
		lower = strings.ReplaceAll(lower, phrase, " ")
	}
	// Collapse multiple spaces
	parts := strings.Fields(lower)
	return strings.Join(parts, " ")
}

// queryKind classifies user intent to enable node-type boosting.
// Credit: query kind detection heuristic adapted from code-review-graph
// (github.com/tirth8205/code-review-graph).
type queryKind int

const (
	queryKindNatural  queryKind = iota
	queryKindRoute
	queryKindClass
	queryKindFunction
)

// detectQueryKind analyzes a search query to determine intent (route, class,
// function, or natural language). Uses the ORIGINAL query (not cleanedQuery)
// so route terms like "api" and "endpoint" are preserved.
func detectQueryKind(query string) queryKind {
	lower := strings.ToLower(query)
	words := strings.Fields(lower)

	// Route intent: HTTP verbs, path patterns, OR route-related terms
	routeTerms := map[string]bool{"api": true, "endpoint": true, "endpoints": true, "route": true, "routes": true, "url": true, "login": true}
	if strings.Contains(lower, "/v") || strings.Contains(lower, "post ") || strings.Contains(lower, "get ") || strings.Contains(lower, "put ") || strings.Contains(lower, "delete ") || strings.Contains(lower, "patch ") {
		return queryKindRoute
	}
	for _, w := range words {
		if routeTerms[w] {
			return queryKindRoute
		}
	}

	// Class intent: any word is PascalCase (starts uppercase, has lowercase)
	for _, w := range strings.Fields(query) { // use original case
		if len(w) >= 2 && unicode.IsUpper(rune(w[0])) && containsLower(w) && !isAllUpper(w) {
			return queryKindClass
		}
	}

	// Function intent: any word has underscore (snake_case)
	for _, w := range words {
		if strings.Contains(w, "_") {
			return queryKindFunction
		}
	}

	return queryKindNatural
}

// containsLower returns true if s contains at least one lowercase letter.
func containsLower(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return true
		}
	}
	return false
}

// isAllUpper returns true if every letter in s is uppercase.
func isAllUpper(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && !unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

// nodeTypeBoost returns a scoring multiplier based on the query kind and node type.
// Route queries boost route nodes and methods; class queries boost class/interface
// nodes; function queries boost function nodes. Uses h.config for boost values.
func (h *HybridSearch) nodeTypeBoost(kind queryKind, nodeType types.NodeType) float64 {
	switch kind {
	case queryKindRoute:
		switch nodeType {
		case types.NodeTypeRoute:
			return h.config.RouteBoost
		case types.NodeTypeMethod:
			return h.config.RouteMethodBoost
		}
	case queryKindClass:
		switch nodeType {
		case types.NodeTypeClass, types.NodeTypeInterface:
			return h.config.ClassBoost
		}
	case queryKindFunction:
		switch nodeType {
		case types.NodeTypeFunction:
			return h.config.FunctionBoost
		}
	}
	return 1.0
}

// nodeTypeBoostDefault is a backward-compatible free function that uses DefaultConfig.
// Kept for tests that call nodeTypeBoost() as a package-level function.
func nodeTypeBoost(kind queryKind, nodeType types.NodeType) float64 {
	hs := &HybridSearch{config: defaultConfig}
	return hs.nodeTypeBoost(kind, nodeType)
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
			parts := tokenutil.SplitCamelCase(word)
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
		return query // fallback to original if everything was filtered
	}

	// Apply query alias expansion — prefix-based matching broadens vocabulary coverage.
	// For each query term, if any alias key (min 3 chars) is a prefix of the term,
	// the alias values are appended. E.g. "authentication" has prefix "auth" → expands.
	var withAliases []string
	for _, term := range expanded {
		withAliases = append(withAliases, term)
		lower := strings.ToLower(term)
		for aliasKey, aliases := range queryAliases {
			if len(aliasKey) >= 3 && strings.HasPrefix(lower, aliasKey) {
				withAliases = append(withAliases, aliases...)
			}
		}
	}
	expanded = withAliases

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

	// First pass: build deduped scores map, keeping max score per node
	scores := make(map[string]float64)
	for _, r := range results {
		if existing, ok := scores[r.Node.ID]; !ok || r.Score > existing {
			scores[r.Node.ID] = r.Score
		}
	}

	// Second pass: compute min/max from deduped map values only
	var minScore, maxScore float64
	first := true
	for _, score := range scores {
		if first {
			minScore = score
			maxScore = score
			first = false
		} else {
			if score < minScore {
				minScore = score
			}
			if score > maxScore {
				maxScore = score
			}
		}
	}

	scoreRange := maxScore - minScore
	if scoreRange <= 0 {
		// All scores identical — assign uniform 0.5
		normalized := make(map[string]float64)
		for id := range scores {
			normalized[id] = 0.5
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
		// All values identical — assign 0.5
		result := make(map[string]float64)
		for k := range m {
			result[k] = 0.5
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
// Files with pathPenalty <= NonCoreThreshold (generated, vendor, IDE helpers, tests, migrations)
// are capped at 1 to prevent large auto-generated files from dominating results.
func (h *HybridSearch) applyPerFileCap(results []types.SearchResult, maxPerFile int) []types.SearchResult {
	fileCounts := make(map[string]int)
	var capped []types.SearchResult

	for _, r := range results {
		cap := maxPerFile
		if h.pathPenalty(r.Node.FilePath) <= h.config.NonCoreThreshold {
			cap = 1 // non-core files (generated/vendor/test/etc): 1 result max
		}
		count := fileCounts[r.Node.FilePath]
		if count < cap {
			capped = append(capped, r)
			fileCounts[r.Node.FilePath] = count + 1
		}
	}

	return capped
}

// applyPerFileCap (free function) is a backward-compatible wrapper that uses DefaultConfig.
// Kept for tests that call applyPerFileCap() as a package-level function.
func applyPerFileCap(results []types.SearchResult, maxPerFile int) []types.SearchResult {
	hs := &HybridSearch{config: defaultConfig}
	return hs.applyPerFileCap(results, maxPerFile)
}

// pathPenalty returns a scoring multiplier for a file path.
// Core source files return 1.0; non-core paths return lower values
// so they don't dominate results over the actual implementation files.
// Uses h.config for penalty values.
func (h *HybridSearch) pathPenalty(filePath string) float64 {
	lower := strings.ToLower(filePath)

	// Auto-generated / IDE helpers — strongest penalty
	if strings.Contains(lower, "_ide_helper") || strings.HasSuffix(lower, ".d.ts") {
		return h.config.PenaltyGenerated
	}
	for _, part := range strings.Split(lower, "/") {
		if part == "generated" || strings.HasPrefix(part, "generated.") ||
			strings.HasPrefix(part, "generated_") {
			return h.config.PenaltyGenerated
		}
	}

	// Vendor / third-party dependencies
	for _, part := range strings.Split(lower, "/") {
		if part == "vendor" || part == "node_modules" || part == "lib" {
			return h.config.PenaltyVendor
		}
	}

	// Database migrations — schema changes, not business logic
	for _, part := range strings.Split(lower, "/") {
		if part == "migrations" || part == "migration" {
			return h.config.PenaltyMigration
		}
	}

	// Test files
	for _, part := range strings.Split(lower, "/") {
		if part == "tests" || part == "test" || part == "__tests__" || part == "spec" {
			return h.config.PenaltyTest
		}
	}
	// File-name patterns for tests
	base := lower
	if idx := strings.LastIndex(lower, "/"); idx >= 0 {
		base = lower[idx+1:]
	}
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.js") ||
		strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".spec.js") ||
		strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, "test.php") {
		return h.config.PenaltyTest
	}
	// CamelCase test convention (e.g., OrderControllerTest.php, FooTest.java)
	if strings.Contains(base, "test.") || strings.Contains(base, "Test.") {
		return h.config.PenaltyTest
	}

	// Examples
	for _, part := range strings.Split(lower, "/") {
		if part == "examples" || part == "example" {
			return h.config.PenaltyExample
		}
	}

	// Config directories — useful but not primary business logic
	for _, part := range strings.Split(lower, "/") {
		if part == "config" {
			return h.config.PenaltyConfig
		}
	}

	return 1.0
}

// pathPenalty (free function) is a backward-compatible wrapper that uses DefaultConfig.
// Kept for tests that call pathPenalty() as a package-level function.
func pathPenalty(filePath string) float64 {
	hs := &HybridSearch{config: defaultConfig}
	return hs.pathPenalty(filePath)
}

// expandFromSeeds takes the initial scored results, expands 1 hop from the
// top seeds via graph edges, fetches newly discovered nodes from the store,
// scores them, and merges them into the result set.
// queryTerms is used to filter expanded neighbors for query relevance.
func (h *HybridSearch) expandFromSeeds(
	results []types.SearchResult,
	existingCandidates map[string]types.ASTNode,
	bm25Scores, semanticScores, pprScores, betweennessScores, inDegreeScores map[string]float64,
	limit int,
	queryTerms string,
) []types.SearchResult {
	// Take top-N seeds (pre-sort by score to get the best ones, stable tiebreak)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Node.ID < results[j].Node.ID
	})

	seedCount := h.config.ExpansionSeedCount
	if seedCount > len(results) {
		seedCount = len(results)
	}

	// Collect neighbor IDs from top seeds (1 hop: callees + callers)
	// Track how many distinct seeds connect to each neighbor for multi-seed signal.
	neighborSeedCount := make(map[string]int)
	for i := 0; i < seedCount; i++ {
		seedID := results[i].Node.ID
		seen := make(map[string]bool) // avoid double-counting same seed
		for _, callee := range h.graph.GetCallees(seedID) {
			if _, exists := existingCandidates[callee]; !exists && !seen[callee] {
				neighborSeedCount[callee]++
				seen[callee] = true
			}
		}
		for _, caller := range h.graph.GetCallers(seedID) {
			if _, exists := existingCandidates[caller]; !exists && !seen[caller] {
				neighborSeedCount[caller]++
				seen[caller] = true
			}
		}
	}

	if len(neighborSeedCount) == 0 {
		return results
	}

	// Parse query terms for relevance filtering
	terms := strings.Fields(strings.ToLower(queryTerms))

	// Collect all neighbors, then sort for deterministic expansion
	neighborIDs := make([]string, 0, len(neighborSeedCount))
	for id := range neighborSeedCount {
		neighborIDs = append(neighborIDs, id)
	}
	sort.Slice(neighborIDs, func(i, j int) bool {
		ci, cj := neighborSeedCount[neighborIDs[i]], neighborSeedCount[neighborIDs[j]]
		if ci != cj {
			return ci > cj // most-connected neighbors first
		}
		return neighborIDs[i] < neighborIDs[j] // stable tiebreak
	})
	// Cap expansion to prevent result bloat
	maxExpansion := h.config.ExpansionMaxNeighbors
	if len(neighborIDs) > maxExpansion {
		neighborIDs = neighborIDs[:maxExpansion]
	}

	// Fetch neighbor nodes from store — limit additions to avoid flooding results
	maxAdded := limit / h.config.ExpansionMaxAddedDivisor
	if maxAdded < 3 {
		maxAdded = 3
	}
	added := 0
	for _, nID := range neighborIDs {
		if added >= maxAdded {
			break
		}
		node, err := h.store.GetNode(nID)
		if err != nil || node == nil {
			continue
		}

		// Query-relevance filter: only include neighbors that contain a query
		// term in their symbol name, file path, or content summary.
		// Exception: neighbors connected to 2+ seeds are included regardless
		// (strong structural signal).
		if neighborSeedCount[nID] < 2 && len(terms) > 0 {
			relevant := false
			lowerSymbol := strings.ToLower(node.SymbolName)
			lowerPath := strings.ToLower(node.FilePath)
			lowerContent := strings.ToLower(node.ContentSum)
			for _, term := range terms {
				if strings.Contains(lowerSymbol, term) ||
					strings.Contains(lowerPath, term) ||
					strings.Contains(lowerContent, term) {
					relevant = true
					break
				}
			}
			if !relevant {
				continue
			}
		}

		// Score the neighbor using whatever signals we have
		bm25 := safeGet(bm25Scores, nID)
		semantic := safeGet(semanticScores, nID)
		ppr := safeGet(pprScores, nID)
		betweenness := safeGet(betweennessScores, nID)
		inDegree := safeGet(inDegreeScores, nID)

		composite := h.config.WeightPPR*ppr +
			h.config.WeightBM25*bm25 +
			h.config.WeightBetweenness*betweenness +
			h.config.WeightInDegree*inDegree +
			h.config.WeightSemantic*semantic

		// Graph-expanded nodes get a connectivity bonus: they were reachable
		// from high-scoring seeds, which means they're structurally relevant
		// even if they scored low on lexical/semantic signals.
		// Give them a minimum score based on the seed they expanded from.
		if composite == 0 {
			// Find the best seed score that connects to this neighbor
			bestSeedScore := 0.0
			for i := 0; i < seedCount; i++ {
				seedID := results[i].Node.ID
				// Check if this neighbor is connected to this seed
				for _, callee := range h.graph.GetCallees(seedID) {
					if callee == nID && results[i].Score > bestSeedScore {
						bestSeedScore = results[i].Score
					}
				}
				for _, caller := range h.graph.GetCallers(seedID) {
					if caller == nID && results[i].Score > bestSeedScore {
						bestSeedScore = results[i].Score
					}
				}
			}
			// Give neighbor a modest fraction of the seed's score —
			// low enough to not displace direct lexical/semantic hits
			composite = bestSeedScore * h.config.ExpansionBonus
		}

		// Apply path-based penalty to expanded nodes
		composite *= h.pathPenalty(node.FilePath)

		if composite > 0 {
			added++
			results = append(results, types.SearchResult{
				Node:  *node,
				Score: composite,
				Breakdown: &types.ScoreBreakdown{
					PPR:         ppr,
					BM25:        bm25,
					Betweenness: betweenness,
					InDegree:    inDegree,
					Semantic:    semantic,
				},
			})
		}
	}

	return results
}

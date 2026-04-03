//go:build fts5 && realrepo

package tests

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// benchmarkQueries maps query IDs to the search queries used for grading.
var benchmarkQueries = map[string]string{
	"A1": "FiscalYearController",
	"A2": "OrderController",
	"A3": "order API endpoints",
	"A4": "payment files",
	"B1": "payment processing flow",
	"B2": "authentication and session management",
	"B3": "loyalty program logic",
	"B4": "database schema management",
	"B5": "error handling and logging",
	"B6": "omnichannel order sync",
	"C1": "end to end order creation flow",
	"C2": "stock transaction lifecycle",
	"C3": "webhook handling and dispatch",
	"C4": "OpenTelemetry tracing pipeline",
	"C5": "inventory database write flow",
}

// expectedItem represents one rubric item from the human answer key.
type expectedItem struct {
	raw      string     // original line from the answer file
	kind     answerKind // file, route, symbol, skip
	filePath string     // for file/symbol items: significant suffix (e.g., "OrderController.php")
	route    string     // for route items: the route pattern
	symbol   string     // for symbol/method items: the symbol name
	method   string     // for method items: the method name (e.g., "stockTransaction")
}

type answerKind int

const (
	answerSkip   answerKind = iota
	answerFile              // match on file path
	answerRoute             // match on route pattern
	answerSymbol            // match on symbol name
)

// httpMethodRe matches lines that start with an HTTP method or a leading slash (route patterns).
var httpMethodRe = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+`)

// parseAnswerKey reads benchmarks/human-answers.md and returns a map of query ID -> expected items.
func parseAnswerKey(t *testing.T) map[string][]expectedItem {
	t.Helper()

	// Find the answer key relative to the test directory
	answerPath := filepath.Join("..", "benchmarks", "human-answers.md")
	if _, err := os.Stat(answerPath); os.IsNotExist(err) {
		// Try from repo root via working directory heuristic
		wd, _ := os.Getwd()
		answerPath = filepath.Join(wd, "..", "benchmarks", "human-answers.md")
	}

	f, err := os.Open(answerPath)
	if err != nil {
		t.Fatalf("failed to open answer key at %s: %v", answerPath, err)
	}
	defer f.Close()

	result := make(map[string][]expectedItem)
	scanner := bufio.NewScanner(f)

	var currentID string
	inCodeBlock := false

	// Valid query IDs
	validIDs := make(map[string]bool)
	for id := range benchmarkQueries {
		validIDs[id] = true
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Check for query ID header (e.g., "A1", "B2")
		if validIDs[trimmed] && !inCodeBlock {
			currentID = trimmed
			continue
		}

		// Toggle code block
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}

		// Only process lines inside code blocks under a valid ID
		if !inCodeBlock || currentID == "" {
			continue
		}

		item := classifyLine(trimmed)
		if item.kind != answerSkip {
			result[currentID] = append(result[currentID], item)
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("error reading answer key: %v", err)
	}

	return result
}

// filePathInLineRe extracts a file path (containing / and ending with a code extension) embedded in a longer line.
var filePathInLineRe = regexp.MustCompile(`(?:^|[\s—])((app|config|database|resources|routes|tests|App)/\S+\.(?:php|go|js|ts|sql|blade\.php))`)

// methodRefRe matches method references like "app/Order.php — manageLoyaltyPoint()" or "Inventory.php:stockTransaction()"
var methodRefRe = regexp.MustCompile(`(\S+\.php)(?::|\s+—\s+)(\w+)\(\)`)

// classifyLine determines whether a line from the answer key is a file path, route, symbol, or skip.
func classifyLine(line string) expectedItem {
	trimmed := strings.TrimSpace(line)

	// Skip empty lines
	if trimmed == "" {
		return expectedItem{raw: line, kind: answerSkip}
	}

	// Skip lines with wildcards or ellipses
	if strings.Contains(trimmed, "*.") || strings.Contains(trimmed, "..") {
		return expectedItem{raw: line, kind: answerSkip}
	}

	// Strip leading bullet/number markers: "- ", "1. ", "2. ", etc.
	cleaned := stripLeadingMarker(trimmed)

	// Check for HTTP method routes: "POST /v1/merchant/{chainID}/login"
	if httpMethodRe.MatchString(cleaned) {
		parts := strings.SplitN(cleaned, " ", 2)
		if len(parts) == 2 {
			route := strings.TrimSpace(parts[1])
			return expectedItem{raw: line, kind: answerRoute, route: route}
		}
	}

	// Check for route patterns starting with / (but not absolute /Users/ paths)
	if strings.HasPrefix(cleaned, "/") && !strings.HasPrefix(cleaned, "/Users/") {
		return expectedItem{raw: line, kind: answerRoute, route: cleaned}
	}

	// Check for absolute paths — extract relative part after qbapi/
	if strings.HasPrefix(cleaned, "/Users/") {
		idx := strings.Index(cleaned, "qbapi/")
		if idx >= 0 {
			relPath := cleaned[idx+len("qbapi/"):]
			return expectedItem{
				raw:      line,
				kind:     answerFile,
				filePath: relPath,
			}
		}
		return expectedItem{raw: line, kind: answerSkip}
	}

	// Check for method references like "Inventory.php:stockTransaction()" or
	// "app/Order.php — manageLoyaltyPoint()"
	if m := methodRefRe.FindStringSubmatch(cleaned); m != nil {
		return expectedItem{
			raw:      line,
			kind:     answerSymbol,
			filePath: m[1],
			symbol:   m[2],
			method:   m[2],
		}
	}

	// Check for pure file paths (contain / and end with a code extension, no extra text)
	if looksLikeFilePath(cleaned) && !strings.Contains(cleaned, " ") {
		return expectedItem{
			raw:      line,
			kind:     answerFile,
			filePath: cleaned,
		}
	}

	// Check for bare symbol names (e.g., "StockLedger.php", "InventoryController.php", "chain.sql")
	if !strings.Contains(cleaned, " ") &&
		(strings.HasSuffix(cleaned, ".php") || strings.HasSuffix(cleaned, ".go") ||
			strings.HasSuffix(cleaned, ".js") || strings.HasSuffix(cleaned, ".sql")) {
		return expectedItem{
			raw:      line,
			kind:     answerFile,
			filePath: cleaned,
		}
	}

	// Try to extract embedded file paths from lines with mixed content like:
	// "1. Razorpay — app/razorPay.php"
	// "- resources/views/invoices/order.blade.php — Order invoices"
	if m := filePathInLineRe.FindStringSubmatch(trimmed); m != nil {
		extractedPath := m[1]
		return expectedItem{
			raw:      line,
			kind:     answerFile,
			filePath: extractedPath,
		}
	}

	// Final fallback: file paths with spaces in surrounding text
	if looksLikeFilePath(cleaned) {
		// Extract just the path portion
		if m := filePathInLineRe.FindStringSubmatch(cleaned); m != nil {
			return expectedItem{
				raw:      line,
				kind:     answerFile,
				filePath: m[1],
			}
		}
	}

	// Skip everything else (descriptions, section headers, etc.)
	return expectedItem{raw: line, kind: answerSkip}
}

// isDescriptiveLine is kept for reference but no longer used as a front-gate.
// The classifier now checks for concrete patterns first (routes, paths, method refs)
// and only falls through to skip as a last resort.

// stripLeadingMarker removes bullet/number markers from a line.
func stripLeadingMarker(s string) string {
	s = strings.TrimSpace(s)
	// Strip "- " prefix
	if strings.HasPrefix(s, "- ") {
		s = strings.TrimSpace(s[2:])
	}
	// Strip numbered prefixes like "1. ", "2. "
	if len(s) >= 3 && s[0] >= '0' && s[0] <= '9' && s[1] == '.' && s[2] == ' ' {
		s = strings.TrimSpace(s[3:])
	}
	return s
}

// looksLikeFilePath checks if a string looks like a relative file path.
func looksLikeFilePath(s string) bool {
	// Must contain at least one path separator and a code extension
	if !strings.Contains(s, "/") {
		return false
	}
	extensions := []string{".php", ".go", ".js", ".ts", ".py", ".sql", ".json", ".yaml", ".yml", ".blade.php"}
	for _, ext := range extensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

// queryTier returns the tier (A, B, C) for a query ID.
func queryTier(id string) string {
	if len(id) > 0 {
		return string(id[0])
	}
	return ""
}

// queryLimit returns the search result limit for a given tier.
func queryLimit(tier string) int {
	switch tier {
	case "A":
		return 10
	default: // B, C
		return 20
	}
}

// matchItem checks whether a search result set contains the expected item.
func matchItem(item expectedItem, results []searchResultInfo) bool {
	switch item.kind {
	case answerFile:
		return matchFilePath(item.filePath, results)
	case answerRoute:
		return matchRoute(item.route, results)
	case answerSymbol:
		return matchSymbol(item, results)
	default:
		return false
	}
}

// searchResultInfo holds the fields we need from a search result for matching.
type searchResultInfo struct {
	filePath   string
	symbolName string
	contentSum string
}

// matchFilePath checks if any result's FilePath ends with the expected path suffix.
func matchFilePath(expected string, results []searchResultInfo) bool {
	// Normalize: we want to match on the significant suffix
	// e.g., "app/Http/Controllers/OrderController.php" should match
	// a result with file path ending in that suffix
	expectedLower := strings.ToLower(expected)

	for _, r := range results {
		resultPath := strings.ToLower(r.filePath)

		// Direct suffix match
		if strings.HasSuffix(resultPath, strings.ToLower(expected)) {
			return true
		}

		// Try matching just the filename if expected is a bare filename
		if !strings.Contains(expected, "/") {
			expectedBase := strings.ToLower(filepath.Base(expected))
			resultBase := strings.ToLower(filepath.Base(r.filePath))
			if expectedBase == resultBase {
				return true
			}
		}

		// Normalize by removing leading "app/" or "App/" differences
		normalizedExpected := normalizePathCase(expectedLower)
		normalizedResult := normalizePathCase(resultPath)
		if strings.HasSuffix(normalizedResult, normalizedExpected) {
			return true
		}
	}
	return false
}

// normalizePathCase lowercases and normalizes app/ vs App/ prefixes.
func normalizePathCase(p string) string {
	p = strings.ToLower(p)
	return p
}

// matchRoute checks if any result contains the route pattern.
func matchRoute(route string, results []searchResultInfo) bool {
	routeLower := strings.ToLower(route)
	for _, r := range results {
		sym := strings.ToLower(r.symbolName)
		content := strings.ToLower(r.contentSum)
		path := strings.ToLower(r.filePath)

		if strings.Contains(sym, routeLower) || strings.Contains(content, routeLower) {
			return true
		}
		// Also check if the route path is in the file path (e.g., routes files)
		if strings.Contains(path, "route") && strings.Contains(content, routeLower) {
			return true
		}
	}
	return false
}

// matchSymbol checks if any result matches the symbol/method reference.
func matchSymbol(item expectedItem, results []searchResultInfo) bool {
	for _, r := range results {
		sym := strings.ToLower(r.symbolName)
		path := strings.ToLower(r.filePath)
		content := strings.ToLower(r.contentSum)

		// Match file part (e.g., "Inventory.php")
		if item.filePath != "" {
			fileBase := strings.ToLower(filepath.Base(item.filePath))
			if strings.HasSuffix(path, fileBase) || strings.Contains(path, strings.TrimSuffix(fileBase, filepath.Ext(fileBase))) {
				return true
			}
		}

		// Match method/symbol name
		if item.method != "" {
			methodLower := strings.ToLower(item.method)
			if strings.Contains(sym, methodLower) || strings.Contains(content, methodLower) {
				return true
			}
		}
		if item.symbol != "" {
			symbolLower := strings.ToLower(item.symbol)
			if strings.Contains(sym, symbolLower) {
				return true
			}
		}
	}
	return false
}

// TestAutomatedGrading runs all 15 benchmark queries against the indexed real repo
// and scores results against the human-provided answer key.
func TestAutomatedGrading(t *testing.T) {
	env := getSharedEnv(t)

	// Parse the answer key
	answerKey := parseAnswerKey(t)

	// Ordered query IDs for deterministic output
	orderedIDs := []string{
		"A1", "A2", "A3", "A4",
		"B1", "B2", "B3", "B4", "B5", "B6",
		"C1", "C2", "C3", "C4", "C5",
	}

	// Track scores per tier
	type tierScore struct {
		hits  int
		total int
	}
	tierScores := map[string]*tierScore{
		"A": {}, "B": {}, "C": {},
	}

	for _, id := range orderedIDs {
		query, ok := benchmarkQueries[id]
		if !ok {
			t.Errorf("missing query for ID %s", id)
			continue
		}

		expected, hasAnswers := answerKey[id]
		if !hasAnswers || len(expected) == 0 {
			t.Logf("[%s] %q — no expected items in answer key, skipping scoring", id, query)
			continue
		}

		tier := queryTier(id)
		limit := queryLimit(tier)

		results, err := env.searchEng.Search(query, limit, nil)
		if err != nil {
			t.Errorf("[%s] Search(%q) error: %v", id, query, err)
			continue
		}

		// Convert to our matching format
		resultInfos := make([]searchResultInfo, len(results))
		for i, r := range results {
			resultInfos[i] = searchResultInfo{
				filePath:   r.Node.FilePath,
				symbolName: r.Node.SymbolName,
				contentSum: r.Node.ContentSum,
			}
		}

		// Score each expected item
		hits := 0
		var hitItems []string
		var missItems []string
		for _, item := range expected {
			if matchItem(item, resultInfos) {
				hits++
				hitItems = append(hitItems, fmt.Sprintf("  HIT:  %s", item.raw))
			} else {
				missItems = append(missItems, fmt.Sprintf("  MISS: %s", item.raw))
			}
		}

		total := len(expected)
		ts := tierScores[tier]
		ts.hits += hits
		ts.total += total

		// Log per-query details
		t.Logf("[%s] %q — %d/%d hits (%d results returned)", id, query, hits, total, len(results))
		for _, h := range hitItems {
			t.Logf("  %s", h)
		}
		for _, m := range missItems {
			t.Logf("  %s", m)
		}

		// Log top results for debugging
		maxShow := 5
		if len(results) < maxShow {
			maxShow = len(results)
		}
		for i := 0; i < maxShow; i++ {
			r := results[i]
			t.Logf("  result[%d]: %s (%s) in %s — score=%.4f",
				i, r.Node.SymbolName, r.Node.NodeType, r.Node.FilePath, r.Score)
		}
	}

	// Aggregate scoring
	t.Logf("\n=== TIER SUMMARY ===")
	for _, tier := range []string{"A", "B", "C"} {
		ts := tierScores[tier]
		pct := float64(0)
		if ts.total > 0 {
			pct = float64(ts.hits) / float64(ts.total) * 100
		}
		t.Logf("Tier %s: %d/%d hits (%.1f%%)", tier, ts.hits, ts.total, pct)
	}

	// B+C combined score (primary metric)
	bcHits := tierScores["B"].hits + tierScores["C"].hits
	bcTotal := tierScores["B"].total + tierScores["C"].total
	bcPct := float64(0)
	if bcTotal > 0 {
		bcPct = float64(bcHits) / float64(bcTotal) * 100
	}
	t.Logf("B+C combined: %d/%d hits (%.1f%%)", bcHits, bcTotal, bcPct)

	// Overall score
	allHits := tierScores["A"].hits + bcHits
	allTotal := tierScores["A"].total + bcTotal
	allPct := float64(0)
	if allTotal > 0 {
		allPct = float64(allHits) / float64(allTotal) * 100
	}
	t.Logf("Overall: %d/%d hits (%.1f%%)", allHits, allTotal, allPct)

	// Regression guard: fail if B+C hit rate drops below threshold.
	// Starting at 5% — raise this threshold as search quality improves.
	const bcThreshold = 5.0
	if bcPct < bcThreshold {
		t.Errorf("REGRESSION: B+C hit rate %.1f%% is below threshold %.1f%%", bcPct, bcThreshold)
	}
}

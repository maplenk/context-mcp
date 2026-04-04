package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	mcp_golang "github.com/metoro-io/mcp-golang"

	"github.com/maplenk/context-mcp/internal/graph"
	"github.com/maplenk/context-mcp/internal/search"
	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/types"
)

// Version is the package-level version string, referenced by server.go.
const Version = "0.2.0"

// ToolDeps holds dependencies needed by MCP tools
type ToolDeps struct {
	Store    *storage.Store
	Graph    *graph.GraphEngine
	Search   *search.HybridSearch
	RepoRoot string
	Profile  string
}

// isToolInProfile returns true if a tool should be registered for the MCP SDK
// based on the given profile. CLI tools are always registered regardless.
func isToolInProfile(toolName, profile string) bool {
	coreTools := map[string]bool{
		"context":         true,
		"read_symbol":     true,
		"understand":      true,
		"impact":          true,
		"detect_changes":  true,
		"trace_call_path": true,
	}
	extendedTools := map[string]bool{
		"get_architecture_summary": true,
		"get_key_symbols":          true,
		"explore":                  true,
		"search_code":              true,
	}
	switch profile {
	case "full":
		return true
	case "extended":
		return coreTools[toolName] || extendedTools[toolName]
	default: // "core"
		return coreTools[toolName]
	}
}

// ContextParams are the parameters for the context tool.
// Tags provide JSON schema metadata for the MCP SDK.
type ContextParams struct {
	Query       string   `json:"query" jsonschema:"description=Natural language or keyword query to search for relevant code"`
	Limit       int      `json:"limit,omitempty" jsonschema:"description=Maximum number of results to return (default: 5)"`
	Mode        string   `json:"mode,omitempty" jsonschema:"description=Search mode: 'search' (default) for hybrid search or 'architecture' for community detection"`
	MaxPerFile  int      `json:"max_per_file,omitempty" jsonschema:"description=Maximum results per unique file path (default: 1)"`
	ActiveFiles []string `json:"active_files,omitempty" jsonschema:"description=File paths the developer is currently editing for PPR personalization"`
}

// ImpactParams are the parameters for the impact tool.
type ImpactParams struct {
	SymbolID string `json:"symbol_id" jsonschema:"required,description=The ID or name of the symbol to analyze"`
	Depth    int    `json:"depth,omitempty" jsonschema:"description=Maximum BFS traversal depth (default: 5)"`
}

// ReadSymbolParams are the parameters for the read_symbol tool.
type ReadSymbolParams struct {
	SymbolID string `json:"symbol_id" jsonschema:"required,description=The ID or name of the symbol to read"`
}

// QueryParams are the parameters for the query tool.
type QueryParams struct {
	SQL string `json:"sql" jsonschema:"required,description=SQL SELECT query to execute"`
}

// HealthParams are the parameters for the health tool (empty — no inputs).
type HealthParams struct{}

// IndexParams are the parameters for the index tool.
type IndexParams struct {
	Path string `json:"path,omitempty" jsonschema:"description=Optional: specific path to re-index"`
}

// TraceCallPathParams are the parameters for the trace_call_path tool
type TraceCallPathParams struct {
	From     string `json:"from" jsonschema:"required,description=Source symbol name or ID"`
	To       string `json:"to" jsonschema:"required,description=Target symbol name or ID"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"description=Maximum path depth to search (default: 10)"`
}

// GetKeySymbolsParams are the parameters for the get_key_symbols tool
type GetKeySymbolsParams struct {
	Limit      int    `json:"limit,omitempty" jsonschema:"description=Maximum number of symbols to return (default: 20)"`
	FileFilter string `json:"file_filter,omitempty" jsonschema:"description=Optional file path prefix to scope results to a specific directory"`
}

// SearchCodeParams are the parameters for the search_code tool
type SearchCodeParams struct {
	Pattern    string `json:"pattern" jsonschema:"required,description=Regex pattern to search for in file contents"`
	FileFilter string `json:"file_filter,omitempty" jsonschema:"description=Optional glob pattern to filter files"`
	Limit      int    `json:"limit,omitempty" jsonschema:"description=Maximum number of matching lines to return (default: 20)"`
}

// DetectChangesParams are the parameters for the detect_changes tool
type DetectChangesParams struct {
	Since string `json:"since" jsonschema:"required,description=Git ref to compare against (e.g. HEAD~5 or main or a commit hash)"`
	Path  string `json:"path,omitempty" jsonschema:"description=Optional path filter for changed files"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=Maximum number of ranked changes to return (default: 5)"`
}

// ArchitectureSummaryParams are the parameters for the get_architecture_summary tool
type ArchitectureSummaryParams struct {
	Limit int `json:"limit,omitempty" jsonschema:"description=Maximum number of structurally important nodes to return (default: 5)"`
}

// ExploreParams are the parameters for the explore tool
type ExploreParams struct {
	Symbol      string `json:"symbol" jsonschema:"required,description=Symbol name to search for"`
	IncludeDeps bool   `json:"include_deps,omitempty" jsonschema:"description=Whether to include dependency/dependent analysis (default: false)"`
	Depth       int    `json:"depth,omitempty" jsonschema:"description=Depth for dependency traversal (default: 2)"`
}

// UnderstandParams are the parameters for the understand tool
type UnderstandParams struct {
	Symbol string `json:"symbol" jsonschema:"required,description=Symbol name to understand"`
}

// IndexFunc is the callback for triggering a re-index
type IndexFunc func(path string) error

// RegisterTools registers all 13 tools for CLI mode and profile-gated tools for MCP SDK mode.
// CLI tools: all 13 available via GetHandler/GetTools (always).
// SDK tools (MCP protocol): gated by deps.Profile — "core" (6), "extended" (10), or "full" (13).
func RegisterTools(s *Server, deps ToolDeps, indexFn IndexFunc) {
	registerContextTool(s, deps)
	registerImpactTool(s, deps)
	registerReadSymbolTool(s, deps)
	registerQueryTool(s, deps)
	registerHealthTool(s, deps)
	registerIndexTool(s, deps, indexFn)
	registerTraceCallPathTool(s, deps)
	registerGetKeySymbolsTool(s, deps)
	registerSearchCodeTool(s, deps)
	registerDetectChangesTool(s, deps)
	registerArchitectureSummaryTool(s, deps)
	registerExploreTool(s, deps)
	registerUnderstandTool(s, deps)
	// M9: Register MCP resources and prompts
	registerResources(s, deps)
	registerPrompts(s, deps)
}

// ----- Tool 1: context -----

func contextHandler(deps ToolDeps, p ContextParams) (interface{}, error) {
	if p.Query == "" && p.Mode != "architecture" {
		return nil, fmt.Errorf("query is required (use mode='architecture' for community detection)")
	}
	if p.Limit == 0 {
		p.Limit = 5
	}
	if p.Limit > 100 {
		p.Limit = 100
	}
	// Architecture mode: return community detection results (backward compat, undocumented)
	if p.Mode == "architecture" {
		if deps.Graph == nil {
			return nil, fmt.Errorf("graph engine not initialized")
		}
		communities, modularity := deps.Graph.DetectCommunities()
		response := map[string]interface{}{
			"mode":        "architecture",
			"communities": communities,
			"modularity":  modularity,
			"count":       len(communities),
		}
		summaries, _ := deps.Store.GetAllProjectSummaries()
		if len(summaries) > 0 {
			var adrTexts []string
			for _, s := range summaries {
				adrTexts = append(adrTexts, fmt.Sprintf("[%s] %s", s.Project, s.Summary))
			}
			response["architecture_context"] = strings.Join(adrTexts, "\n\n")
		}
		return response, nil
	}
	if deps.Search == nil {
		return nil, fmt.Errorf("search engine not initialized")
	}
	// Resolve active files to node IDs for PPR personalization
	var activeFileNodeIDs []string
	for _, filePath := range p.ActiveFiles {
		nodeIDs, err := deps.Store.GetNodeIDsByFile(filePath)
		if err == nil {
			activeFileNodeIDs = append(activeFileNodeIDs, nodeIDs...)
		}
	}
	results, err := deps.Search.Search(p.Query, p.Limit, activeFileNodeIDs, p.MaxPerFile)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Collect file intents for why_now
	fileIntents := make(map[string]string)
	if len(results) > 0 {
		filePaths := make([]string, 0)
		seen := make(map[string]bool)
		for _, r := range results {
			if !seen[r.Node.FilePath] {
				filePaths = append(filePaths, r.Node.FilePath)
				seen[r.Node.FilePath] = true
			}
		}
		intents, intentErr := deps.Store.GetFileIntentsByPaths(filePaths)
		if intentErr == nil {
			for path, fi := range intents {
				fileIntents[path] = fi.IntentText
			}
		}
	}

	// Convert to Inspectables
	inspectables := make([]types.Inspectable, 0, len(results))
	for i, r := range results {
		targetType := r.Node.NodeType.String()

		// Determine next tool based on node type
		nextTool := "read_symbol"
		if r.Node.NodeType == types.NodeTypeStruct || r.Node.NodeType == types.NodeTypeClass || r.Node.NodeType == types.NodeTypeInterface {
			nextTool = "understand"
		}
		if r.Node.NodeType == types.NodeTypeFile {
			nextTool = "explore"
		}

		// Build next_args using ID (durable hash) with name as fallback
		symbolRef := r.Node.ID
		if symbolRef == "" {
			symbolRef = r.Node.SymbolName
		}
		// Use correct key for each tool: explore uses "symbol", others use "symbol_id"
		var nextArgs map[string]string
		if nextTool == "explore" {
			nextArgs = map[string]string{"symbol": symbolRef}
		} else {
			nextArgs = map[string]string{"symbol_id": symbolRef}
		}

		item := types.Inspectable{
			Rank:       i + 1,
			TargetType: targetType,
			Name:       r.Node.SymbolName,
			FilePath:   r.Node.FilePath,
			ID:         r.Node.ID,
			Score:      r.Score,
			Reason:     generateReasonFromBreakdown(r.Breakdown),
			NextTool:   nextTool,
			NextArgs:   nextArgs,
		}

		// Attach why_now from file intents
		if intent, ok := fileIntents[r.Node.FilePath]; ok {
			item.WhyNow = intent
		}

		inspectables = append(inspectables, item)
	}

	summary := fmt.Sprintf("%d results for %q", len(inspectables), p.Query)

	return types.InspectableResponse{
		Inspectables: inspectables,
		Total:        len(inspectables),
		Query:        p.Query,
		Summary:      summary,
	}, nil
}

// generateReasonFromBreakdown produces a human-readable explanation from a *ScoreBreakdown.
// Returns a generic reason if the breakdown is nil.
func generateReasonFromBreakdown(b *types.ScoreBreakdown) string {
	if b == nil {
		return "Composite match"
	}
	return generateReason(*b)
}

// generateReason produces a human-readable explanation from a ScoreBreakdown.
func generateReason(b types.ScoreBreakdown) string {
	type signal struct {
		name  string
		value float64
	}
	signals := []signal{
		{"PageRank", b.PPR},
		{"Lexical match", b.BM25},
		{"Betweenness (bottleneck)", b.Betweenness},
		{"In-degree (popular)", b.InDegree},
		{"Semantic similarity", b.Semantic},
	}
	sort.Slice(signals, func(i, j int) bool {
		return signals[i].value > signals[j].value
	})
	// Pick top 2 non-zero signals
	var parts []string
	for _, s := range signals {
		if s.value > 0.01 && len(parts) < 2 {
			parts = append(parts, s.name)
		}
	}
	if len(parts) == 0 {
		return "Composite match"
	}
	return strings.Join(parts, " + ")
}

func registerContextTool(s *Server, deps ToolDeps) {
	desc := "Ranked code discovery. Returns the top 5 symbols most relevant to your query with scores, reasons, and next-step tool recommendations. Start here for any code search or exploration."

	// CLI handler (json.RawMessage)
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ContextParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return contextHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "context",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Natural language or keyword query to search for relevant code",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of results to return (default: 5)",
					"default":     5,
				},
				"mode": map[string]interface{}{
					"type":        "string",
					"description": "Search mode: 'search' (default) for hybrid search, 'architecture' for community detection",
					"default":     "search",
				},
				"max_per_file": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum results per unique file path (default: 3)",
					"default":     3,
				},
				"active_files": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "string",
					},
					"description": "File paths the developer is currently editing for PPR personalization",
				},
			},
		},
	}, cliHandler)

	// SDK handler (typed struct) — gated by profile
	if isToolInProfile("context", deps.Profile) {
		if err := s.RegisterSDKTool("context", desc, func(p ContextParams) (*mcp_golang.ToolResponse, error) {
			result, err := contextHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'context': %v", err)
		}
	}
}

// ----- Tool 2: impact -----

func impactHandler(deps ToolDeps, p ImpactParams) (interface{}, error) {
	if p.Depth == 0 {
		p.Depth = 5
	}
	if p.Depth > 20 {
		p.Depth = 20
	}
	if deps.Graph == nil {
		return nil, fmt.Errorf("graph engine not initialized")
	}

	// Try to resolve symbol name to ID
	nodeID := p.SymbolID
	if node, err := deps.Store.GetNodeByName(p.SymbolID); err == nil {
		nodeID = node.ID
	}

	// Get affected nodes with their hop depths
	affectedWithDepth := deps.Graph.BlastRadiusWithDepth(nodeID, p.Depth)
	if affectedWithDepth == nil {
		return nil, fmt.Errorf("symbol not found in graph: %s", p.SymbolID)
	}

	// Get betweenness score for the target node
	var riskScore float64
	if score, err := deps.Store.GetNodeScore(nodeID); err == nil {
		riskScore = score.Betweenness
	}

	// impactNode is a minimal node descriptor for the response
	type impactNode struct {
		ID         string            `json:"id"`
		SymbolName string            `json:"symbol_name"`
		FilePath   string            `json:"file_path"`
		NextTool   string            `json:"next_tool"`
		NextArgs   map[string]string `json:"next_args,omitempty"`
	}

	// Group affected nodes by risk level based on hop depth
	var direct []impactNode    // depth 1 = CRITICAL
	var highRisk []impactNode  // depth 2 = HIGH
	var mediumRisk []impactNode // depth 3 = MEDIUM
	var lowRisk []impactNode   // depth 4+ = LOW
	var affectedTests []impactNode

	for id, depth := range affectedWithDepth {
		node, err := deps.Store.GetNode(id)
		if err != nil {
			continue
		}

		// Identify test nodes
		testNode := impactNode{
			ID:         node.ID,
			SymbolName: node.SymbolName,
			FilePath:   node.FilePath,
			NextTool:   "read_symbol",
			NextArgs:   map[string]string{"symbol_id": node.ID},
		}
		if strings.Contains(node.SymbolName, "test") || strings.Contains(node.SymbolName, "Test") {
			affectedTests = append(affectedTests, testNode)
		}

		// Group by risk level
		switch depth {
		case 0, 1:
			// Direct dependents get trace_call_path as next tool
			n := impactNode{
				ID:         node.ID,
				SymbolName: node.SymbolName,
				FilePath:   node.FilePath,
				NextTool:   "trace_call_path",
				NextArgs:   map[string]string{"from": p.SymbolID, "to": node.SymbolName},
			}
			direct = append(direct, n)
		case 2:
			n := impactNode{
				ID:         node.ID,
				SymbolName: node.SymbolName,
				FilePath:   node.FilePath,
				NextTool:   "read_symbol",
				NextArgs:   map[string]string{"symbol_id": node.ID},
			}
			highRisk = append(highRisk, n)
		case 3:
			n := impactNode{
				ID:         node.ID,
				SymbolName: node.SymbolName,
				FilePath:   node.FilePath,
				NextTool:   "read_symbol",
				NextArgs:   map[string]string{"symbol_id": node.ID},
			}
			mediumRisk = append(mediumRisk, n)
		default:
			n := impactNode{
				ID:         node.ID,
				SymbolName: node.SymbolName,
				FilePath:   node.FilePath,
				NextTool:   "read_symbol",
				NextArgs:   map[string]string{"symbol_id": node.ID},
			}
			lowRisk = append(lowRisk, n)
		}
	}

	// Record original counts before capping
	totalDirect := len(direct)
	totalHighRisk := len(highRisk)
	totalMediumRisk := len(mediumRisk)
	totalLowRisk := len(lowRisk)

	// Cap each risk tier at 5 entries
	if len(direct) > 5 {
		direct = direct[:5]
	}
	if len(highRisk) > 5 {
		highRisk = highRisk[:5]
	}
	if len(mediumRisk) > 5 {
		mediumRisk = mediumRisk[:5]
	}
	if len(lowRisk) > 5 {
		lowRisk = lowRisk[:5]
	}

	totalAffected := len(affectedWithDepth)
	summary := fmt.Sprintf(
		"Symbol has betweenness %.2f — %d direct (showing %d), %d high-risk (showing %d), %d medium (showing %d), %d low (showing %d), %d tests impacted",
		riskScore, totalDirect, len(direct), totalHighRisk, len(highRisk), totalMediumRisk, len(mediumRisk), totalLowRisk, len(lowRisk), len(affectedTests),
	)

	return map[string]interface{}{
		"symbol":         p.SymbolID,
		"depth":          p.Depth,
		"risk_score":     riskScore,
		"affected_count": totalAffected,
		"direct":         direct,
		"high_risk":      highRisk,
		"medium_risk":    mediumRisk,
		"low_risk":       lowRisk,
		"affected_tests": affectedTests,
		"summary":        summary,
	}, nil
}

func registerImpactTool(s *Server, deps ToolDeps) {
	desc := "Blast radius: finds all downstream dependents of a symbol, grouped by risk level (CRITICAL/HIGH/MEDIUM/LOW). Use before making changes to assess risk."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ImpactParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return impactHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "impact",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"symbol_id": map[string]interface{}{
					"type":        "string",
					"description": "The ID or name of the symbol to analyze",
				},
				"depth": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum BFS traversal depth (default: 5)",
					"default":     5,
				},
			},
			"required": []string{"symbol_id"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("impact", deps.Profile) {
		if err := s.RegisterSDKTool("impact", desc, func(p ImpactParams) (*mcp_golang.ToolResponse, error) {
			result, err := impactHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'impact': %v", err)
		}
	}
}

// ----- Tool 3: read_symbol -----

func readSymbolHandler(deps ToolDeps, p ReadSymbolParams) (interface{}, error) {
	// Try by name first, then by ID
	node, err := deps.Store.GetNodeByName(p.SymbolID)
	if err != nil {
		node, err = deps.Store.GetNode(p.SymbolID)
		if err != nil {
			return nil, fmt.Errorf("symbol not found: %s", p.SymbolID)
		}
	}

	// Read the exact byte range from the file
	absPath := filepath.Join(deps.RepoRoot, node.FilePath)
	// Prevent path traversal
	absPath, err = filepath.Abs(absPath)
	if err != nil {
		return nil, fmt.Errorf("resolving path: %w", err)
	}
	// Resolve symlinks to prevent symlink-based path traversal bypass
	absPath, err = filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, fmt.Errorf("resolving symlinks in path: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(deps.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving repo root symlinks: %w", err)
	}
	absPath = filepath.Clean(absPath)
	if absPath != resolvedRoot && !strings.HasPrefix(absPath, resolvedRoot+string(filepath.Separator)) {
		return nil, fmt.Errorf("path traversal detected: %s is outside repo root", node.FilePath)
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("opening file %s: %w", node.FilePath, err)
	}
	defer f.Close()

	// Check for stale byte offsets (file may have changed since indexing)
	fi, statErr := f.Stat()
	if statErr != nil {
		return nil, fmt.Errorf("stat file %s: %w", node.FilePath, statErr)
	}
	if int64(node.EndByte) > fi.Size() {
		return map[string]interface{}{
			"symbol_name": node.SymbolName,
			"file_path":   node.FilePath,
			"error":       "file modified since indexing — byte offsets are stale",
			"stale":       true,
		}, nil
	}

	if node.EndByte <= node.StartByte {
		return nil, fmt.Errorf("invalid byte range for %s: start=%d end=%d", node.SymbolName, node.StartByte, node.EndByte)
	}
	length := node.EndByte - node.StartByte
	if length > 5*1024*1024 { // 5MB safety cap
		return nil, fmt.Errorf("symbol too large: %d bytes", length)
	}
	buf := make([]byte, length)
	_, err = f.ReadAt(buf, int64(node.StartByte))
	if err != nil {
		return nil, fmt.Errorf("reading bytes: %w", err)
	}

	response := map[string]interface{}{
		"symbol_name": node.SymbolName,
		"file_path":   node.FilePath,
		"node_type":   node.NodeType.String(),
		"start_byte":  node.StartByte,
		"end_byte":    node.EndByte,
		"source":      string(buf),
	}

	// Heuristic staleness check: verify extracted content contains the symbol's base name.
	// If the file changed since indexing, the byte offsets may be stale.
	baseName := symbolBaseName(node.SymbolName)
	if baseName != "" && !strings.Contains(string(buf), baseName) {
		response["stale"] = true
		response["warning"] = "file may have changed since indexing"
	}

	// Add chaining hints
	symbolRef := node.ID
	if symbolRef == "" {
		symbolRef = node.SymbolName
	}
	symbolRefJSON, _ := json.Marshal(symbolRef)
	response["next_tools"] = []map[string]string{
		{"tool": "understand", "args_hint": `{"symbol": ` + string(symbolRefJSON) + `}`},
		{"tool": "impact", "args_hint": `{"symbol_id": ` + string(symbolRefJSON) + `}`},
	}

	return response, nil
}

// symbolBaseName extracts the last component of a dotted symbol name.
// e.g. "pkg.MyStruct.Method" -> "Method", "processOrder" -> "processOrder"
func symbolBaseName(symbolName string) string {
	if idx := strings.LastIndex(symbolName, "."); idx >= 0 {
		return symbolName[idx+1:]
	}
	return symbolName
}

func registerReadSymbolTool(s *Server, deps ToolDeps) {
	desc := "Read exact source code of a symbol by name or ID. Returns the precise byte range from disk. Use after context or explore to inspect a specific result."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ReadSymbolParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return readSymbolHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "read_symbol",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"symbol_id": map[string]interface{}{
					"type":        "string",
					"description": "The ID or name of the symbol to read",
				},
			},
			"required": []string{"symbol_id"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("read_symbol", deps.Profile) {
		if err := s.RegisterSDKTool("read_symbol", desc, func(p ReadSymbolParams) (*mcp_golang.ToolResponse, error) {
			result, err := readSymbolHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'read_symbol': %v", err)
		}
	}
}

// ----- Tool 4: query -----

func queryHandler(deps ToolDeps, p QueryParams) (interface{}, error) {
	results, err := deps.Store.RawQuery(p.SQL)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	return results, nil
}

func registerQueryTool(s *Server, deps ToolDeps) {
	desc := "Execute a read-only SQL query against the code graph database. Advanced: for custom analysis not covered by other tools."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p QueryParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return queryHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "query",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"sql": map[string]interface{}{
					"type":        "string",
					"description": "SQL SELECT query to execute",
				},
			},
			"required": []string{"sql"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("query", deps.Profile) {
		if err := s.RegisterSDKTool("query", desc, func(p QueryParams) (*mcp_golang.ToolResponse, error) {
			result, err := queryHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'query': %v", err)
		}
	}
}

// ----- Tool 5: health -----

func healthHandler(deps ToolDeps) (interface{}, error) {
	var nodeCount, edgeCount int
	if deps.Graph != nil {
		nodeCount = deps.Graph.NodeCount()
		edgeCount = deps.Graph.EdgeCount()
	}
	return map[string]interface{}{
		"status":  "healthy",
		"nodes":   nodeCount,
		"edges":   edgeCount,
		"version": "0.2.0",
	}, nil
}

func registerHealthTool(s *Server, deps ToolDeps) {
	desc := "System health: node count, edge count, index status, version."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		return healthHandler(deps)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "health",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("health", deps.Profile) {
		if err := s.RegisterSDKTool("health", desc, func(p HealthParams) (*mcp_golang.ToolResponse, error) {
			result, err := healthHandler(deps)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'health': %v", err)
		}
	}
}

// ----- Tool 6: index -----

func indexHandler(indexFn IndexFunc, p IndexParams, repoRoot string) (interface{}, error) {
	if indexFn == nil {
		return nil, fmt.Errorf("index function not configured")
	}

	if p.Path != "" {
		// M7: Resolve relative paths against repoRoot, not CWD
		pathToResolve := p.Path
		if !filepath.IsAbs(pathToResolve) {
			pathToResolve = filepath.Join(repoRoot, pathToResolve)
		}
		absPath, err := filepath.Abs(pathToResolve)
		if err != nil {
			return nil, fmt.Errorf("invalid path: %w", err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(repoRoot)
		if err != nil {
			return nil, fmt.Errorf("resolving repo root symlinks: %w", err)
		}
		// Resolve symlinks on the path if it exists; otherwise use Abs path
		resolvedPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			// Path doesn't exist yet — resolve parent to normalize symlinks
			resolvedParent, perr := filepath.EvalSymlinks(filepath.Dir(absPath))
			if perr != nil {
				resolvedPath = absPath
			} else {
				resolvedPath = filepath.Join(resolvedParent, filepath.Base(absPath))
			}
		}
		if !strings.HasPrefix(resolvedPath, resolvedRoot+string(filepath.Separator)) && resolvedPath != resolvedRoot {
			return nil, fmt.Errorf("path traversal detected: %s is outside repo root", p.Path)
		}
	}

	if err := indexFn(p.Path); err != nil {
		return nil, fmt.Errorf("indexing failed: %w", err)
	}
	return "Indexing completed successfully", nil
}

func registerIndexTool(s *Server, deps ToolDeps, indexFn IndexFunc) {
	desc := "Trigger re-indexing of the codebase or a specific file path. Use after bulk file changes."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p IndexParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return indexHandler(indexFn, p, deps.RepoRoot)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "index",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Optional: specific path to re-index",
				},
			},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("index", deps.Profile) {
		if err := s.RegisterSDKTool("index", desc, func(p IndexParams) (*mcp_golang.ToolResponse, error) {
			result, err := indexHandler(indexFn, p, deps.RepoRoot)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'index': %v", err)
		}
	}
}

// ----- Tool 6: trace_call_path -----

func traceCallPathHandler(deps ToolDeps, p TraceCallPathParams) (interface{}, error) {
	if p.From == "" || p.To == "" {
		return nil, fmt.Errorf("both 'from' and 'to' are required")
	}
	if p.MaxDepth == 0 {
		p.MaxDepth = 10
	}
	if p.MaxDepth > 20 {
		p.MaxDepth = 20
	}
	if deps.Graph == nil {
		return nil, fmt.Errorf("graph engine not initialized")
	}

	// Resolve symbol names to IDs
	fromHash := p.From
	if node, err := deps.Store.GetNodeByName(p.From); err == nil {
		fromHash = node.ID
	}
	toHash := p.To
	if node, err := deps.Store.GetNodeByName(p.To); err == nil {
		toHash = node.ID
	}

	paths := deps.Graph.TraceCallPath(fromHash, toHash, p.MaxDepth)

	// pathNode represents a node in the resolved call path with chaining hints
	type pathNode struct {
		SymbolName     string            `json:"symbol_name"`
		FilePath       string            `json:"file_path,omitempty"`
		ReadSymbolArgs map[string]string `json:"read_symbol_args,omitempty"`
	}

	// Resolve hash IDs to symbol names for readability
	var resolvedPaths [][]pathNode
	var edgeTypes [][]string
	for _, path := range paths {
		var resolved []pathNode
		var edges []string
		for i, hashID := range path {
			if node, err := deps.Store.GetNode(hashID); err == nil {
				resolved = append(resolved, pathNode{
					SymbolName:     node.SymbolName,
					FilePath:       node.FilePath,
					ReadSymbolArgs: map[string]string{"symbol_id": node.ID},
				})
			} else {
				resolved = append(resolved, pathNode{SymbolName: hashID})
			}
			if i > 0 {
				// Try to determine edge type
				edgesFrom, err := deps.Store.GetEdgesFrom(path[i-1])
				edgeType := "calls"
				if err == nil {
					for _, e := range edgesFrom {
						if e.TargetID == hashID {
							edgeType = e.EdgeType.String()
							break
						}
					}
				}
				edges = append(edges, edgeType)
			}
		}
		resolvedPaths = append(resolvedPaths, resolved)
		edgeTypes = append(edgeTypes, edges)
	}

	return map[string]interface{}{
		"from":       p.From,
		"to":         p.To,
		"paths":      resolvedPaths,
		"edge_types": edgeTypes,
		"count":      len(resolvedPaths),
	}, nil
}

func registerTraceCallPathTool(s *Server, deps ToolDeps) {
	desc := "Finds the call chain between two symbols using bidirectional graph traversal. Use to understand how control flows from A to B."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p TraceCallPathParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return traceCallPathHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "trace_call_path",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"from": map[string]interface{}{
					"type":        "string",
					"description": "Source symbol name or ID",
				},
				"to": map[string]interface{}{
					"type":        "string",
					"description": "Target symbol name or ID",
				},
				"max_depth": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum path depth to search (default: 10)",
					"default":     10,
				},
			},
			"required": []string{"from", "to"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("trace_call_path", deps.Profile) {
		if err := s.RegisterSDKTool("trace_call_path", desc, func(p TraceCallPathParams) (*mcp_golang.ToolResponse, error) {
			result, err := traceCallPathHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'trace_call_path': %v", err)
		}
	}
}

// ----- Tool 7: get_key_symbols -----

func getKeySymbolsHandler(deps ToolDeps, p GetKeySymbolsParams) (interface{}, error) {
	if p.Limit == 0 {
		p.Limit = 5
	}
	if p.Limit > 50 {
		p.Limit = 50
	}
	if deps.Graph == nil {
		return nil, fmt.Errorf("graph engine not initialized")
	}

	// Use stored PageRank scores instead of recomputing O(V * iterations)
	pageranks := make(map[string]float64)
	allScores, err := deps.Store.GetAllNodeScores()
	if err == nil && len(allScores) > 0 {
		for _, s := range allScores {
			pageranks[s.NodeID] = s.PageRank
		}
	}
	// Fallback to computing if no stored scores
	if len(pageranks) == 0 {
		pageranks = deps.Graph.PageRank()
	}
	if len(pageranks) == 0 {
		return types.InspectableResponse{
			Inspectables: []types.Inspectable{},
			Total:        0,
			Summary:      "No symbols found with PageRank scores",
		}, nil
	}

	type symbolInfo struct {
		Name      string  `json:"name"`
		FilePath  string  `json:"file_path"`
		NodeType  string  `json:"node_type"`
		PageRank  float64 `json:"pagerank"`
		InDegree  int     `json:"in_degree"`
		OutDegree int     `json:"out_degree"`
		ID        string  `json:"id"`
	}

	var symbols []symbolInfo
	for hashID, pr := range pageranks {
		node, err := deps.Store.GetNode(hashID)
		if err != nil {
			continue
		}

		// Apply file filter if specified (prefix match)
		if p.FileFilter != "" && !strings.HasPrefix(node.FilePath, p.FileFilter) {
			continue
		}

		symbols = append(symbols, symbolInfo{
			Name:      node.SymbolName,
			FilePath:  node.FilePath,
			NodeType:  node.NodeType.String(),
			PageRank:  pr,
			InDegree:  deps.Graph.GetInDegree(hashID),
			OutDegree: deps.Graph.GetOutDegree(hashID),
			ID:        hashID,
		})
	}

	// Sort by PageRank descending
	sort.Slice(symbols, func(i, j int) bool {
		return symbols[i].PageRank > symbols[j].PageRank
	})

	totalBeforeCap := len(symbols)
	if len(symbols) > p.Limit {
		symbols = symbols[:p.Limit]
	}

	var inspectables []types.Inspectable
	for i, sym := range symbols {
		reason := fmt.Sprintf("PageRank %.4f, %d callers, %d callees", sym.PageRank, sym.InDegree, sym.OutDegree)

		nextTool := "read_symbol"
		if sym.NodeType == "struct" || sym.NodeType == "class" || sym.NodeType == "interface" {
			nextTool = "understand"
		}

		inspectables = append(inspectables, types.Inspectable{
			Rank:       i + 1,
			TargetType: "symbol",
			Name:       sym.Name,
			FilePath:   sym.FilePath,
			ID:         sym.ID,
			Score:      sym.PageRank,
			Reason:     reason,
			NextTool:   nextTool,
			NextArgs:   map[string]string{"symbol_id": sym.ID},
		})
	}

	return types.InspectableResponse{
		Inspectables: inspectables,
		Total:        totalBeforeCap,
		Summary:      fmt.Sprintf("Top %d symbols by PageRank out of %d total", len(inspectables), totalBeforeCap),
	}, nil
}

func registerGetKeySymbolsTool(s *Server, deps ToolDeps) {
	desc := "Top symbols ranked by graph centrality (PageRank, betweenness). Use to find the most structurally important code in a directory or the whole codebase."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p GetKeySymbolsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return getKeySymbolsHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "get_key_symbols",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of symbols to return (default: 5)",
					"default":     5,
				},
				"file_filter": map[string]interface{}{
					"type":        "string",
					"description": "Optional file path prefix to scope results to a specific directory",
				},
			},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("get_key_symbols", deps.Profile) {
		if err := s.RegisterSDKTool("get_key_symbols", desc, func(p GetKeySymbolsParams) (*mcp_golang.ToolResponse, error) {
			result, err := getKeySymbolsHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'get_key_symbols': %v", err)
		}
	}
}

// ----- Tool 8: search_code -----

func searchCodeHandler(deps ToolDeps, p SearchCodeParams) (interface{}, error) {
	if p.Pattern == "" {
		return nil, fmt.Errorf("'pattern' is required")
	}
	if len(p.Pattern) > 10000 {
		return nil, fmt.Errorf("pattern too long: %d characters (max 10000)", len(p.Pattern))
	}
	if p.Limit == 0 {
		p.Limit = 20
	}
	if p.Limit > 200 {
		p.Limit = 200
	}

	// Compile regex
	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	// Get all unique file paths from the database
	filePaths, err := deps.Store.GetAllFilePaths()
	if err != nil {
		return nil, fmt.Errorf("getting file paths: %w", err)
	}

	// Resolve repo root for path traversal protection
	resolvedRoot, err := filepath.EvalSymlinks(deps.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving repo root symlinks: %w", err)
	}

	type codeMatch struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Content string `json:"content"`
	}

	var matches []codeMatch
	var scanWarnings []string
	filesScanned := 0
	filesWithErrors := 0
	for _, relPath := range filePaths {
		// Apply file filter if specified (try full path first, fallback to basename)
		if p.FileFilter != "" {
			matched, err := filepath.Match(p.FileFilter, relPath)
			if err != nil || !matched {
				// Fallback: try matching just the basename for simple patterns like "*.go"
				matched, err = filepath.Match(p.FileFilter, filepath.Base(relPath))
				if err != nil || !matched {
					continue
				}
			}
		}

		absPath := filepath.Join(deps.RepoRoot, relPath)
		absPath, err := filepath.Abs(absPath)
		if err != nil {
			continue
		}
		// Resolve symlinks for path traversal protection
		absPath, err = filepath.EvalSymlinks(absPath)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(absPath, resolvedRoot+string(filepath.Separator)) && absPath != resolvedRoot {
			continue // path traversal attempt
		}

		filesScanned++

		// M10: Per-file regex execution timeout to prevent hangs on
		// pathological inputs (even though Go's RE2 is linear, large
		// files can still be slow).
		limitReached := func() bool {
			f, err := os.Open(absPath)
			if err != nil {
				filesWithErrors++
				warning := fmt.Sprintf("open error on %s: %v", relPath, err)
				log.Printf("search_code: %s", warning)
				scanWarnings = append(scanWarnings, warning)
				return false
			}
			defer f.Close()

			deadline := time.Now().Add(5 * time.Second)
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for long lines
			lineNum := 0
			for scanner.Scan() {
				lineNum++
				if time.Now().After(deadline) {
					log.Printf("search_code: regex execution timeout on %s after 5s, skipping", relPath)
					return false // skip this file, continue with others
				}
				line := scanner.Text()
				if re.MatchString(line) {
					matches = append(matches, codeMatch{
						File:    relPath,
						Line:    lineNum,
						Content: strings.TrimSpace(line),
					})
					if len(matches) >= p.Limit {
						return true
					}
				}
			}
			// Check for scanner errors (oversized tokens, I/O failures)
			if err := scanner.Err(); err != nil {
				filesWithErrors++
				warning := fmt.Sprintf("scanner error on %s: %v (results may be incomplete)", relPath, err)
				log.Printf("search_code: %s", warning)
				scanWarnings = append(scanWarnings, warning)
			}
			return false
		}()
		if limitReached {
			break
		}
	}

	// M1: If ALL scanned files had errors, return an error so isError is set in MCP response
	if filesScanned > 0 && filesWithErrors == filesScanned {
		return nil, fmt.Errorf("partial results due to I/O errors: all %d files failed to scan: %s",
			filesScanned, strings.Join(scanWarnings, "; "))
	}

	result := map[string]interface{}{
		"matches": matches,
		"count":   len(matches),
		"pattern": p.Pattern,
	}
	if len(scanWarnings) > 0 {
		result["warnings"] = scanWarnings
		result["truncated"] = true
		result["error"] = "partial results due to I/O errors"
	}
	return result, nil
}

func registerSearchCodeTool(s *Server, deps ToolDeps) {
	desc := "Literal regex search across source files. Fallback for exact text/pattern matching when context does not find what you need."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p SearchCodeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return searchCodeHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "search_code",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Regex pattern to search for in file contents",
				},
				"file_filter": map[string]interface{}{
					"type":        "string",
					"description": "Optional glob pattern to filter files by name (e.g., '*.go', '*.js'). Matched against both full path and basename.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of matching lines to return (default: 20)",
					"default":     20,
				},
			},
			"required": []string{"pattern"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("search_code", deps.Profile) {
		if err := s.RegisterSDKTool("search_code", desc, func(p SearchCodeParams) (*mcp_golang.ToolResponse, error) {
			result, err := searchCodeHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'search_code': %v", err)
		}
	}
}

// ----- Tool 9: detect_changes -----

func detectChangesHandler(deps ToolDeps, p DetectChangesParams) (interface{}, error) {
	if p.Since == "" {
		return nil, fmt.Errorf("'since' is required")
	}
	if p.Limit == 0 {
		p.Limit = 5
	}

	// Validate the git ref to prevent command injection
	// M25: Allow alphanumeric, ~, ^, ., -, / for branch names (e.g. feature/my-branch, origin/main)
	// Path traversal (..) and reflog syntax (@{}) are rejected separately below
	validRef := regexp.MustCompile(`^[a-zA-Z0-9~^.\-/]+$`)
	if !validRef.MatchString(p.Since) {
		return nil, fmt.Errorf("invalid git ref: %s", p.Since)
	}
	if strings.HasPrefix(p.Since, "-") {
		return nil, fmt.Errorf("invalid git ref (must not start with dash): %s", p.Since)
	}
	// Prevent path traversal and git network access triggers
	if strings.Contains(p.Since, "..") {
		return nil, fmt.Errorf("invalid git ref (path traversal detected): %s", p.Since)
	}
	if strings.Contains(p.Since, "@{") {
		return nil, fmt.Errorf("invalid git ref (reflog syntax not allowed): %s", p.Since)
	}

	// Run git diff to get changed files
	args := []string{"diff", "--name-only", p.Since}
	if p.Path != "" {
		// Validate path filter
		if strings.Contains(p.Path, "..") {
			return nil, fmt.Errorf("path traversal detected in path filter")
		}
		// M14: Reject absolute paths to prevent accessing files outside the repo
		if filepath.IsAbs(p.Path) {
			return nil, fmt.Errorf("absolute paths not allowed in path filter: %s", p.Path)
		}
		args = append(args, "--", p.Path)
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = deps.RepoRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff failed: %w", err)
	}

	changedFiles := strings.Split(strings.TrimSpace(string(output)), "\n")
	// Filter empty strings
	var validFiles []string
	for _, f := range changedFiles {
		f = strings.TrimSpace(f)
		if f != "" {
			validFiles = append(validFiles, f)
		}
	}
	changedFiles = validFiles

	// For each changed file, categorize: deleted, new, or modified
	type symbolChange struct {
		Name     string
		FilePath string
		ID       string
		Status   string // "file_modified", "deleted", "new_file"
	}

	var allCandidates []symbolChange
	var newFileCount, deletedCount, modifiedCount int

	for _, filePath := range changedFiles {
		// Get stored symbols for this file
		storedNodes, err := deps.Store.GetNodesByFile(filePath)
		if err != nil {
			continue
		}

		// Check which stored symbols still exist (file might be deleted)
		absPath := filepath.Join(deps.RepoRoot, filePath)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			// File deleted — all its symbols are deleted
			for _, n := range storedNodes {
				allCandidates = append(allCandidates, symbolChange{
					Name:     n.SymbolName,
					FilePath: n.FilePath,
					ID:       n.ID,
					Status:   "deleted",
				})
				deletedCount++
			}
			continue
		}

		// File exists but changed — symbols are potentially modified
		for _, n := range storedNodes {
			allCandidates = append(allCandidates, symbolChange{
				Name:     n.SymbolName,
				FilePath: n.FilePath,
				ID:       n.ID,
				Status:   "file_modified",
			})
			modifiedCount++
		}

		// If no stored nodes exist, file might be new
		if len(storedNodes) == 0 {
			allCandidates = append(allCandidates, symbolChange{
				Name:     "(new file)",
				FilePath: filePath,
				Status:   "new_file",
			})
			newFileCount++
		}
	}

	totalCandidates := len(allCandidates)

	// Fetch file intents for all changed files (used in WhyNow)
	fileIntents := make(map[string]string)
	if len(changedFiles) > 0 {
		intents, intentErr := deps.Store.GetFileIntentsByPaths(changedFiles)
		if intentErr == nil && len(intents) > 0 {
			for path, fi := range intents {
				fileIntents[path] = fi.IntentText
			}
		}
	}

	// Score each candidate
	type scoredCandidate struct {
		symbolChange
		betweenness    float64
		dependentCount int
		changeSeverity float64
		composite      float64
	}

	useFastPath := totalCandidates > 100 // skip BlastRadiusWithDepth for large pools

	scored := make([]scoredCandidate, 0, totalCandidates)
	maxDependents := 0

	// First pass: collect betweenness and dependent counts
	for _, c := range allCandidates {
		sc := scoredCandidate{symbolChange: c}

		// Change severity
		switch c.Status {
		case "deleted":
			sc.changeSeverity = 1.0
		case "new_file":
			sc.changeSeverity = 0.7
		default:
			sc.changeSeverity = 0.5
		}

		// Betweenness from stored node scores
		if c.ID != "" {
			nodeScore, err := deps.Store.GetNodeScore(c.ID)
			if err == nil && nodeScore != nil {
				sc.betweenness = nodeScore.Betweenness
			}
		}

		// Dependent count via blast radius (depth 1), unless fast path
		if !useFastPath && c.ID != "" && deps.Graph != nil {
			depMap := deps.Graph.BlastRadiusWithDepth(c.ID, 1)
			// depMap includes the node itself, so subtract 1
			sc.dependentCount = len(depMap)
			if sc.dependentCount > 0 {
				sc.dependentCount-- // exclude the node itself
			}
		}

		if sc.dependentCount > maxDependents {
			maxDependents = sc.dependentCount
		}

		scored = append(scored, sc)
	}

	// Second pass: compute composite scores
	for i := range scored {
		if useFastPath {
			// Betweenness-only ranking for large pools
			scored[i].composite = 0.8*scored[i].betweenness + 0.2*scored[i].changeSeverity
		} else {
			normalizedDeps := 0.0
			if maxDependents > 0 {
				normalizedDeps = float64(scored[i].dependentCount) / float64(maxDependents)
			}
			scored[i].composite = 0.5*scored[i].betweenness + 0.3*normalizedDeps + 0.2*scored[i].changeSeverity
		}
	}

	// Sort by composite score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].composite > scored[j].composite
	})

	// Cap to limit
	topN := scored
	if len(topN) > p.Limit {
		topN = topN[:p.Limit]
	}

	// Count high-risk changes (betweenness > 0.1)
	highRiskCount := 0
	for _, sc := range scored {
		if sc.betweenness > 0.1 {
			highRiskCount++
		}
	}

	// Build InspectableResponse
	inspectables := make([]types.Inspectable, 0, len(topN))
	for i, sc := range topN {
		// Build reason string
		var reasonParts []string
		if sc.betweenness > 0 {
			reasonParts = append(reasonParts, fmt.Sprintf("betweenness %.2f", sc.betweenness))
		}
		if sc.dependentCount > 0 {
			reasonParts = append(reasonParts, fmt.Sprintf("%d direct dependents", sc.dependentCount))
		}
		switch sc.Status {
		case "deleted":
			reasonParts = append(reasonParts, "deleted symbol")
		case "new_file":
			reasonParts = append(reasonParts, "new file")
		case "file_modified":
			reasonParts = append(reasonParts, "file modified")
		}
		reason := strings.Join(reasonParts, ", ")
		if reason == "" {
			reason = "Changed symbol"
		}

		// Determine next tool
		nextTool := "read_symbol"
		if sc.betweenness > 0.05 {
			nextTool = "impact"
		}

		// Build next args — new files have no ID, fall back to explore by file path
		nextArgs := map[string]string{}
		if sc.ID != "" {
			nextArgs["symbol_id"] = sc.ID
		} else if sc.FilePath != "" {
			nextTool = "explore"
			nextArgs["symbol"] = sc.FilePath
		}

		// WhyNow from file intent
		whyNow := fileIntents[sc.FilePath]

		inspectables = append(inspectables, types.Inspectable{
			Rank:       i + 1,
			TargetType: "symbol",
			Name:       sc.Name,
			FilePath:   sc.FilePath,
			ID:         sc.ID,
			Score:      sc.composite,
			Reason:     reason,
			WhyNow:     whyNow,
			NextTool:   nextTool,
			NextArgs:   nextArgs,
		})
	}

	summary := fmt.Sprintf("%d high-risk changes (betweenness > 0.1), %d total modified symbols, %d new files, %d deleted",
		highRiskCount, modifiedCount, newFileCount, deletedCount)

	return types.InspectableResponse{
		Inspectables: inspectables,
		Total:        totalCandidates,
		Summary:      summary,
	}, nil
}

func registerDetectChangesTool(s *Server, deps ToolDeps) {
	desc := "What changed and what matters most? Returns the top 5 highest-risk symbol changes since a git ref, ranked by centrality and blast radius."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p DetectChangesParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return detectChangesHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "detect_changes",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"since": map[string]interface{}{
					"type":        "string",
					"description": "Git ref to compare against (e.g., 'HEAD~5', 'main', a commit hash)",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Optional path filter for changed files",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of ranked changes to return (default: 5)",
				},
			},
			"required": []string{"since"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("detect_changes", deps.Profile) {
		if err := s.RegisterSDKTool("detect_changes", desc, func(p DetectChangesParams) (*mcp_golang.ToolResponse, error) {
			result, err := detectChangesHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'detect_changes': %v", err)
		}
	}
}

// ----- Tool 10: get_architecture_summary -----

func architectureSummaryHandler(deps ToolDeps, p ArchitectureSummaryParams) (interface{}, error) {
	if deps.Graph == nil {
		return nil, fmt.Errorf("graph engine not initialized")
	}
	if p.Limit == 0 {
		p.Limit = 5
	}

	// Get graph metrics
	communities, modularity := deps.Graph.DetectCommunities()
	pageranks := deps.Graph.PageRank()
	betweenness, _ := deps.Store.GetAllBetweenness()

	// Collect all candidates with their structural role
	type candidate struct {
		hashID     string
		targetType string // "entry_point", "hub", "connector"
		reason     string
		outDegree  int
	}
	candidateMap := make(map[string]*candidate)

	// Entry points: zero in-degree
	for _, hashID := range deps.Graph.GetEntryPoints() {
		outDeg := deps.Graph.GetOutDegree(hashID)
		candidateMap[hashID] = &candidate{
			hashID:     hashID,
			targetType: "entry_point",
			reason:     fmt.Sprintf("Entry point: %d downstream callees", outDeg),
			outDegree:  outDeg,
		}
	}

	// Hubs: highest out-degree (collect more than limit for ranking pool)
	poolSize := p.Limit * 3
	if poolSize < 20 {
		poolSize = 20
	}
	for _, h := range deps.Graph.GetHubs(poolSize) {
		if _, exists := candidateMap[h.HashID]; !exists {
			candidateMap[h.HashID] = &candidate{
				hashID:     h.HashID,
				targetType: "hub",
				reason:     fmt.Sprintf("Hub: calls %d functions", h.OutDegree),
				outDegree:  h.OutDegree,
			}
		}
	}

	// Connectors: high betweenness + multi-community edges
	for _, hashID := range deps.Graph.GetConnectors(betweenness, poolSize) {
		if _, exists := candidateMap[hashID]; !exists {
			btwn := betweenness[hashID]
			candidateMap[hashID] = &candidate{
				hashID:     hashID,
				targetType: "connector",
				reason:     fmt.Sprintf("Connector: betweenness %.3f, bridges communities", btwn),
				outDegree:  deps.Graph.GetOutDegree(hashID),
			}
		}
	}

	// Normalize out-degrees for scoring
	var maxOutDegree int
	for _, c := range candidateMap {
		if c.outDegree > maxOutDegree {
			maxOutDegree = c.outDegree
		}
	}

	// Normalize PageRank among candidates for comparable [0,1] scale
	var maxPR float64
	for _, c := range candidateMap {
		if pr := pageranks[c.hashID]; pr > maxPR {
			maxPR = pr
		}
	}

	// Score and rank candidates
	type scoredCandidate struct {
		candidate
		score float64
	}
	var scored []scoredCandidate
	for _, c := range candidateMap {
		pr := pageranks[c.hashID]
		normalizedPR := 0.0
		if maxPR > 0 {
			normalizedPR = pr / maxPR
		}
		btwn := betweenness[c.hashID]
		normalizedDeg := 0.0
		if maxOutDegree > 0 {
			normalizedDeg = float64(c.outDegree) / float64(maxOutDegree)
		}
		score := 0.4*normalizedPR + 0.3*btwn + 0.3*normalizedDeg
		scored = append(scored, scoredCandidate{candidate: *c, score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	if len(scored) > p.Limit {
		scored = scored[:p.Limit]
	}

	// Build inspectables
	inspectables := make([]types.Inspectable, 0, len(scored))
	for _, sc := range scored {
		node, err := deps.Store.GetNode(sc.hashID)
		if err != nil {
			continue
		}

		// Set next_tool based on structural role
		nextTool := "understand"
		if sc.targetType == "hub" || sc.targetType == "connector" {
			nextTool = "impact"
		}

		symbolRef := node.ID
		if symbolRef == "" {
			symbolRef = node.SymbolName
		}

		inspectables = append(inspectables, types.Inspectable{
			Rank:       len(inspectables) + 1,
			TargetType: sc.targetType,
			Name:       node.SymbolName,
			FilePath:   node.FilePath,
			ID:         node.ID,
			Score:      sc.score,
			Reason:     sc.reason,
			NextTool:   nextTool,
			NextArgs:   map[string]string{"symbol_id": symbolRef},
		})
	}

	summary := fmt.Sprintf("%d communities (modularity %.2f), %d nodes, %d edges — top %d structural nodes shown",
		len(communities), modularity, deps.Graph.NodeCount(), deps.Graph.EdgeCount(), len(inspectables))

	return types.InspectableResponse{
		Inspectables: inspectables,
		Total:        len(scored),
		Summary:      summary,
	}, nil
}

func registerArchitectureSummaryTool(s *Server, deps ToolDeps) {
	desc := "Top entry points, hubs, and connectors in the codebase ranked by structural importance. Bounded output — returns counts and top 5 nodes, not full member lists."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ArchitectureSummaryParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return architectureSummaryHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "get_architecture_summary",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of structurally important nodes to return (default: 5)",
					"default":     5,
				},
			},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("get_architecture_summary", deps.Profile) {
		if err := s.RegisterSDKTool("get_architecture_summary", desc, func(p ArchitectureSummaryParams) (*mcp_golang.ToolResponse, error) {
			result, err := architectureSummaryHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'get_architecture_summary': %v", err)
		}
	}
}

// ----- Tool 11: explore -----

func exploreHandler(deps ToolDeps, p ExploreParams) (interface{}, error) {
	if p.Symbol == "" {
		return nil, fmt.Errorf("'symbol' is required")
	}
	if p.Depth == 0 {
		p.Depth = 2
	}
	if p.Depth > 10 {
		p.Depth = 10
	}

	type matchInfo struct {
		Name     string `json:"name"`
		FilePath string `json:"file_path"`
		NodeType string `json:"node_type"`
		ID       string `json:"id"`
	}

	// Search by name: exact match first, then pattern match
	var matches []matchInfo
	exactNode, err := deps.Store.GetNodeByName(p.Symbol)
	if err == nil {
		matches = append(matches, matchInfo{
			Name:     exactNode.SymbolName,
			FilePath: exactNode.FilePath,
			NodeType: exactNode.NodeType.String(),
			ID:       exactNode.ID,
		})
	}

	// Pattern match
	patternNodes, err := deps.Store.SearchNodesByName(p.Symbol)
	if err == nil {
		for _, n := range patternNodes {
			// Avoid duplicates with exact match
			if exactNode != nil && n.ID == exactNode.ID {
				continue
			}
			matches = append(matches, matchInfo{
				Name:     n.SymbolName,
				FilePath: n.FilePath,
				NodeType: n.NodeType.String(),
				ID:       n.ID,
			})
		}
	}

	// Also try FTS search
	if deps.Search != nil && len(matches) < 10 {
		ftsResults, err := deps.Search.Search(p.Symbol, 10, nil, 5)
		if err == nil {
			existingIDs := make(map[string]bool)
			for _, m := range matches {
				existingIDs[m.ID] = true
			}
			for _, r := range ftsResults {
				if !existingIDs[r.Node.ID] {
					matches = append(matches, matchInfo{
						Name:     r.Node.SymbolName,
						FilePath: r.Node.FilePath,
						NodeType: r.Node.NodeType.String(),
						ID:       r.Node.ID,
					})
				}
			}
		}
	}

	// Convert matches to Inspectable items
	var inspectables []types.Inspectable
	for i, m := range matches {
		nextTool := "read_symbol"
		if m.NodeType == "struct" || m.NodeType == "class" || m.NodeType == "interface" {
			nextTool = "understand"
		}

		inspectables = append(inspectables, types.Inspectable{
			Rank:       i + 1,
			TargetType: "symbol",
			Name:       m.Name,
			FilePath:   m.FilePath,
			ID:         m.ID,
			Score:      0,
			Reason:     fmt.Sprintf("Matched symbol (%s)", m.NodeType),
			NextTool:   nextTool,
			NextArgs:   map[string]string{"symbol_id": m.ID},
		})
	}

	totalMatches := len(matches)
	if len(inspectables) > 10 {
		inspectables = inspectables[:10]
	}

	result := map[string]interface{}{
		"inspectables": inspectables,
		"total":        totalMatches,
		"query":        p.Symbol,
		"summary":      fmt.Sprintf("Found %d matches for '%s'", totalMatches, p.Symbol),
	}

	// Collect dependencies if requested
	if p.IncludeDeps && deps.Graph != nil && len(matches) > 0 {
		// Use the first match for dependency analysis
		primaryID := matches[0].ID

		depsIDs, dependentIDs := deps.Graph.CollectDeps(primaryID, p.Depth)

		var dependencies []matchInfo
		for _, hashID := range depsIDs {
			if node, err := deps.Store.GetNode(hashID); err == nil {
				dependencies = append(dependencies, matchInfo{
					Name:     node.SymbolName,
					FilePath: node.FilePath,
					NodeType: node.NodeType.String(),
					ID:       node.ID,
				})
			}
		}

		var dependents []matchInfo
		for _, hashID := range dependentIDs {
			if node, err := deps.Store.GetNode(hashID); err == nil {
				dependents = append(dependents, matchInfo{
					Name:     node.SymbolName,
					FilePath: node.FilePath,
					NodeType: node.NodeType.String(),
					ID:       node.ID,
				})
			}
		}

		// Identify hotspots: nodes in the result set with highest betweenness
		// Use pre-computed betweenness from node_scores instead of O(V*E) recomputation
		betweenness, _ := deps.Store.GetAllBetweenness()
		type hotspot struct {
			Name        string  `json:"name"`
			FilePath    string  `json:"file_path"`
			Betweenness float64 `json:"betweenness"`
		}

		allRelatedIDs := append(depsIDs, dependentIDs...)
		allRelatedIDs = append(allRelatedIDs, primaryID)
		var hotspots []hotspot
		for _, hashID := range allRelatedIDs {
			if btwn, ok := betweenness[hashID]; ok && btwn > 0 {
				if node, err := deps.Store.GetNode(hashID); err == nil {
					hotspots = append(hotspots, hotspot{
						Name:        node.SymbolName,
						FilePath:    node.FilePath,
						Betweenness: btwn,
					})
				}
			}
		}
		sort.Slice(hotspots, func(i, j int) bool {
			return hotspots[i].Betweenness > hotspots[j].Betweenness
		})
		if len(hotspots) > 5 {
			hotspots = hotspots[:5]
		}

		result["dependencies"] = dependencies
		result["dependents"] = dependents
		result["hotspots"] = hotspots
	}

	return result, nil
}

func registerExploreTool(s *Server, deps ToolDeps) {
	desc := "Search for symbols by name with optional dependency traversal and hotspot detection. Use for targeted symbol lookup when you know a partial name."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ExploreParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return exploreHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "explore",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"symbol": map[string]interface{}{
					"type":        "string",
					"description": "Symbol name to search for",
				},
				"include_deps": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to include dependency/dependent analysis (default: false)",
					"default":     false,
				},
				"depth": map[string]interface{}{
					"type":        "integer",
					"description": "Depth for dependency traversal (default: 2)",
					"default":     2,
				},
			},
			"required": []string{"symbol"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("explore", deps.Profile) {
		if err := s.RegisterSDKTool("explore", desc, func(p ExploreParams) (*mcp_golang.ToolResponse, error) {
			result, err := exploreHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'explore': %v", err)
		}
	}
}

// ----- Tool 12: understand -----

func understandHandler(deps ToolDeps, p UnderstandParams) (interface{}, error) {
	if p.Symbol == "" {
		return nil, fmt.Errorf("'symbol' is required")
	}

	type symbolDetail struct {
		Name     string `json:"name"`
		FilePath string `json:"file_path"`
		NodeType string `json:"node_type"`
		ID       string `json:"id"`
	}

	// Tier 1: Exact match
	var found *symbolDetail
	var resolvedID string
	resolution := "exact"

	exactNode, err := deps.Store.GetNodeByName(p.Symbol)
	if err == nil {
		found = &symbolDetail{
			Name:     exactNode.SymbolName,
			FilePath: exactNode.FilePath,
			NodeType: exactNode.NodeType.String(),
			ID:       exactNode.ID,
		}
		resolvedID = exactNode.ID
	}

	// Tier 2: Fuzzy match (pattern search)
	if found == nil {
		resolution = "fuzzy"
		patternNodes, err := deps.Store.SearchNodesByName(p.Symbol)
		if err == nil && len(patternNodes) > 0 {
			n := patternNodes[0]
			found = &symbolDetail{
				Name:     n.SymbolName,
				FilePath: n.FilePath,
				NodeType: n.NodeType.String(),
				ID:       n.ID,
			}
			resolvedID = n.ID
		}
	}

	// Tier 3: FTS search (file-scoped search)
	// H1: Search with a larger limit and prefer exact name matches over
	// higher-scoring but differently-named results.
	if found == nil && deps.Search != nil {
		resolution = "fts"
		results, err := deps.Search.Search(p.Symbol, 10, nil)
		if err == nil && len(results) > 0 {
			// Prefer exact name match among top results
			var bestIdx int
			exactFound := false
			for i, r := range results {
				if strings.EqualFold(r.Node.SymbolName, p.Symbol) {
					bestIdx = i
					exactFound = true
					break
				}
			}
			if !exactFound {
				bestIdx = 0 // fall back to best FTS result
			}
			n := results[bestIdx].Node
			found = &symbolDetail{
				Name:     n.SymbolName,
				FilePath: n.FilePath,
				NodeType: n.NodeType.String(),
				ID:       n.ID,
			}
			resolvedID = n.ID
		}
	}

	if found == nil {
		return nil, fmt.Errorf("symbol not found: %s", p.Symbol)
	}

	result := map[string]interface{}{
		"symbol":     found,
		"resolution": resolution,
	}

	// Cold Start: add file intent for the symbol's file
	fi, fiErr := deps.Store.GetFileIntent(found.FilePath)
	if fiErr == nil && fi != nil {
		result["file_intent"] = fi.IntentText
	}

	// Get callers and callees from graph
	if deps.Graph != nil && resolvedID != "" {
		callerIDs := deps.Graph.GetCallers(resolvedID)
		var callers []symbolDetail
		for _, hashID := range callerIDs {
			if node, err := deps.Store.GetNode(hashID); err == nil {
				callers = append(callers, symbolDetail{
					Name:     node.SymbolName,
					FilePath: node.FilePath,
					NodeType: node.NodeType.String(),
					ID:       node.ID,
				})
			}
		}

		calleeIDs := deps.Graph.GetCallees(resolvedID)
		var callees []symbolDetail
		for _, hashID := range calleeIDs {
			if node, err := deps.Store.GetNode(hashID); err == nil {
				callees = append(callees, symbolDetail{
					Name:     node.SymbolName,
					FilePath: node.FilePath,
					NodeType: node.NodeType.String(),
					ID:       node.ID,
				})
			}
		}

		result["callers"] = callers
		result["callees"] = callees

		// PageRank score — use stored scores to avoid O(V * iterations) recomputation
		if score, err := deps.Store.GetNodeScore(resolvedID); err == nil {
			result["pagerank"] = score.PageRank
		} else {
			// Fallback to computing if no stored score
			pageranks := deps.Graph.PageRank()
			if pageranks != nil {
				result["pagerank"] = pageranks[resolvedID]
			}
		}

		// Community membership (with early exit)
		communities, _ := deps.Graph.DetectCommunities()
		for _, c := range communities {
			communityFound := false
			for _, nodeID := range c.NodeIDs {
				if nodeID == resolvedID {
					result["community"] = c.ID
					communityFound = true
					break
				}
			}
			if communityFound {
				break
			}
		}
	}

	// Add chaining hints
	symbolRef := found.ID
	if symbolRef == "" {
		symbolRef = found.Name
	}
	symbolRefJSON, _ := json.Marshal(symbolRef)
	result["next_tools"] = []map[string]string{
		{"tool": "impact", "args_hint": `{"symbol_id": ` + string(symbolRefJSON) + `}`},
		{"tool": "trace_call_path", "args_hint": `{"from": ` + string(symbolRefJSON) + `}`},
	}

	return result, nil
}

func registerUnderstandTool(s *Server, deps ToolDeps) {
	desc := "Deep analysis of a symbol: callers, callees, PageRank importance, community membership, and recent file changes. Use to understand what a symbol does and why it matters."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p UnderstandParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return understandHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "understand",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"symbol": map[string]interface{}{
					"type":        "string",
					"description": "Symbol name to understand",
				},
			},
			"required": []string{"symbol"},
		},
	}, cliHandler)

	// SDK handler — gated by profile
	if isToolInProfile("understand", deps.Profile) {
		if err := s.RegisterSDKTool("understand", desc, func(p UnderstandParams) (*mcp_golang.ToolResponse, error) {
			result, err := understandHandler(deps, p)
			if err != nil {
				return mcp_golang.NewToolResponse(mcp_golang.NewTextContent("Error: "+err.Error())), nil
			}
			return toToolResponse(result)
		}); err != nil {
			log.Printf("Warning: failed to register SDK tool 'understand': %v", err)
		}
	}
}

// ----- Helpers -----

// ----- MCP Resources (M9) -----

func registerResources(s *Server, deps ToolDeps) {
	// Register codebase graph statistics as a resource
	if err := s.sdk.RegisterResource(
		"qb://graph/stats",
		"Graph Statistics",
		"Current codebase graph statistics including node count, edge count, and community info",
		"application/json",
		func() (*mcp_golang.ResourceResponse, error) {
			var nodeCount, edgeCount int
			if deps.Graph != nil {
				nodeCount = deps.Graph.NodeCount()
				edgeCount = deps.Graph.EdgeCount()
			}
			data := map[string]interface{}{
				"nodes": nodeCount,
				"edges": edgeCount,
			}
			if deps.Graph != nil {
				communities, modularity := deps.Graph.DetectCommunities()
				data["communities"] = len(communities)
				data["modularity"] = modularity
			}
			jsonBytes, _ := json.MarshalIndent(data, "", "  ")
			return mcp_golang.NewResourceResponse(
				mcp_golang.NewTextEmbeddedResource("qb://graph/stats", string(jsonBytes), "application/json"),
			), nil
		},
	); err != nil {
		log.Printf("Warning: failed to register resource 'qb://graph/stats': %v", err)
	}
}

// ----- MCP Prompts (M9) -----

type explainSymbolArgs struct {
	Symbol string `json:"symbol" jsonschema:"required,description=The symbol name to explain"`
}

func registerPrompts(s *Server, deps ToolDeps) {
	// Register an "explain symbol" prompt template
	if err := s.sdk.RegisterPrompt(
		"explain_symbol",
		"Generate a prompt to explain a code symbol in context",
		func(args explainSymbolArgs) (*mcp_golang.PromptResponse, error) {
			promptText := fmt.Sprintf(
				"Explain the purpose and behavior of the code symbol '%s'. "+
					"Use the `context` tool to find it, then `read_symbol` to read its source code, "+
					"and `impact` to understand its blast radius. Provide a clear, concise explanation.",
				args.Symbol,
			)
			return mcp_golang.NewPromptResponse(
				"Explain: "+args.Symbol,
				mcp_golang.NewPromptMessage(
					mcp_golang.NewTextContent(promptText),
					mcp_golang.RoleUser,
				),
			), nil
		},
	); err != nil {
		log.Printf("Warning: failed to register prompt 'explain_symbol': %v", err)
	}
}

// toToolResponse converts a generic result (string or anything JSON-serializable)
// into the SDK's ToolResponse format.
func toToolResponse(result interface{}) (*mcp_golang.ToolResponse, error) {
	// Cap response size at 1MB to prevent memory issues
	const maxResponseSize = 1024 * 1024

	var text string
	switch v := result.(type) {
	case string:
		text = v
	default:
		jsonBytes, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
		text = string(jsonBytes)
	}
	if len(text) > maxResponseSize {
		// M2/M11: Produce valid JSON when truncating JSON payloads.
		// Detect JSON by checking the first non-whitespace byte.
		trimmed := strings.TrimSpace(text)
		isJSON := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
		if isJSON {
			// Build a wrapper that keeps the output valid JSON.
			// Reserve space for the envelope so the total stays within the limit.
			const envelope = `{"truncated":true,"message":"Response exceeded 1MB size limit","partial_data":""}`
			budget := maxResponseSize - len(envelope)
			if budget < 0 {
				budget = 0
			}
			partial := text[:budget]
			// Walk back to a valid UTF-8 boundary.
			for !utf8.ValidString(partial) && len(partial) > 0 {
				partial = partial[:len(partial)-1]
			}
			// Escape the partial data so it is a valid JSON string value.
			escapedBytes, _ := json.Marshal(partial)
			// json.Marshal wraps in quotes; strip them for embedding.
			escaped := string(escapedBytes[1 : len(escapedBytes)-1])
			// M6: JSON escaping can expand the text beyond the budget.
			// Re-check after escaping and truncate the escaped string if needed,
			// being careful not to break a JSON escape sequence mid-way.
			if len(escaped) > budget {
				escaped = escaped[:budget]
				// Walk back to avoid breaking a JSON escape sequence.
				// JSON escape sequences start with '\' and are 2-6 chars
				// (e.g., \n, \t, \uXXXX). Find the last safe boundary.
				for len(escaped) > 0 && escaped[len(escaped)-1] == '\\' {
					escaped = escaped[:len(escaped)-1]
				}
				// Also check for broken \uXXXX sequences: if the last
				// backslash-u sequence doesn't have 4 hex digits after it,
				// truncate to before that sequence.
				if idx := strings.LastIndex(escaped, `\u`); idx >= 0 && idx+6 > len(escaped) {
					escaped = escaped[:idx]
				}
			}
			text = `{"truncated":true,"message":"Response exceeded 1MB size limit","partial_data":"` + escaped + `"}`
		} else {
			// Plain text: truncate at a valid UTF-8 boundary.
			truncated := text[:maxResponseSize]
			for !utf8.ValidString(truncated) && len(truncated) > 0 {
				truncated = truncated[:len(truncated)-1]
			}
			text = truncated + "\n... [truncated, response exceeded 1MB]"
		}
	}
	return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(text)), nil
}


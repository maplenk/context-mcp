package mcp

import (
	"bufio"
	"context"
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

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maplenk/context-mcp/internal/graph"
	"github.com/maplenk/context-mcp/internal/search"
	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/types"
)

// Version is set by ldflags during release builds.
var Version = "dev"

// ToolDeps holds dependencies needed by MCP tools
type ToolDeps struct {
	Store       *storage.Store
	Graph       *graph.GraphEngine
	Search      *search.HybridSearch
	RepoRoot    string
	Profile     string
	Checkpoints *CheckpointStore // nil-safe, tools skip if nil
	OutputStore *OutputStore     // nil-safe, initialized in RegisterTools if nil
}

// No global output store — passed through call chain via deps.OutputStore.

// stripInspectable returns a compact version of an Inspectable.
// Compact contract: Keep ID, Name, FilePath, Score/Rank, line spans.
// Drop: Reason, WhyNow, NextTool, NextArgs.
func stripInspectable(i types.Inspectable) types.Inspectable {
	i.Reason = ""
	i.WhyNow = ""
	i.NextTool = ""
	i.NextArgs = nil
	return i
}

// isToolInProfile returns true if a tool should be registered for the MCP SDK
// based on the given profile. CLI tools are always registered regardless.
func isToolInProfile(toolName, profile string) bool {
	// Infrastructure tools always available in all profiles
	if toolName == "retrieve_output" {
		return true
	}
	coreTools := map[string]bool{
		"context":           true,
		"read_symbol":       true,
		"list_file_symbols": true,
		"understand":        true,
		"impact":            true,
		"detect_changes":    true,
		"trace_call_path":   true,
	}
	extendedTools := map[string]bool{
		"get_architecture_summary": true,
		"get_key_symbols":          true,
		"explore":                  true,
		"search_code":              true,
		"assemble_context":         true,
		"compare_symbols":          true,
		"find_routes":              true,
		"trace_route":              true,
		"compare_routes":           true,
		"checkpoint_context":       true,
		"read_delta":               true,
	}
	switch profile {
	case "full":
		return true
	case "extended":
		return coreTools[toolName] || extendedTools[toolName]
	case "minimal":
		return toolName == "discover_tools" || toolName == "execute_tool" || toolName == "health"
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
	Compact     bool     `json:"compact,omitempty" jsonschema:"description=Return compact output: IDs, scores, and line spans only. Drops reasons, next-tool hints, and verbose prose."`
}

// ImpactParams are the parameters for the impact tool.
type ImpactParams struct {
	SymbolID string `json:"symbol_id" jsonschema:"required,description=The ID or name of the symbol to analyze"`
	Depth    int    `json:"depth,omitempty" jsonschema:"description=Maximum BFS traversal depth (default: 5)"`
	Compact  bool   `json:"compact,omitempty" jsonschema:"description=Return compact output: IDs, scores, and line spans only. Drops reasons, next-tool hints, and verbose prose."`
}

// ReadSymbolParams are the parameters for the read_symbol tool.
type ReadSymbolParams struct {
	SymbolID  string `json:"symbol_id" jsonschema:"required,description=The ID or name of the symbol to read"`
	Mode      string `json:"mode,omitempty" jsonschema:"description=Read mode: bounded (default), signature, section, flow_summary, or full"`
	MaxChars  int    `json:"max_chars,omitempty" jsonschema:"description=Maximum characters to return for source-bearing modes (default: 6000, hard cap: 20000)"`
	MaxLines  int    `json:"max_lines,omitempty" jsonschema:"description=Maximum lines to return for source-bearing modes (default: 60, hard cap: 200)"`
	StartLine int    `json:"start_line,omitempty" jsonschema:"description=Optional 1-based file-relative start line for section reads"`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"description=Optional 1-based file-relative end line for section reads"`
	Section   string `json:"section,omitempty" jsonschema:"description=Section selector for section reads: top, middle, bottom, auto"`
}

// ListFileSymbolsParams are the parameters for the list_file_symbols tool.
type ListFileSymbolsParams struct {
	Path  string   `json:"path" jsonschema:"required,description=Repo-relative or absolute-under-repo file path to inspect"`
	Limit int      `json:"limit,omitempty" jsonschema:"description=Maximum symbols to return (default: 200)"`
	Kinds []string `json:"kinds,omitempty" jsonschema:"description=Optional node-type filters such as function, method, class, struct, interface, route"`
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
	Since   string `json:"since" jsonschema:"required,description=Git ref to compare against (e.g. HEAD~5 or main or a commit hash)"`
	Path    string `json:"path,omitempty" jsonschema:"description=Optional path filter for changed files"`
	Limit   int    `json:"limit,omitempty" jsonschema:"description=Maximum number of ranked changes to return (default: 5)"`
	Compact bool   `json:"compact,omitempty" jsonschema:"description=Return compact output: IDs, scores, and line spans only. Drops reasons, next-tool hints, and verbose prose."`
}

// ArchitectureSummaryParams are the parameters for the get_architecture_summary tool
type ArchitectureSummaryParams struct {
	Limit   int  `json:"limit,omitempty" jsonschema:"description=Maximum number of structurally important nodes to return (default: 5)"`
	Compact bool `json:"compact,omitempty" jsonschema:"description=Return compact output: IDs, scores, and line spans only. Drops reasons, next-tool hints, and verbose prose."`
}

// ExploreParams are the parameters for the explore tool
type ExploreParams struct {
	Symbol      string `json:"symbol" jsonschema:"required,description=Symbol name to search for"`
	IncludeDeps bool   `json:"include_deps,omitempty" jsonschema:"description=Whether to include dependency/dependent analysis (default: false)"`
	Depth       int    `json:"depth,omitempty" jsonschema:"description=Depth for dependency traversal (default: 2)"`
	Compact     bool   `json:"compact,omitempty" jsonschema:"description=Return compact output: IDs, scores, and line spans only. Drops reasons, next-tool hints, and verbose prose."`
}

// UnderstandParams are the parameters for the understand tool
type UnderstandParams struct {
	Symbol  string `json:"symbol" jsonschema:"required,description=Symbol name to understand"`
	Compact bool   `json:"compact,omitempty" jsonschema:"description=Return compact output: IDs, scores, and line spans only. Drops reasons, next-tool hints, and verbose prose."`
}

// AssembleContextParams are the parameters for the assemble_context tool
type AssembleContextParams struct {
	Query            string   `json:"query" jsonschema:"required,description=Search query to find relevant code"`
	Task             string   `json:"task,omitempty" jsonschema:"description=Deprecated alias for query. Prefer query."`
	BudgetTokens     int      `json:"budget_tokens,omitempty" jsonschema:"description=Maximum token budget for assembled context (default: 4000)"`
	Mode             string   `json:"mode,omitempty" jsonschema:"description=Output fidelity: summary, signatures, snippets, bundle, or full (default: snippets)"`
	ActiveFiles      []string `json:"active_files,omitempty" jsonschema:"description=File paths currently being edited for PPR personalization"`
	MaxPerFile       int      `json:"max_per_file,omitempty" jsonschema:"description=Maximum results per file (default: 2)"`
	IncludeNeighbors bool     `json:"include_neighbors,omitempty" jsonschema:"description=Include callers/callees of top results (default: false)"`
	Compact          bool     `json:"compact,omitempty" jsonschema:"description=Return compact output: IDs, scores, and line spans only. Drops reasons, next-tool hints, and verbose prose."`
	Goal             string   `json:"goal,omitempty" jsonschema:"description=Optional task shape: inspect_symbol, compare_symbols, find_routes, trace_route, compare_routes"`
	Targets          []string `json:"targets,omitempty" jsonschema:"description=Optional symbol or route references used with goal-aware assembly"`
}

// AssembleContextResponse is the structured response from assemble_context
type AssembleContextResponse struct {
	Query        string                `json:"query"`
	Mode         string                `json:"mode"`
	BudgetTokens int                   `json:"budget_tokens"`
	UsedTokens   int                   `json:"used_tokens"`
	Items        []AssembleContextItem `json:"items"`
	Excluded     int                   `json:"excluded"`
	Summary      string                `json:"summary"`
	Strategy     string                `json:"strategy,omitempty"`
}

// AssembleContextItem represents a single item in the assembled context
type AssembleContextItem struct {
	Rank     int     `json:"rank"`
	Name     string  `json:"name"`
	FilePath string  `json:"file_path"`
	ID       string  `json:"id"`
	Score    float64 `json:"score"`
	Content  string  `json:"content"`
	Tokens   int     `json:"tokens"`
	Reason   string  `json:"reason,omitempty"`
	Group    string  `json:"group,omitempty"`
}

type CompareSymbolsParams struct {
	Left    string `json:"left" jsonschema:"required,description=Left symbol name or ID"`
	Right   string `json:"right" jsonschema:"required,description=Right symbol name or ID"`
	Depth   int    `json:"depth,omitempty" jsonschema:"description=Traversal depth for callers/callees comparison (default: 1, max: 3)"`
	Compact bool   `json:"compact,omitempty" jsonschema:"description=Return compact output by dropping verbose summaries from list items"`
}

type FindRoutesParams struct {
	Query           string `json:"query" jsonschema:"required,description=Route path, handler, or concept query"`
	Method          string `json:"method,omitempty" jsonschema:"description=Optional HTTP method filter"`
	Limit           int    `json:"limit,omitempty" jsonschema:"description=Maximum number of routes to return (default: 10)"`
	IncludeHandlers *bool  `json:"include_handlers,omitempty" jsonschema:"description=Include resolved route handlers when available (default: true)"`
	Compact         bool   `json:"compact,omitempty" jsonschema:"description=Return compact output by dropping verbose summaries from list items"`
}

type TraceRouteParams struct {
	Route   string `json:"route" jsonschema:"required,description=Route ID, symbol, or path fragment to trace"`
	Depth   int    `json:"depth,omitempty" jsonschema:"description=Maximum downstream traversal depth from the handler (default: 2, max: 4)"`
	Compact bool   `json:"compact,omitempty" jsonschema:"description=Return compact output by dropping verbose summaries from list items"`
}

type CompareRoutesParams struct {
	Left    string `json:"left" jsonschema:"required,description=Left route ID or symbol"`
	Right   string `json:"right" jsonschema:"required,description=Right route ID or symbol"`
	Depth   int    `json:"depth,omitempty" jsonschema:"description=Maximum downstream traversal depth from each handler (default: 2, max: 4)"`
	Compact bool   `json:"compact,omitempty" jsonschema:"description=Return compact output by dropping verbose summaries from list items"`
}

// CheckpointContextParams are the parameters for the checkpoint_context tool
type CheckpointContextParams struct {
	Name string `json:"name,omitempty" jsonschema:"description=Checkpoint name (auto-generated if empty)"`
}

// ReadDeltaParams are the parameters for the read_delta tool
type ReadDeltaParams struct {
	Since string `json:"since" jsonschema:"description=Checkpoint name to compare against,required"`
	Path  string `json:"path,omitempty" jsonschema:"description=Filter by file path prefix"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=Maximum items per change type (default: 20)"`
}

// IndexFunc is the callback for triggering a re-index
type IndexFunc func(path string) error

// RegisterTools registers all 20 tools for CLI mode and profile-gated tools for MCP SDK mode.
// CLI tools: all 20 available via GetHandler/GetTools (always).
// SDK tools (MCP protocol): gated by deps.Profile — "minimal" (4), "core" (8), "extended" (15), or "full" (20).
func RegisterTools(s *Server, deps ToolDeps, indexFn IndexFunc) {
	// Initialize output store if not provided
	if deps.OutputStore == nil {
		deps.OutputStore = NewOutputStore(50, 10*time.Minute)
	}
	registerContextTool(s, deps)
	registerImpactTool(s, deps)
	registerReadSymbolTool(s, deps)
	registerListFileSymbolsTool(s, deps)
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
	registerCompareSymbolsTool(s, deps)
	registerFindRoutesTool(s, deps)
	registerTraceRouteTool(s, deps)
	registerCompareRoutesTool(s, deps)
	registerAssembleContextTool(s, deps)
	registerCheckpointContextTool(s, deps)
	registerReadDeltaTool(s, deps)
	// Discovery tools (registered in all modes, only SDK-active in minimal)
	registerDiscoverToolsTool(s, deps)
	registerExecuteToolTool(s, deps)
	// Output sandbox (registered in all profiles)
	registerRetrieveOutputTool(s, deps)
	// P4: Register MCP resources and prompts
	RegisterResources(s, deps)
	RegisterPrompts(s)
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
		nextArgs := defaultSymbolNextArgs(nextTool, symbolRef)

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

	// Compact mode: strip verbose fields
	if p.Compact {
		for i := range inspectables {
			inspectables[i] = stripInspectable(inspectables[i])
		}
		summary = ""
	}

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
					"description": "Maximum results per unique file path (default: 1)",
					"default":     1,
				},
				"active_files": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "string",
					},
					"description": "File paths the developer is currently editing for PPR personalization",
				},
				"compact": map[string]interface{}{
					"type":        "boolean",
					"description": "Return compact output: IDs, scores, and line spans only",
				},
			},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("context",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Context Search"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("query", mcp.Description("Natural language or keyword query to search for relevant code")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of results to return (default: 5)")),
		mcp.WithString("mode", mcp.Description("Search mode: 'search' (default) for hybrid search, 'architecture' for community detection")),
		mcp.WithNumber("max_per_file", mcp.Description("Maximum results per unique file path (default: 1)")),
		mcp.WithArray("active_files", mcp.Description("File paths the developer is currently editing for PPR personalization"), mcp.WithStringItems()),
		mcp.WithBoolean("compact", mcp.Description("Return compact output: IDs, scores, and line spans only")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/alwaysLoad": true,
			"anthropic/searchHint": "ranked code discovery; start here for where to look",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ContextParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := contextHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "context", deps.OutputStore)
	}

	// Register or defer based on profile
	if isToolInProfile("context", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("context", tool, sdkHandler)
	}
}

// ----- Tool 2: impact -----

func impactHandler(deps ToolDeps, p ImpactParams) (interface{}, error) {
	if p.SymbolID == "" {
		return nil, fmt.Errorf("'symbol_id' is required")
	}
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
	var direct []impactNode     // depth 1 = CRITICAL
	var highRisk []impactNode   // depth 2 = HIGH
	var mediumRisk []impactNode // depth 3 = MEDIUM
	var lowRisk []impactNode    // depth 4+ = LOW
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
			NextArgs:   defaultReadSymbolArgs(node.ID),
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
				NextArgs:   defaultReadSymbolArgs(node.ID),
			}
			highRisk = append(highRisk, n)
		case 3:
			n := impactNode{
				ID:         node.ID,
				SymbolName: node.SymbolName,
				FilePath:   node.FilePath,
				NextTool:   "read_symbol",
				NextArgs:   defaultReadSymbolArgs(node.ID),
			}
			mediumRisk = append(mediumRisk, n)
		default:
			n := impactNode{
				ID:         node.ID,
				SymbolName: node.SymbolName,
				FilePath:   node.FilePath,
				NextTool:   "read_symbol",
				NextArgs:   defaultReadSymbolArgs(node.ID),
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

	// Compact mode: keep only symbol_id, risk tiers with IDs only, total_affected
	if p.Compact {
		type compactNode struct {
			ID         string `json:"id"`
			SymbolName string `json:"symbol_name"`
		}
		compact := func(nodes []impactNode) []compactNode {
			out := make([]compactNode, len(nodes))
			for i, n := range nodes {
				out[i] = compactNode{ID: n.ID, SymbolName: n.SymbolName}
			}
			return out
		}
		return map[string]interface{}{
			"symbol":         p.SymbolID,
			"affected_count": totalAffected,
			"direct":         compact(direct),
			"high_risk":      compact(highRisk),
			"medium_risk":    compact(mediumRisk),
			"low_risk":       compact(lowRisk),
		}, nil
	}

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
				"compact": map[string]interface{}{
					"type":        "boolean",
					"description": "Return compact output: IDs, scores, and line spans only",
				},
			},
			"required": []string{"symbol_id"},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("impact",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Impact Analysis"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("symbol_id", mcp.Description("The ID or name of the symbol to analyze"), mcp.Required()),
		mcp.WithNumber("depth", mcp.Description("Maximum BFS traversal depth (default: 5)")),
		mcp.WithBoolean("compact", mcp.Description("Return compact output: IDs, scores, and line spans only")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "blast radius before editing code",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ImpactParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := impactHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "impact", deps.OutputStore)
	}

	if isToolInProfile("impact", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("impact", tool, sdkHandler)
	}
}

// ----- Tool 3: read_symbol -----

func resolveSymbolNode(deps ToolDeps, symbolID string) (*types.ASTNode, error) {
	if symbolID == "" {
		return nil, fmt.Errorf("'symbol_id' is required")
	}
	if node, err := deps.Store.GetNodeByName(symbolID); err == nil {
		return node, nil
	}
	node, err := deps.Store.GetNode(symbolID)
	if err != nil {
		return nil, fmt.Errorf("symbol not found: %s", symbolID)
	}
	return node, nil
}

func readSymbolHandler(deps ToolDeps, p ReadSymbolParams) (interface{}, error) {
	node, err := resolveSymbolNode(deps, p.SymbolID)
	if err != nil {
		return nil, err
	}
	if node.EndByte <= node.StartByte {
		return nil, fmt.Errorf("invalid byte range for %s: start=%d end=%d", node.SymbolName, node.StartByte, node.EndByte)
	}

	requestedMode := p.Mode
	if requestedMode == "" {
		requestedMode = defaultReadSymbolMode
	}
	if !readSymbolModes[requestedMode] {
		return nil, fmt.Errorf("invalid mode %q: must be one of bounded, signature, section, flow_summary, full", requestedMode)
	}
	if p.Section != "" && !readSymbolSections[p.Section] {
		return nil, fmt.Errorf("invalid section %q: must be one of top, middle, bottom, auto", p.Section)
	}

	fileCtx, err := loadSymbolFileContext(deps.RepoRoot, node.FilePath)
	if err != nil {
		return nil, err
	}
	inspection := buildSymbolInspection(*node, fileCtx)
	limits := clampReadSymbolLimits(p.MaxChars, p.MaxLines)

	symbolRef := node.ID
	if symbolRef == "" {
		symbolRef = node.SymbolName
	}
	symbolRefJSON, _ := json.Marshal(symbolRef)
	filePathJSON, _ := json.Marshal(node.FilePath)

	response := map[string]interface{}{
		"symbol_name":          node.SymbolName,
		"file_path":            node.FilePath,
		"node_type":            node.NodeType.String(),
		"start_byte":           node.StartByte,
		"end_byte":             node.EndByte,
		"symbol_start_line":    inspection.SymbolStartLine,
		"symbol_end_line":      inspection.SymbolEndLine,
		"mode_requested":       requestedMode,
		"mode_used":            requestedMode,
		"truncated":            false,
		"downgraded":           false,
		"downgrade_reason":     "",
		"signature":            inspection.Signature,
		"signature_start_line": inspection.SignatureStartLine,
		"signature_end_line":   inspection.SignatureEndLine,
		"outline":              inspection.Outline,
		"next_modes":           orderedReadSymbolModes(requestedMode),
		"applied_max_chars":    limits.MaxChars,
		"applied_max_lines":    limits.MaxLines,
		"stale":                inspection.Stale,
		"next_tools": []map[string]string{
			{"tool": "understand", "args_hint": `{"symbol": ` + string(symbolRefJSON) + `}`},
			{"tool": "impact", "args_hint": `{"symbol_id": ` + string(symbolRefJSON) + `}`},
			{"tool": "list_file_symbols", "args_hint": `{"path": ` + string(filePathJSON) + `, "limit": 50}`},
		},
	}

	// Heuristic staleness check: if the extracted symbol body no longer contains the base
	// symbol name, prefer a safe metadata-only response rather than returning misleading code.
	baseName := symbolBaseName(node.SymbolName)
	if baseName != "" && inspection.Source != "" && !strings.Contains(inspection.Source, baseName) {
		inspection.Stale = true
		inspection.StaleReason = "file may have changed since indexing"
		response["stale"] = true
	}
	if inspection.Stale {
		response["error"] = inspection.StaleReason
		response["selected_start_line"] = inspection.SymbolStartLine
		response["selected_end_line"] = inspection.SymbolEndLine
		return response, nil
	}

	if requestedMode == "signature" {
		response["selected_start_line"] = inspection.SignatureStartLine
		response["selected_end_line"] = inspection.SignatureEndLine
		response["next_modes"] = orderedReadSymbolModes("signature")
		return response, nil
	}

	if requestedMode == "flow_summary" {
		response["flow_summary"] = flowSummaryForInspection(inspection, symbolRef)
		response["selected_start_line"] = inspection.SymbolStartLine
		response["selected_end_line"] = inspection.SymbolEndLine
		response["next_modes"] = orderedReadSymbolModes("flow_summary")
		return response, nil
	}

	modeUsed := requestedMode
	if requestedMode == "full" && !symbolFitsWithinLimits(inspection, limits) {
		modeUsed = "bounded"
		response["downgraded"] = true
		response["downgrade_reason"] = "symbol_exceeds_safe_read_threshold"
	}
	response["mode_used"] = modeUsed
	response["next_modes"] = orderedReadSymbolModes(modeUsed)

	source, selectedStart, selectedEnd, truncated := selectSourceWindow(inspection, modeUsed, p.Section, p.StartLine, p.EndLine, limits)
	response["selected_start_line"] = selectedStart
	response["selected_end_line"] = selectedEnd
	response["truncated"] = truncated || selectedStart != inspection.SymbolStartLine || selectedEnd != inspection.SymbolEndLine
	if p.StartLine != 0 || p.EndLine != 0 {
		response["selected_section"] = "explicit_range"
	} else if modeUsed == "section" || (modeUsed == "bounded" && func() bool { v, ok := response["truncated"].(bool); return ok && v }()) {
		section := p.Section
		if section == "" {
			section = "auto"
		}
		response["selected_section"] = section
	}
	response["source"] = source
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
	desc := "Safely inspect a symbol by name or ID. Defaults to bounded reads with line spans, summary modes, and explicit full reads only when safe."

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
				"mode": map[string]interface{}{
					"type":        "string",
					"description": "Read mode: bounded (default), signature, section, flow_summary, or full",
					"default":     defaultReadSymbolMode,
				},
				"max_chars": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum characters to return for source-bearing modes (default: 6000, hard cap: 20000)",
					"default":     defaultReadSymbolMaxChars,
				},
				"max_lines": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum lines to return for source-bearing modes (default: 60, hard cap: 200)",
					"default":     defaultReadSymbolMaxLines,
				},
				"start_line": map[string]interface{}{
					"type":        "integer",
					"description": "Optional 1-based file-relative start line for section reads",
				},
				"end_line": map[string]interface{}{
					"type":        "integer",
					"description": "Optional 1-based file-relative end line for section reads",
				},
				"section": map[string]interface{}{
					"type":        "string",
					"description": "Section selector for section reads: top, middle, bottom, auto",
				},
			},
			"required": []string{"symbol_id"},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("read_symbol",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Read Symbol"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("symbol_id", mcp.Description("The ID or name of the symbol to read"), mcp.Required()),
		mcp.WithString("mode", mcp.Description("Read mode: bounded (default), signature, section, flow_summary, or full")),
		mcp.WithNumber("max_chars", mcp.Description("Maximum characters to return for source-bearing modes (default: 6000, hard cap: 20000)")),
		mcp.WithNumber("max_lines", mcp.Description("Maximum lines to return for source-bearing modes (default: 60, hard cap: 200)")),
		mcp.WithNumber("start_line", mcp.Description("Optional 1-based file-relative start line for section reads")),
		mcp.WithNumber("end_line", mcp.Description("Optional 1-based file-relative end line for section reads")),
		mcp.WithString("section", mcp.Description("Section selector for section reads: top, middle, bottom, auto")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "safe bounded inspection for a selected symbol",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ReadSymbolParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := readSymbolHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("read_symbol", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("read_symbol", tool, sdkHandler)
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

	// Always build the SDK tool
	tool := mcp.NewTool("query",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Query Index"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("sql", mcp.Description("SQL SELECT query to execute"), mcp.Required()),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "raw SQL query against the index",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p QueryParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := queryHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("query", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("query", tool, sdkHandler)
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
		"version": Version,
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

	// Always build the SDK tool
	tool := mcp.NewTool("health",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Health Check"),
		mcp.WithReadOnlyHintAnnotation(true),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "server health and index statistics",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := healthHandler(deps)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("health", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("health", tool, sdkHandler)
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

	// Always build the SDK tool
	tool := mcp.NewTool("index",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Reindex"),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("path", mcp.Description("Optional: specific path to re-index")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "trigger re-indexing of the repository",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p IndexParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := indexHandler(indexFn, p, deps.RepoRoot)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("index", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("index", tool, sdkHandler)
	}
}

// ----- Tool 7: trace_call_path -----

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
					ReadSymbolArgs: defaultReadSymbolArgs(node.ID),
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

	// Always build the SDK tool
	tool := mcp.NewTool("trace_call_path",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Trace Call Path"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("from", mcp.Description("Source symbol name or ID"), mcp.Required()),
		mcp.WithString("to", mcp.Description("Target symbol name or ID"), mcp.Required()),
		mcp.WithNumber("max_depth", mcp.Description("Maximum path depth to search (default: 10)")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "control flow from source to destination",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p TraceCallPathParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := traceCallPathHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("trace_call_path", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("trace_call_path", tool, sdkHandler)
	}
}

// ----- Tool 8: get_key_symbols -----

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
			NextArgs:   defaultSymbolNextArgs(nextTool, sym.ID),
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

	// Always build the SDK tool
	tool := mcp.NewTool("get_key_symbols",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Key Symbols"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithNumber("limit", mcp.Description("Maximum number of symbols to return (default: 5)")),
		mcp.WithString("file_filter", mcp.Description("Optional file path prefix to scope results to a specific directory")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "most important symbols by PageRank",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p GetKeySymbolsParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := getKeySymbolsHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("get_key_symbols", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("get_key_symbols", tool, sdkHandler)
	}
}

// ----- Tool 9: search_code -----

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

	// Global timeout to prevent unbounded scanning across many files
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var matches []codeMatch
	var scanWarnings []string
	filesScanned := 0
	filesWithErrors := 0
	for _, relPath := range filePaths {
		if ctx.Err() != nil {
			scanWarnings = append(scanWarnings, "global 60s timeout reached, results may be incomplete")
			break
		}
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

	// Always build the SDK tool
	tool := mcp.NewTool("search_code",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Search Code"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("pattern", mcp.Description("Regex pattern to search for in file contents"), mcp.Required()),
		mcp.WithString("file_filter", mcp.Description("Optional glob pattern to filter files")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of matching lines to return (default: 20)")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "regex search across indexed files",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p SearchCodeParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := searchCodeHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("search_code", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("search_code", tool, sdkHandler)
	}
}

// ----- Tool 10: detect_changes -----

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
		if strings.Contains(p.Path, ":") {
			return nil, fmt.Errorf("path contains invalid character ':'")
		}
		// M14: Reject absolute paths to prevent accessing files outside the repo
		if filepath.IsAbs(p.Path) {
			return nil, fmt.Errorf("absolute paths not allowed in path filter: %s", p.Path)
		}
		args = append(args, "--", p.Path)
	}

	gitCtx, gitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer gitCancel()
	cmd := exec.CommandContext(gitCtx, "git", args...)
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
			nextArgs = defaultSymbolNextArgs(nextTool, sc.ID)
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

	// Compact mode: strip verbose fields
	if p.Compact {
		for i := range inspectables {
			inspectables[i] = stripInspectable(inspectables[i])
		}
		summary = ""
	}

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
				"compact": map[string]interface{}{
					"type":        "boolean",
					"description": "Return compact output: IDs, scores, and line spans only",
				},
			},
			"required": []string{"since"},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("detect_changes",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Detect Changes"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("since", mcp.Description("Git ref to compare against (e.g. HEAD~5 or main or a commit hash)"), mcp.Required()),
		mcp.WithString("path", mcp.Description("Optional path filter for changed files")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of ranked changes to return (default: 5)")),
		mcp.WithBoolean("compact", mcp.Description("Return compact output: IDs, scores, and line spans only")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "recent changes ranked by importance",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p DetectChangesParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := detectChangesHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "detect_changes", deps.OutputStore)
	}

	if isToolInProfile("detect_changes", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("detect_changes", tool, sdkHandler)
	}
}

// ----- Tool 11: get_architecture_summary -----

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
			NextArgs:   defaultSymbolNextArgs(nextTool, symbolRef),
		})
	}

	summary := fmt.Sprintf("%d communities (modularity %.2f), %d nodes, %d edges — top %d structural nodes shown",
		len(communities), modularity, deps.Graph.NodeCount(), deps.Graph.EdgeCount(), len(inspectables))

	// Compact mode: strip verbose fields
	if p.Compact {
		for i := range inspectables {
			inspectables[i] = stripInspectable(inspectables[i])
		}
		summary = ""
	}

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
				"compact": map[string]interface{}{
					"type":        "boolean",
					"description": "Return compact output: IDs, scores, and line spans only",
				},
			},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("get_architecture_summary",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Architecture Summary"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithNumber("limit", mcp.Description("Maximum number of structurally important nodes to return (default: 5)")),
		mcp.WithBoolean("compact", mcp.Description("Return compact output: IDs, scores, and line spans only")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "structural overview of the codebase",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ArchitectureSummaryParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := architectureSummaryHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "get_architecture_summary", deps.OutputStore)
	}

	if isToolInProfile("get_architecture_summary", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("get_architecture_summary", tool, sdkHandler)
	}
}

// ----- Tool 12: explore -----

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
			NextArgs:   defaultSymbolNextArgs(nextTool, m.ID),
		})
	}

	totalMatches := len(matches)
	if len(inspectables) > 10 {
		inspectables = inspectables[:10]
	}

	// Compact mode: strip verbose fields from inspectables
	if p.Compact {
		for i := range inspectables {
			inspectables[i] = stripInspectable(inspectables[i])
		}
	}

	summaryText := ""
	if !p.Compact {
		summaryText = fmt.Sprintf("Found %d matches for '%s'", totalMatches, p.Symbol)
	}

	result := map[string]interface{}{
		"inspectables": inspectables,
		"total":        totalMatches,
		"query":        p.Symbol,
		"summary":      summaryText,
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

func listFileSymbolsHandler(deps ToolDeps, p ListFileSymbolsParams) (interface{}, error) {
	if p.Path == "" {
		return nil, fmt.Errorf("'path' is required")
	}
	if p.Limit <= 0 {
		p.Limit = 200
	}
	if p.Limit > 500 {
		p.Limit = 500
	}

	fileCtx, err := loadSymbolFileContext(deps.RepoRoot, p.Path)
	if err != nil {
		return nil, err
	}

	nodes, err := deps.Store.GetNodesByFile(fileCtx.RelPath)
	if err != nil {
		return nil, fmt.Errorf("query indexed symbols for %s: %w", fileCtx.RelPath, err)
	}

	kindFilter := make(map[string]bool)
	for _, kind := range p.Kinds {
		if kind == "" {
			continue
		}
		kind = strings.ToLower(strings.TrimSpace(kind))
		switch kind {
		case "function", "class", "struct", "method", "interface", "file", "route":
			kindFilter[kind] = true
		default:
			return nil, fmt.Errorf("invalid kind %q: must be one of function, class, struct, method, interface, file, route", kind)
		}
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].StartByte == nodes[j].StartByte {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].StartByte < nodes[j].StartByte
	})

	var symbols []map[string]interface{}
	totalMatching := 0
	for _, node := range nodes {
		kind := node.NodeType.String()
		if len(kindFilter) == 0 && node.NodeType == types.NodeTypeFile {
			continue
		}
		if len(kindFilter) > 0 && !kindFilter[kind] {
			continue
		}

		totalMatching++
		if len(symbols) >= p.Limit {
			continue
		}

		inspection := buildSymbolInspection(node, fileCtx)
		symbolRef := node.ID
		if symbolRef == "" {
			symbolRef = node.SymbolName
		}

		symbols = append(symbols, map[string]interface{}{
			"id":               node.ID,
			"symbol_name":      node.SymbolName,
			"node_type":        kind,
			"start_line":       inspection.SymbolStartLine,
			"end_line":         inspection.SymbolEndLine,
			"signature":        inspection.Signature,
			"read_symbol_args": defaultReadSymbolArgs(symbolRef),
		})
	}

	return map[string]interface{}{
		"path":      fileCtx.RelPath,
		"count":     len(symbols),
		"total":     totalMatching,
		"truncated": totalMatching > len(symbols),
		"symbols":   symbols,
	}, nil
}

func registerListFileSymbolsTool(s *Server, deps ToolDeps) {
	desc := "List indexed symbols in a file in source order with safe read_symbol follow-up args. Use instead of grep when you need method or symbol inventory."

	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ListFileSymbolsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return listFileSymbolsHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "list_file_symbols",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Repo-relative or absolute-under-repo file path to inspect",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum symbols to return (default: 200)",
					"default":     200,
				},
				"kinds": map[string]interface{}{
					"type":        "array",
					"description": "Optional node-type filters such as function, method, class, struct, interface, route",
					"items":       map[string]interface{}{"type": "string"},
				},
			},
			"required": []string{"path"},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("list_file_symbols",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("List File Symbols"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("path", mcp.Description("Repo-relative or absolute-under-repo file path to inspect"), mcp.Required()),
		mcp.WithNumber("limit", mcp.Description("Maximum symbols to return (default: 200)")),
		mcp.WithArray("kinds", mcp.Description("Optional node-type filters such as function, method, class, struct, interface, route"), mcp.WithStringItems()),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "inventory of symbols in a file without shell grep",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ListFileSymbolsParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := listFileSymbolsHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("list_file_symbols", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("list_file_symbols", tool, sdkHandler)
	}
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
				"compact": map[string]interface{}{
					"type":        "boolean",
					"description": "Return compact output: IDs, scores, and line spans only",
				},
			},
			"required": []string{"symbol"},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("explore",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Explore Symbol"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("symbol", mcp.Description("Symbol name to search for"), mcp.Required()),
		mcp.WithBoolean("include_deps", mcp.Description("Whether to include dependency/dependent analysis (default: false)")),
		mcp.WithNumber("depth", mcp.Description("Depth for dependency traversal (default: 2)")),
		mcp.WithBoolean("compact", mcp.Description("Return compact output: IDs, scores, and line spans only")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "deep-dive into a specific symbol and its neighbors",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ExploreParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := exploreHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "explore", deps.OutputStore)
	}

	if isToolInProfile("explore", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("explore", tool, sdkHandler)
	}
}

// ----- Tool 13: understand -----

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

	// Compact mode: keep only symbol, callers/callees names+IDs, drop verbose fields
	if p.Compact {
		type compactDetail struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		}
		compact := map[string]interface{}{
			"symbol": found,
		}
		if callers, ok := result["callers"].([]symbolDetail); ok {
			cc := make([]compactDetail, len(callers))
			for i, c := range callers {
				cc[i] = compactDetail{Name: c.Name, ID: c.ID}
			}
			compact["callers"] = cc
		}
		if callees, ok := result["callees"].([]symbolDetail); ok {
			cc := make([]compactDetail, len(callees))
			for i, c := range callees {
				cc[i] = compactDetail{Name: c.Name, ID: c.ID}
			}
			compact["callees"] = cc
		}
		return compact, nil
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
				"compact": map[string]interface{}{
					"type":        "boolean",
					"description": "Return compact output: IDs, scores, and line spans only",
				},
			},
			"required": []string{"symbol"},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("understand",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Understand Symbol"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("symbol", mcp.Description("Symbol name to understand"), mcp.Required()),
		mcp.WithBoolean("compact", mcp.Description("Return compact output: IDs, scores, and line spans only")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "relationships, callers, callees, importance",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p UnderstandParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := understandHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "understand", deps.OutputStore)
	}

	if isToolInProfile("understand", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("understand", tool, sdkHandler)
	}
}

type compactNameID struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type routeResultItem struct {
	ID        string         `json:"id"`
	Symbol    string         `json:"symbol"`
	Method    string         `json:"method"`
	Path      string         `json:"path"`
	FilePath  string         `json:"file_path"`
	Score     float64        `json:"score,omitempty"`
	Handler   map[string]any `json:"handler,omitempty"`
	Summary   string         `json:"summary,omitempty"`
}

type goalCandidate struct {
	Result types.SearchResult
	Group  string
}

func parseRouteSymbol(symbol string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(symbol), " ", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid route symbol %q", symbol)
	}
	return parts[0], parts[1], nil
}

func resolveRouteNode(deps ToolDeps, routeRef string) (*types.ASTNode, error) {
	if routeRef == "" {
		return nil, fmt.Errorf("'route' is required")
	}
	if node, err := deps.Store.GetNode(routeRef); err == nil && node != nil && node.NodeType == types.NodeTypeRoute {
		return node, nil
	}
	if node, err := deps.Store.GetNodeByName(routeRef); err == nil && node != nil && node.NodeType == types.NodeTypeRoute {
		return node, nil
	}
	candidates, err := deps.Store.SearchNodesByName(routeRef)
	if err != nil {
		return nil, err
	}
	for _, node := range candidates {
		if node.NodeType == types.NodeTypeRoute {
			return &node, nil
		}
	}
	return nil, fmt.Errorf("route not found: %s", routeRef)
}

func resolveRouteHandler(deps ToolDeps, routeNode types.ASTNode) (*types.ASTNode, string) {
	edges, err := deps.Store.GetEdgesFrom(routeNode.ID)
	if err != nil {
		return nil, ""
	}
	for _, edge := range edges {
		if edge.EdgeType != types.EdgeTypeHandles {
			continue
		}
		if edge.TargetID != "" {
			if node, err := deps.Store.GetNode(edge.TargetID); err == nil && node != nil {
				return node, edge.TargetSymbol
			}
		}
		if edge.TargetSymbol != "" {
			if node, err := deps.Store.GetNodeByName(edge.TargetSymbol); err == nil && node != nil {
				return node, edge.TargetSymbol
			}
		}
		return nil, edge.TargetSymbol
	}
	return nil, ""
}

func nodeSummary(node *types.ASTNode) map[string]any {
	if node == nil {
		return nil
	}
	return map[string]any{
		"id":        node.ID,
		"name":      node.SymbolName,
		"file_path": node.FilePath,
		"node_type": node.NodeType.String(),
	}
}

func routeItemFromResult(deps ToolDeps, node types.ASTNode, score float64, includeHandler bool, compact bool) routeResultItem {
	method, path, _ := parseRouteSymbol(node.SymbolName)
	item := routeResultItem{
		ID:       node.ID,
		Symbol:   node.SymbolName,
		Method:   method,
		Path:     path,
		FilePath: node.FilePath,
		Score:    score,
	}
	if includeHandler {
		if handler, unresolved := resolveRouteHandler(deps, node); handler != nil {
			item.Handler = nodeSummary(handler)
		} else if unresolved != "" {
			item.Handler = map[string]any{"name": unresolved}
		}
	}
	if !compact {
		item.Summary = fmt.Sprintf("%s route in %s", node.SymbolName, node.FilePath)
	}
	return item
}

func routeHandlersEnabled(flag *bool) bool {
	return flag == nil || *flag
}

func applyCompactSummary(result map[string]any, compact bool) map[string]any {
	if compact {
		delete(result, "summary")
	}
	return result
}

func appendGoalCandidate(out []goalCandidate, seen map[string]bool, node *types.ASTNode, score float64, group string) []goalCandidate {
	if node == nil || seen[node.ID] {
		return out
	}
	seen[node.ID] = true
	return append(out, goalCandidate{
		Result: types.SearchResult{Node: *node, Score: score},
		Group:  group,
	})
}

func uniqueNodeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	var out []string
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func collectNeighbors(graphEngine *graph.GraphEngine, roots []string, depth int, mode string) []string {
	if graphEngine == nil || depth <= 0 {
		return nil
	}
	current := uniqueNodeIDs(roots)
	seen := make(map[string]bool, len(current))
	for _, id := range current {
		seen[id] = true
	}
	var out []string
	for level := 0; level < depth; level++ {
		var next []string
		for _, id := range current {
			var neighbors []string
			if mode == "callers" {
				neighbors = graphEngine.GetCallers(id)
			} else {
				neighbors = graphEngine.GetCallees(id)
			}
			for _, neighbor := range neighbors {
				if seen[neighbor] {
					continue
				}
				seen[neighbor] = true
				next = append(next, neighbor)
				out = append(out, neighbor)
			}
		}
		current = next
		if len(current) == 0 {
			break
		}
	}
	return out
}

func summarizeIDs(store *storage.Store, ids []string) []compactNameID {
	ids = uniqueNodeIDs(ids)
	out := make([]compactNameID, 0, len(ids))
	for _, id := range ids {
		node, err := store.GetNode(id)
		if err != nil || node == nil {
			continue
		}
		out = append(out, compactNameID{Name: node.SymbolName, ID: node.ID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func symbolSignature(repoRoot string, node types.ASTNode) string {
	fileCtx, err := loadSymbolFileContext(repoRoot, node.FilePath)
	if err != nil {
		return ""
	}
	return buildSymbolInspection(node, fileCtx).Signature
}

func compareSymbolsHandler(deps ToolDeps, p CompareSymbolsParams) (interface{}, error) {
	leftNode, err := resolveSymbolNode(deps, p.Left)
	if err != nil {
		return nil, err
	}
	rightNode, err := resolveSymbolNode(deps, p.Right)
	if err != nil {
		return nil, err
	}
	if p.Depth <= 0 {
		p.Depth = 1
	}
	if p.Depth > 3 {
		p.Depth = 3
	}

	leftCallers := collectNeighbors(deps.Graph, []string{leftNode.ID}, p.Depth, "callers")
	rightCallers := collectNeighbors(deps.Graph, []string{rightNode.ID}, p.Depth, "callers")
	leftCallees := collectNeighbors(deps.Graph, []string{leftNode.ID}, p.Depth, "callees")
	rightCallees := collectNeighbors(deps.Graph, []string{rightNode.ID}, p.Depth, "callees")

	toSet := func(ids []string) map[string]bool {
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return set
	}
	intersect := func(left, right []string) []string {
		rightSet := toSet(right)
		var out []string
		for _, id := range uniqueNodeIDs(left) {
			if rightSet[id] {
				out = append(out, id)
			}
		}
		return out
	}
	diff := func(left, right []string) []string {
		rightSet := toSet(right)
		var out []string
		for _, id := range uniqueNodeIDs(left) {
			if !rightSet[id] {
				out = append(out, id)
			}
		}
		return out
	}

	result := map[string]any{
		"left":              nodeSummary(leftNode),
		"right":             nodeSummary(rightNode),
		"left_signature":    symbolSignature(deps.RepoRoot, *leftNode),
		"right_signature":   symbolSignature(deps.RepoRoot, *rightNode),
		"shared_callers":    summarizeIDs(deps.Store, intersect(leftCallers, rightCallers)),
		"left_only_callers": summarizeIDs(deps.Store, diff(leftCallers, rightCallers)),
		"right_only_callers": summarizeIDs(deps.Store, diff(rightCallers, leftCallers)),
		"shared_callees":     summarizeIDs(deps.Store, intersect(leftCallees, rightCallees)),
		"left_only_callees":  summarizeIDs(deps.Store, diff(leftCallees, rightCallees)),
		"right_only_callees": summarizeIDs(deps.Store, diff(rightCallees, leftCallees)),
		"summary": fmt.Sprintf("Compared %s and %s across callers/callees at depth %d",
			leftNode.SymbolName, rightNode.SymbolName, p.Depth),
	}
	if p.Compact {
		delete(result, "summary")
	}
	return result, nil
}

func findRoutesHandler(deps ToolDeps, p FindRoutesParams) (interface{}, error) {
	if strings.TrimSpace(p.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if p.Limit <= 0 {
		p.Limit = 10
	}
	includeHandlers := routeHandlersEnabled(p.IncludeHandlers)

	var items []routeResultItem
	seen := make(map[string]bool)
	addNode := func(node types.ASTNode, score float64) {
		if node.NodeType != types.NodeTypeRoute || seen[node.ID] {
			return
		}
		item := routeItemFromResult(deps, node, score, includeHandlers, p.Compact)
		if p.Method != "" && !strings.EqualFold(item.Method, p.Method) {
			return
		}
		seen[node.ID] = true
		items = append(items, item)
	}

	if exact, err := deps.Store.SearchNodesByName(p.Query); err == nil {
		for _, node := range exact {
			addNode(node, 1.5)
		}
	}
	if deps.Search != nil {
		if results, err := deps.Search.Search(p.Query, p.Limit*4, nil, 2); err == nil {
			for _, result := range results {
				addNode(result.Node, result.Score)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	if len(items) > p.Limit {
		items = items[:p.Limit]
	}
	return applyCompactSummary(map[string]any{
		"query":            p.Query,
		"method_filter":    strings.ToUpper(p.Method),
		"count":            len(items),
		"routes":           items,
		"include_handlers": includeHandlers,
		"summary":          fmt.Sprintf("Found %d matching routes for %q", len(items), p.Query),
	}, p.Compact), nil
}

func traceRouteHandler(deps ToolDeps, p TraceRouteParams) (interface{}, error) {
	routeNode, err := resolveRouteNode(deps, p.Route)
	if err != nil {
		return nil, err
	}
	if p.Depth <= 0 {
		p.Depth = 2
	}
	if p.Depth > 4 {
		p.Depth = 4
	}
	handlerNode, unresolved := resolveRouteHandler(deps, *routeNode)
	result := map[string]any{
		"route":   routeItemFromResult(deps, *routeNode, 1, true, p.Compact),
		"summary": fmt.Sprintf("Traced route %s", routeNode.SymbolName),
	}
	if handlerNode != nil {
		result["handler"] = nodeSummary(handlerNode)
	} else if unresolved != "" {
		result["handler"] = map[string]any{"name": unresolved}
	}

	var layers []map[string]any
	current := []string{}
	if handlerNode != nil {
		current = append(current, handlerNode.ID)
	}
	seen := make(map[string]bool)
	for depth := 1; depth <= p.Depth && len(current) > 0; depth++ {
		next := uniqueNodeIDs(collectNeighbors(deps.Graph, current, 1, "callees"))
		var symbols []compactNameID
		for _, id := range next {
			if seen[id] {
				continue
			}
			seen[id] = true
			if node, err := deps.Store.GetNode(id); err == nil && node != nil {
				symbols = append(symbols, compactNameID{Name: node.SymbolName, ID: node.ID})
			}
		}
		if len(symbols) > 0 {
			layers = append(layers, map[string]any{"depth": depth, "symbols": symbols})
		}
		current = next
	}
	result["layers"] = layers
	return applyCompactSummary(result, p.Compact), nil
}

func compareRoutesHandler(deps ToolDeps, p CompareRoutesParams) (interface{}, error) {
	leftNode, err := resolveRouteNode(deps, p.Left)
	if err != nil {
		return nil, err
	}
	rightNode, err := resolveRouteNode(deps, p.Right)
	if err != nil {
		return nil, err
	}
	if p.Depth <= 0 {
		p.Depth = 2
	}
	if p.Depth > 4 {
		p.Depth = 4
	}
	leftMethod, leftPath, _ := parseRouteSymbol(leftNode.SymbolName)
	rightMethod, rightPath, _ := parseRouteSymbol(rightNode.SymbolName)
	leftHandler, _ := resolveRouteHandler(deps, *leftNode)
	rightHandler, _ := resolveRouteHandler(deps, *rightNode)
	leftDownstream := []string{}
	rightDownstream := []string{}
	if leftHandler != nil {
		leftDownstream = collectNeighbors(deps.Graph, []string{leftHandler.ID}, p.Depth, "callees")
	}
	if rightHandler != nil {
		rightDownstream = collectNeighbors(deps.Graph, []string{rightHandler.ID}, p.Depth, "callees")
	}
	toSet := func(ids []string) map[string]bool {
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return set
	}
	diff := func(left, right []string) []string {
		rightSet := toSet(right)
		var out []string
		for _, id := range uniqueNodeIDs(left) {
			if !rightSet[id] {
				out = append(out, id)
			}
		}
		return out
	}
	shared := func(left, right []string) []string {
		rightSet := toSet(right)
		var out []string
		for _, id := range uniqueNodeIDs(left) {
			if rightSet[id] {
				out = append(out, id)
			}
		}
		return out
	}
	result := map[string]any{
		"left":                   routeItemFromResult(deps, *leftNode, 1, true, p.Compact),
		"right":                  routeItemFromResult(deps, *rightNode, 1, true, p.Compact),
		"same_method":            leftMethod == rightMethod,
		"left_path":              leftPath,
		"right_path":             rightPath,
		"left_handler":           nodeSummary(leftHandler),
		"right_handler":          nodeSummary(rightHandler),
		"shared_downstream":      summarizeIDs(deps.Store, shared(leftDownstream, rightDownstream)),
		"left_only_downstream":   summarizeIDs(deps.Store, diff(leftDownstream, rightDownstream)),
		"right_only_downstream":  summarizeIDs(deps.Store, diff(rightDownstream, leftDownstream)),
		"summary":                fmt.Sprintf("Compared routes %s and %s", leftNode.SymbolName, rightNode.SymbolName),
	}
	return applyCompactSummary(result, p.Compact), nil
}

func registerCompareSymbolsTool(s *Server, deps ToolDeps) {
	desc := "Compare two symbols by signatures, callers, and callees."
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p CompareSymbolsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return compareSymbolsHandler(deps, p)
	}
	s.RegisterTool(ToolDefinition{
		Name:        "compare_symbols",
		Description: desc,
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"left": map[string]interface{}{"type": "string"},
			"right": map[string]interface{}{"type": "string"},
			"depth": map[string]interface{}{"type": "integer", "default": 1},
			"compact": map[string]interface{}{"type": "boolean"},
		}, "required": []string{"left", "right"}},
	}, cliHandler)
	tool := mcp.NewTool("compare_symbols",
		mcp.WithDescription(desc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("left", mcp.Required()),
		mcp.WithString("right", mcp.Required()),
		mcp.WithNumber("depth"),
		mcp.WithBoolean("compact"),
	)
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p CompareSymbolsParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := compareSymbolsHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "compare_symbols", deps.OutputStore)
	}
	if isToolInProfile("compare_symbols", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("compare_symbols", tool, sdkHandler)
	}
}

func registerFindRoutesTool(s *Server, deps ToolDeps) {
	desc := "Find normalized route nodes and optionally resolve their handlers."
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p FindRoutesParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return findRoutesHandler(deps, p)
	}
	s.RegisterTool(ToolDefinition{
		Name:        "find_routes",
		Description: desc,
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"query": map[string]interface{}{"type": "string"},
			"method": map[string]interface{}{"type": "string"},
			"limit": map[string]interface{}{"type": "integer", "default": 10},
			"include_handlers": map[string]interface{}{"type": "boolean", "default": true},
			"compact": map[string]interface{}{"type": "boolean"},
		}, "required": []string{"query"}},
	}, cliHandler)
	tool := mcp.NewTool("find_routes",
		mcp.WithDescription(desc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("query", mcp.Required()),
		mcp.WithString("method"),
		mcp.WithNumber("limit"),
		mcp.WithBoolean("include_handlers"),
		mcp.WithBoolean("compact"),
	)
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p FindRoutesParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := findRoutesHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "find_routes", deps.OutputStore)
	}
	if isToolInProfile("find_routes", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("find_routes", tool, sdkHandler)
	}
}

func registerTraceRouteTool(s *Server, deps ToolDeps) {
	desc := "Trace a route to its handler and downstream callees."
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p TraceRouteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return traceRouteHandler(deps, p)
	}
	s.RegisterTool(ToolDefinition{
		Name:        "trace_route",
		Description: desc,
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"route": map[string]interface{}{"type": "string"},
			"depth": map[string]interface{}{"type": "integer", "default": 2},
			"compact": map[string]interface{}{"type": "boolean"},
		}, "required": []string{"route"}},
	}, cliHandler)
	tool := mcp.NewTool("trace_route",
		mcp.WithDescription(desc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("route", mcp.Required()),
		mcp.WithNumber("depth"),
		mcp.WithBoolean("compact"),
	)
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p TraceRouteParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := traceRouteHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "trace_route", deps.OutputStore)
	}
	if isToolInProfile("trace_route", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("trace_route", tool, sdkHandler)
	}
}

func registerCompareRoutesTool(s *Server, deps ToolDeps) {
	desc := "Compare two routes by path, handler, and downstream symbols."
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p CompareRoutesParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return compareRoutesHandler(deps, p)
	}
	s.RegisterTool(ToolDefinition{
		Name:        "compare_routes",
		Description: desc,
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"left": map[string]interface{}{"type": "string"},
			"right": map[string]interface{}{"type": "string"},
			"depth": map[string]interface{}{"type": "integer", "default": 2},
			"compact": map[string]interface{}{"type": "boolean"},
		}, "required": []string{"left", "right"}},
	}, cliHandler)
	tool := mcp.NewTool("compare_routes",
		mcp.WithDescription(desc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("left", mcp.Required()),
		mcp.WithString("right", mcp.Required()),
		mcp.WithNumber("depth"),
		mcp.WithBoolean("compact"),
	)
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p CompareRoutesParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := compareRoutesHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "compare_routes", deps.OutputStore)
	}
	if isToolInProfile("compare_routes", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("compare_routes", tool, sdkHandler)
	}
}

// ----- Tool 14: assemble_context -----

// estimateTokens returns a rough token count for a string (~4 chars per token).
func estimateTokens(text string) int {
	return len(text) / 4
}

// readNodeSource reads the byte range [StartByte, EndByte) from a node's file.
// Returns "" on any error. Caps file size at 5MB to avoid reading huge files.
func readNodeSource(repoRoot string, node types.ASTNode) string {
	const maxFileSize = 5 * 1024 * 1024 // 5MB

	filePath := filepath.Join(repoRoot, node.FilePath)

	// Validate path stays within repo root to prevent path traversal
	realRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return ""
	}
	realPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return ""
	}
	if realPath != realRoot && !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) {
		return ""
	}

	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() > maxFileSize {
		return ""
	}

	start := node.StartByte
	end := node.EndByte
	if end <= start || int64(end) > info.Size() {
		return ""
	}

	buf := make([]byte, end-start)
	n, err := f.ReadAt(buf, int64(start))
	if err != nil && n == 0 {
		return ""
	}
	return string(buf[:n])
}

func collectGoalAwareCandidates(deps ToolDeps, p AssembleContextParams) ([]goalCandidate, string, error) {
	switch p.Goal {
	case "":
		return nil, "", nil
	case "inspect_symbol":
		if len(p.Targets) == 0 {
			return nil, "", fmt.Errorf("targets[0] is required for goal=inspect_symbol")
		}
		node, err := resolveSymbolNode(deps, p.Targets[0])
		if err != nil {
			return nil, "", err
		}
		seen := map[string]bool{}
		results := appendGoalCandidate(nil, seen, node, 1, "target")
		if p.IncludeNeighbors && deps.Graph != nil {
			for _, id := range collectNeighbors(deps.Graph, []string{node.ID}, 1, "callees") {
				if neighbor, err := deps.Store.GetNode(id); err == nil && neighbor != nil {
					results = appendGoalCandidate(results, seen, neighbor, 0.7, "neighbor")
				}
			}
		}
		return results, "goal=inspect_symbol target_then_neighbors", nil
	case "compare_symbols":
		if len(p.Targets) < 2 {
			return nil, "", fmt.Errorf("two targets are required for goal=compare_symbols")
		}
		left, err := resolveSymbolNode(deps, p.Targets[0])
		if err != nil {
			return nil, "", err
		}
		right, err := resolveSymbolNode(deps, p.Targets[1])
		if err != nil {
			return nil, "", err
		}
		seen := map[string]bool{}
		results := appendGoalCandidate(nil, seen, left, 1, "left")
		results = appendGoalCandidate(results, seen, right, 1, "right")
		for _, id := range collectNeighbors(deps.Graph, []string{left.ID}, 1, "callees") {
			if node, err := deps.Store.GetNode(id); err == nil && node != nil {
				results = appendGoalCandidate(results, seen, node, 0.7, "left_callee")
			}
		}
		for _, id := range collectNeighbors(deps.Graph, []string{right.ID}, 1, "callees") {
			if node, err := deps.Store.GetNode(id); err == nil && node != nil {
				results = appendGoalCandidate(results, seen, node, 0.7, "right_callee")
			}
		}
		for _, id := range collectNeighbors(deps.Graph, []string{left.ID}, 1, "callers") {
			if node, err := deps.Store.GetNode(id); err == nil && node != nil {
				results = appendGoalCandidate(results, seen, node, 0.55, "left_caller")
			}
		}
		for _, id := range collectNeighbors(deps.Graph, []string{right.ID}, 1, "callers") {
			if node, err := deps.Store.GetNode(id); err == nil && node != nil {
				results = appendGoalCandidate(results, seen, node, 0.55, "right_caller")
			}
		}
		return results, "goal=compare_symbols balanced_targets_and_neighbors", nil
	case "find_routes":
		routes, err := findRoutesHandler(deps, FindRoutesParams{
			Query: p.Query,
			Limit: 10,
		})
		if err != nil {
			return nil, "", err
		}
		payload, ok := routes.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("unexpected find_routes response type %T", routes)
		}
		rawRoutes, _ := payload["routes"].([]routeResultItem)
		seen := map[string]bool{}
		var results []goalCandidate
		for _, item := range rawRoutes {
			if node, err := deps.Store.GetNode(item.ID); err == nil && node != nil {
				results = appendGoalCandidate(results, seen, node, item.Score, "route")
			}
		}
		return results, "goal=find_routes routes_first", nil
	case "trace_route":
		if len(p.Targets) == 0 {
			return nil, "", fmt.Errorf("targets[0] is required for goal=trace_route")
		}
		routeNode, err := resolveRouteNode(deps, p.Targets[0])
		if err != nil {
			return nil, "", err
		}
		seen := map[string]bool{}
		results := appendGoalCandidate(nil, seen, routeNode, 1, "route")
		if handler, _ := resolveRouteHandler(deps, *routeNode); handler != nil {
			results = appendGoalCandidate(results, seen, handler, 0.9, "handler")
			for _, id := range collectNeighbors(deps.Graph, []string{handler.ID}, 1, "callees") {
				if node, err := deps.Store.GetNode(id); err == nil && node != nil {
					results = appendGoalCandidate(results, seen, node, 0.7, "trace_depth_1")
				}
			}
			for _, id := range collectNeighbors(deps.Graph, []string{handler.ID}, 2, "callees") {
				if node, err := deps.Store.GetNode(id); err == nil && node != nil {
					results = appendGoalCandidate(results, seen, node, 0.55, "trace_depth_2")
				}
			}
		}
		return results, "goal=trace_route route_then_handler_then_trace", nil
	case "compare_routes":
		if len(p.Targets) < 2 {
			return nil, "", fmt.Errorf("two targets are required for goal=compare_routes")
		}
		left, err := resolveRouteNode(deps, p.Targets[0])
		if err != nil {
			return nil, "", err
		}
		right, err := resolveRouteNode(deps, p.Targets[1])
		if err != nil {
			return nil, "", err
		}
		seen := map[string]bool{}
		results := appendGoalCandidate(nil, seen, left, 1, "left_route")
		results = appendGoalCandidate(results, seen, right, 1, "right_route")
		if handler, _ := resolveRouteHandler(deps, *left); handler != nil {
			results = appendGoalCandidate(results, seen, handler, 0.9, "left_handler")
			for _, id := range collectNeighbors(deps.Graph, []string{handler.ID}, 1, "callees") {
				if node, err := deps.Store.GetNode(id); err == nil && node != nil {
					results = appendGoalCandidate(results, seen, node, 0.7, "left_downstream")
				}
			}
		}
		if handler, _ := resolveRouteHandler(deps, *right); handler != nil {
			results = appendGoalCandidate(results, seen, handler, 0.9, "right_handler")
			for _, id := range collectNeighbors(deps.Graph, []string{handler.ID}, 1, "callees") {
				if node, err := deps.Store.GetNode(id); err == nil && node != nil {
					results = appendGoalCandidate(results, seen, node, 0.7, "right_downstream")
				}
			}
		}
		return results, "goal=compare_routes balanced_routes_handlers_and_downstream", nil
	default:
		return nil, "", fmt.Errorf("invalid goal %q", p.Goal)
	}
}

// assembleContextHandler implements the greedy budget selector for assemble_context.
func assembleContextHandler(deps ToolDeps, p AssembleContextParams) (interface{}, error) {
	if p.Query == "" && p.Task != "" {
		p.Query = p.Task
	}
	if p.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// Apply defaults
	if p.BudgetTokens == 0 {
		p.BudgetTokens = 4000
	}
	if p.BudgetTokens > 100000 {
		p.BudgetTokens = 100000
	}
	if p.Mode == "" {
		p.Mode = "snippets"
	}
	if p.MaxPerFile == 0 {
		p.MaxPerFile = 2
	}

	// Validate mode
	validModes := map[string]bool{
		"summary": true, "signatures": true, "snippets": true, "bundle": true, "full": true,
	}
	if !validModes[p.Mode] {
		return nil, fmt.Errorf("invalid mode %q: must be one of summary, signatures, snippets, bundle, full", p.Mode)
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

	var strategy string
	goalResults, strategy, err := collectGoalAwareCandidates(deps, p)
	if err != nil {
		return nil, err
	}
	resultGroups := make(map[string]string, len(goalResults))
	results := make([]types.SearchResult, 0, len(goalResults))
	for _, candidate := range goalResults {
		results = append(results, candidate.Result)
		if candidate.Group != "" {
			resultGroups[candidate.Result.Node.ID] = candidate.Group
		}
	}
	if len(results) == 0 {
		results, err = deps.Search.Search(p.Query, 50, activeFileNodeIDs, p.MaxPerFile)
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
		if strategy == "" {
			strategy = "ranked_search"
		}
	}

	// Build items within budget
	var items []AssembleContextItem
	usedTokens := 0
	excluded := 0
	rank := 0

	consecutiveSkips := 0
	for i, r := range results {
		content := generateModeContent(deps.RepoRoot, r.Node, p.Mode)
		tokens := estimateTokens(content)

		if usedTokens+tokens > p.BudgetTokens {
			excluded++
			consecutiveSkips++
			if consecutiveSkips > 5 {
				excluded += len(results) - i - 1
				break
			}
			continue
		}
		consecutiveSkips = 0

		rank++
		items = append(items, AssembleContextItem{
			Rank:     rank,
			Name:     r.Node.SymbolName,
			FilePath: r.Node.FilePath,
			ID:       r.Node.ID,
			Score:    r.Score,
			Content:  content,
			Tokens:   tokens,
			Reason:   generateReasonFromBreakdown(r.Breakdown),
			Group:    resultGroups[r.Node.ID],
		})
		usedTokens += tokens
	}

	// If include_neighbors, add callers/callees of top results
	if p.IncludeNeighbors && deps.Graph != nil && len(items) > 0 {
		seen := make(map[string]bool)
		for _, item := range items {
			seen[item.ID] = true
		}

		// Collect neighbor IDs from top 5 items
		limit := 5
		if len(items) < limit {
			limit = len(items)
		}
		var neighborIDs []string
		for _, item := range items[:limit] {
			callers := deps.Graph.GetCallers(item.ID)
			callees := deps.Graph.GetCallees(item.ID)
			neighborIDs = append(neighborIDs, callers...)
			neighborIDs = append(neighborIDs, callees...)
		}

		for _, nID := range neighborIDs {
			if seen[nID] {
				continue
			}
			seen[nID] = true

			node, err := deps.Store.GetNode(nID)
			if err != nil || node == nil {
				continue
			}

			content := generateModeContent(deps.RepoRoot, *node, p.Mode)
			tokens := estimateTokens(content)

			if usedTokens+tokens > p.BudgetTokens {
				excluded++
				continue
			}

			rank++
			items = append(items, AssembleContextItem{
				Rank:     rank,
				Name:     node.SymbolName,
				FilePath: node.FilePath,
				ID:       node.ID,
				Score:    0,
				Content:  content,
				Tokens:   tokens,
				Reason:   "neighbor (caller/callee)",
				Group:    "neighbor",
			})
			usedTokens += tokens
		}
	}

	summary := fmt.Sprintf("Assembled %d items (%d tokens / %d budget) for %q [mode=%s]",
		len(items), usedTokens, p.BudgetTokens, p.Query, p.Mode)
	if excluded > 0 {
		summary += fmt.Sprintf(", %d excluded (over budget)", excluded)
	}

	// Compact mode: truncate Content to first line (max 120 chars), drop Reason
	if p.Compact {
		for i := range items {
			items[i].Reason = ""
			content := items[i].Content
			if idx := strings.IndexByte(content, '\n'); idx >= 0 {
				content = content[:idx]
			}
			if len(content) > 120 {
				content = content[:120]
			}
			items[i].Content = content
		}
	}

	return AssembleContextResponse{
		Query:        p.Query,
		Mode:         p.Mode,
		BudgetTokens: p.BudgetTokens,
		UsedTokens:   usedTokens,
		Items:        items,
		Excluded:     excluded,
		Summary:      summary,
		Strategy:     strategy,
	}, nil
}

// generateModeContent produces content for a node based on the output fidelity mode.
func generateModeContent(repoRoot string, node types.ASTNode, mode string) string {
	switch mode {
	case "summary":
		s := node.ContentSum
		if s == "" {
			s = fmt.Sprintf("%s %s in %s", node.NodeType.String(), node.SymbolName, node.FilePath)
		}
		return s
	case "signatures":
		src := readNodeSource(repoRoot, node)
		if src == "" {
			return fmt.Sprintf("%s %s", node.NodeType.String(), node.SymbolName)
		}
		// Extract first line (typically the signature)
		firstLine := src
		if idx := strings.IndexByte(src, '\n'); idx >= 0 {
			firstLine = src[:idx]
		}
		return strings.TrimSpace(firstLine)
	case "snippets":
		src := readNodeSource(repoRoot, node)
		if src == "" {
			// Fallback to content_sum
			if node.ContentSum != "" {
				return node.ContentSum
			}
			return fmt.Sprintf("%s %s in %s", node.NodeType.String(), node.SymbolName, node.FilePath)
		}
		// Cap snippets at ~50 lines
		lines := strings.SplitN(src, "\n", 51)
		if len(lines) > 50 {
			return strings.Join(lines[:50], "\n") + "\n// ... (truncated)"
		}
		return src
	case "bundle":
		src := readNodeSource(repoRoot, node)
		if src == "" {
			if node.ContentSum != "" {
				return node.ContentSum
			}
			return fmt.Sprintf("%s %s in %s", node.NodeType.String(), node.SymbolName, node.FilePath)
		}
		// Bundle: full source with file header
		return fmt.Sprintf("// %s:%s\n%s", node.FilePath, node.SymbolName, src)
	case "full":
		src := readNodeSource(repoRoot, node)
		if src == "" {
			if node.ContentSum != "" {
				return node.ContentSum
			}
			return fmt.Sprintf("%s %s in %s", node.NodeType.String(), node.SymbolName, node.FilePath)
		}
		return src
	default:
		return node.ContentSum
	}
}

func registerAssembleContextTool(s *Server, deps ToolDeps) {
	desc := "Token-budgeted context assembly. Returns ranked code snippets fitted within a token budget. Use when you need to gather context efficiently within a token limit."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p AssembleContextParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return assembleContextHandler(deps, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "assemble_context",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":             map[string]interface{}{"type": "string", "description": "Search query to find relevant code"},
				"task":              map[string]interface{}{"type": "string", "description": "Deprecated alias for query"},
				"budget_tokens":     map[string]interface{}{"type": "integer", "description": "Maximum token budget (default: 4000)", "default": 4000},
				"mode":              map[string]interface{}{"type": "string", "description": "Output fidelity: summary, signatures, snippets, bundle, or full (default: snippets)", "default": "snippets"},
				"active_files":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "File paths currently being edited"},
				"max_per_file":      map[string]interface{}{"type": "integer", "description": "Maximum results per file (default: 2)", "default": 2},
				"include_neighbors": map[string]interface{}{"type": "boolean", "description": "Include callers/callees of top results"},
				"compact":           map[string]interface{}{"type": "boolean", "description": "Return compact output: IDs, scores, and line spans only"},
				"goal":              map[string]interface{}{"type": "string", "description": "Optional task shape: inspect_symbol, compare_symbols, find_routes, trace_route, compare_routes"},
				"targets":           map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional goal-specific route/symbol refs"},
			},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("assemble_context",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Assemble Context"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("query", mcp.Description("Search query to find relevant code")),
		mcp.WithString("task", mcp.Description("Deprecated alias for query")),
		mcp.WithNumber("budget_tokens", mcp.Description("Maximum token budget (default: 4000)")),
		mcp.WithString("mode", mcp.Description("Output fidelity: summary, signatures, snippets, bundle, or full (default: snippets)")),
		mcp.WithArray("active_files", mcp.Description("File paths currently being edited"), mcp.WithStringItems()),
		mcp.WithNumber("max_per_file", mcp.Description("Maximum results per file (default: 2)")),
		mcp.WithBoolean("include_neighbors", mcp.Description("Include callers/callees of top results")),
		mcp.WithBoolean("compact", mcp.Description("Return compact output: IDs, scores, and line spans only")),
		mcp.WithString("goal", mcp.Description("Optional task shape: inspect_symbol, compare_symbols, find_routes, trace_route, compare_routes")),
		mcp.WithArray("targets", mcp.Description("Optional goal-specific route/symbol refs"), mcp.WithStringItems()),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "token-budgeted context assembly for efficient retrieval",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p AssembleContextParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := assembleContextHandler(deps, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResultWithName(result, "assemble_context", deps.OutputStore)
	}

	if isToolInProfile("assemble_context", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("assemble_context", tool, sdkHandler)
	}
}

// ----- Tool 15: checkpoint_context -----

func registerCheckpointContextTool(s *Server, deps ToolDeps) {
	desc := "Create a named checkpoint of the current index state. Use with read_delta to see what changed since this point."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p CheckpointContextParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		if deps.Checkpoints == nil {
			return nil, fmt.Errorf("checkpoint store not initialized")
		}
		cp, err := deps.Checkpoints.CreateCheckpoint(p.Name, deps.RepoRoot, deps.Store)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"name":        cp.Name,
			"timestamp":   cp.Timestamp.Format(time.RFC3339),
			"head_commit": cp.HeadCommit,
			"node_count":  cp.NodeCount,
			"file_count":  cp.FileCount,
		}, nil
	}

	s.RegisterTool(ToolDefinition{
		Name:        "checkpoint_context",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Checkpoint name (auto-generated if empty)"},
			},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("checkpoint_context",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Checkpoint Context"),
		mcp.WithString("name", mcp.Description("Checkpoint name (auto-generated if empty)")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "snapshot current state; use with read_delta to track changes",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p CheckpointContextParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		if deps.Checkpoints == nil {
			return mcp.NewToolResultError("checkpoint store not initialized"), nil
		}
		cp, err := deps.Checkpoints.CreateCheckpoint(p.Name, deps.RepoRoot, deps.Store)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		result := map[string]interface{}{
			"name":        cp.Name,
			"timestamp":   cp.Timestamp.Format(time.RFC3339),
			"head_commit": cp.HeadCommit,
			"node_count":  cp.NodeCount,
			"file_count":  cp.FileCount,
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("checkpoint_context", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("checkpoint_context", tool, sdkHandler)
	}
}

// ----- Tool 16: read_delta -----

func registerReadDeltaTool(s *Server, deps ToolDeps) {
	desc := "Compare current index state against a named checkpoint. Shows added, modified, and deleted symbols since the checkpoint was created."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ReadDeltaParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		if p.Since == "" {
			return nil, fmt.Errorf("'since' parameter is required")
		}
		if deps.Checkpoints == nil {
			return nil, fmt.Errorf("checkpoint store not initialized")
		}
		return deps.Checkpoints.ComputeDelta(p.Since, deps.RepoRoot, deps.Store, p.Path, p.Limit)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "read_delta",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"since": map[string]interface{}{"type": "string", "description": "Checkpoint name to compare against"},
				"path":  map[string]interface{}{"type": "string", "description": "Filter by file path prefix"},
				"limit": map[string]interface{}{"type": "integer", "description": "Maximum items per change type (default: 20)", "default": 20},
			},
			"required": []string{"since"},
		},
	}, cliHandler)

	// Always build the SDK tool
	tool := mcp.NewTool("read_delta",
		mcp.WithDescription(desc),
		mcp.WithTitleAnnotation("Read Delta"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("since", mcp.Required(), mcp.Description("Checkpoint name to compare against")),
		mcp.WithString("path", mcp.Description("Filter by file path prefix")),
		mcp.WithNumber("limit", mcp.Description("Maximum items per change type (default: 20)")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "diff index state against a checkpoint; shows added/modified/deleted symbols",
		},
	}
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ReadDeltaParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		if p.Since == "" {
			return mcp.NewToolResultError("'since' parameter is required"), nil
		}
		if deps.Checkpoints == nil {
			return mcp.NewToolResultError("checkpoint store not initialized"), nil
		}
		result, err := deps.Checkpoints.ComputeDelta(p.Since, deps.RepoRoot, deps.Store, p.Path, p.Limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("read_delta", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("read_delta", tool, sdkHandler)
	}
}

// ----- Helpers -----

// toCallToolResult converts a generic result (string or anything JSON-serializable)
// into the SDK's CallToolResult format.
func toCallToolResult(result interface{}) (*mcp.CallToolResult, error) {
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
			const suffix = "\n... [truncated, response exceeded 1MB]"
			budget := maxResponseSize - len(suffix)
			if budget < 0 {
				budget = 0
			}
			truncated := text[:budget]
			for !utf8.ValidString(truncated) && len(truncated) > 0 {
				truncated = truncated[:len(truncated)-1]
			}
			text = truncated + suffix
		}
	}
	return mcp.NewToolResultText(text), nil
}

// Tiered output protection thresholds.
const (
	warnThreshold    = 8 * 1024  // 8KB — log warning
	sandboxThreshold = 16 * 1024 // 16KB — auto-sandbox
)

// recoveryHints provides tool-specific suggestions for avoiding large responses.
var recoveryHints = map[string]string{
	"understand":               "Try compact=true to reduce output size",
	"impact":                   "Try compact=true or reduce depth",
	"assemble_context":         "Try a smaller budget_tokens or mode=signatures",
	"get_architecture_summary": "Try compact=true or a smaller limit",
	"context":                  "Try compact=true or a smaller limit",
	"read_symbol":              "Try mode=bounded or mode=signature instead of full",
	"explore":                  "Try compact=true or include_deps=false",
}

// toCallToolResultWithName adds tiered output protection (warning at 8KB,
// sandbox at 16KB) before delegating to toCallToolResult.
func toCallToolResultWithName(result interface{}, toolName string, store *OutputStore) (*mcp.CallToolResult, error) {
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

	if len(text) > warnThreshold {
		log.Printf("[output] %s response: %d bytes (threshold: %d)", toolName, len(text), sandboxThreshold)
	}
	if len(text) > sandboxThreshold && store != nil {
		handle, err := store.Store(toolName, []byte(text))
		if err != nil {
			log.Printf("[output] failed to store sandbox: %v", err)
			return toCallToolResult(result)
		}

		preview := truncatePreview(text, 500)
		hint := recoveryHints[toolName]
		if hint == "" {
			hint = "Try compact=true to reduce output size"
		}

		envelope := map[string]any{
			"sandboxed":     true,
			"handle":        handle,
			"tool":          toolName,
			"size_bytes":    len(text),
			"preview":       preview,
			"recovery_hint": hint,
			"next_tool":     "retrieve_output",
			"next_args": map[string]any{
				"handle": handle,
				"offset": 0,
				"limit":  4000,
			},
		}
		envelopeBytes, _ := json.MarshalIndent(envelope, "", "  ")
		return mcp.NewToolResultText(string(envelopeBytes)), nil
	}

	return toCallToolResult(result)
}

// truncatePreview returns a preview of text, truncated to maxLen with "..." appended.
func truncatePreview(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	preview := text[:maxLen]
	for !utf8.ValidString(preview) && len(preview) > 0 {
		preview = preview[:len(preview)-1]
	}
	return preview + "..."
}

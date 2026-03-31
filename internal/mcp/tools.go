package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	mcp_golang "github.com/metoro-io/mcp-golang"

	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/search"
	"github.com/naman/qb-context/internal/storage"
)

// ToolDeps holds dependencies needed by MCP tools
type ToolDeps struct {
	Store    *storage.Store
	Graph    *graph.GraphEngine
	Search   *search.HybridSearch
	RepoRoot string
}

// ContextParams are the parameters for the context tool.
// Tags provide JSON schema metadata for the MCP SDK.
type ContextParams struct {
	Query       string   `json:"query" jsonschema:"required,description=Natural language or keyword query to search for relevant code"`
	Limit       int      `json:"limit,omitempty" jsonschema:"description=Maximum number of results to return (default: 10)"`
	Mode        string   `json:"mode,omitempty" jsonschema:"description=Search mode: 'search' (default) for hybrid search or 'architecture' for community detection"`
	MaxPerFile  int      `json:"max_per_file,omitempty" jsonschema:"description=Maximum results per unique file path (default: 3)"`
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
}

// ArchitectureSummaryParams are the parameters for the get_architecture_summary tool
type ArchitectureSummaryParams struct {
	Limit int `json:"limit,omitempty" jsonschema:"description=Maximum number of hubs/entry points to return (default: 10)"`
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

// RegisterTools registers all 12 MCP tools with real implementations.
// Each tool is registered twice:
//   - As a ToolHandler (json.RawMessage) for CLI mode (GetHandler/GetTools)
//   - As a typed SDK handler for MCP protocol mode (SDK Serve)
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
	if p.Limit == 0 {
		p.Limit = 10
	}
	// Architecture mode: return community detection results + ADR context
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
		// Include ADR documents in architecture mode
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

	// Load architecture context if available
	summaries, _ := deps.Store.GetAllProjectSummaries()
	if len(summaries) > 0 {
		var adrTexts []string
		for _, s := range summaries {
			adrTexts = append(adrTexts, fmt.Sprintf("[%s] %s", s.Project, s.Summary))
		}
		return map[string]interface{}{
			"results":              results,
			"architecture_context": strings.Join(adrTexts, "\n\n"),
		}, nil
	}

	return results, nil
}

func registerContextTool(s *Server, deps ToolDeps) {
	desc := "Discovers relevant code symbols using hybrid lexical + semantic search. Returns ranked summaries of functions, classes, and structs matching the query."

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
					"description": "Maximum number of results to return (default: 10)",
					"default":     10,
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
			},
			"required": []string{"query"},
		},
	}, cliHandler)

	// SDK handler (typed struct)
	_ = s.RegisterSDKTool("context", desc, func(p ContextParams) (*mcp_golang.ToolResponse, error) {
		result, err := contextHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 2: impact -----

func impactHandler(deps ToolDeps, p ImpactParams) (interface{}, error) {
	if p.Depth == 0 {
		p.Depth = 5
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

	// Get betweenness score for the target node
	var riskScore float64
	if score, err := deps.Store.GetNodeScore(nodeID); err == nil {
		riskScore = score.Betweenness
	}

	// impactNode is a minimal node descriptor for the response
	type impactNode struct {
		ID         string `json:"id"`
		SymbolName string `json:"symbol_name"`
		FilePath   string `json:"file_path"`
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
		n := impactNode{
			ID:         node.ID,
			SymbolName: node.SymbolName,
			FilePath:   node.FilePath,
		}

		// Identify test nodes
		if strings.Contains(node.SymbolName, "test") || strings.Contains(node.SymbolName, "Test") {
			affectedTests = append(affectedTests, n)
		}

		// Group by risk level
		switch depth {
		case 1:
			direct = append(direct, n)
		case 2:
			highRisk = append(highRisk, n)
		case 3:
			mediumRisk = append(mediumRisk, n)
		default:
			lowRisk = append(lowRisk, n)
		}
	}

	totalAffected := len(affectedWithDepth)
	summary := fmt.Sprintf(
		"Symbol has betweenness %.2f — %d direct dependents, %d total affected, %d tests impacted",
		riskScore, len(direct), totalAffected, len(affectedTests),
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
	desc := "Analyzes the blast radius of a code symbol by tracing all downstream dependents via BFS graph traversal."

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

	// SDK handler
	_ = s.RegisterSDKTool("impact", desc, func(p ImpactParams) (*mcp_golang.ToolResponse, error) {
		result, err := impactHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
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
	resolvedRoot, _ := filepath.EvalSymlinks(deps.RepoRoot)
	if resolvedRoot == "" {
		resolvedRoot = deps.RepoRoot
	}
	if !strings.HasPrefix(absPath, resolvedRoot) {
		return nil, fmt.Errorf("path traversal detected: %s is outside repo root", node.FilePath)
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("opening file %s: %w", node.FilePath, err)
	}
	defer f.Close()

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

	return map[string]interface{}{
		"symbol_name": node.SymbolName,
		"file_path":   node.FilePath,
		"node_type":   node.NodeType.String(),
		"start_byte":  node.StartByte,
		"end_byte":    node.EndByte,
		"source":      string(buf),
	}, nil
}

func registerReadSymbolTool(s *Server, deps ToolDeps) {
	desc := "Retrieves the exact source code of a symbol by reading only the specific byte range from disk."

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

	// SDK handler
	_ = s.RegisterSDKTool("read_symbol", desc, func(p ReadSymbolParams) (*mcp_golang.ToolResponse, error) {
		result, err := readSymbolHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
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
	desc := "Executes a read-only SQL query against the structural database."

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

	// SDK handler
	_ = s.RegisterSDKTool("query", desc, func(p QueryParams) (*mcp_golang.ToolResponse, error) {
		result, err := queryHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
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
	desc := "Returns daemon health status and metrics."

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

	// SDK handler
	_ = s.RegisterSDKTool("health", desc, func(p HealthParams) (*mcp_golang.ToolResponse, error) {
		result, err := healthHandler(deps)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 6: index -----

func indexHandler(indexFn IndexFunc, p IndexParams) (interface{}, error) {
	if indexFn == nil {
		return nil, fmt.Errorf("index function not configured")
	}
	if err := indexFn(p.Path); err != nil {
		return nil, fmt.Errorf("indexing failed: %w", err)
	}
	return "Indexing completed successfully", nil
}

func registerIndexTool(s *Server, deps ToolDeps, indexFn IndexFunc) {
	desc := "Triggers a full re-index of the repository."

	// CLI handler
	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p IndexParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return indexHandler(indexFn, p)
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

	// SDK handler
	_ = s.RegisterSDKTool("index", desc, func(p IndexParams) (*mcp_golang.ToolResponse, error) {
		result, err := indexHandler(indexFn, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 6: trace_call_path -----

func traceCallPathHandler(deps ToolDeps, p TraceCallPathParams) (interface{}, error) {
	if p.From == "" || p.To == "" {
		return nil, fmt.Errorf("both 'from' and 'to' are required")
	}
	if p.MaxDepth == 0 {
		p.MaxDepth = 10
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

	// Resolve hash IDs to symbol names for readability
	var resolvedPaths [][]string
	var edgeTypes [][]string
	for _, path := range paths {
		var resolved []string
		var edges []string
		for i, hashID := range path {
			if node, err := deps.Store.GetNode(hashID); err == nil {
				resolved = append(resolved, node.SymbolName)
			} else {
				resolved = append(resolved, hashID)
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
	desc := "Finds call paths between two symbols using bidirectional BFS graph traversal. Useful for understanding how two parts of the codebase are connected."

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

	// SDK handler
	_ = s.RegisterSDKTool("trace_call_path", desc, func(p TraceCallPathParams) (*mcp_golang.ToolResponse, error) {
		result, err := traceCallPathHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 7: get_key_symbols -----

func getKeySymbolsHandler(deps ToolDeps, p GetKeySymbolsParams) (interface{}, error) {
	if p.Limit == 0 {
		p.Limit = 20
	}
	if deps.Graph == nil {
		return nil, fmt.Errorf("graph engine not initialized")
	}

	// Compute PageRank
	pageranks := deps.Graph.PageRank()
	if pageranks == nil {
		return map[string]interface{}{
			"symbols": []interface{}{},
			"count":   0,
		}, nil
	}

	type symbolInfo struct {
		Name      string  `json:"name"`
		FilePath  string  `json:"file_path"`
		NodeType  string  `json:"node_type"`
		PageRank  float64 `json:"pagerank"`
		InDegree  int     `json:"in_degree"`
		OutDegree int     `json:"out_degree"`
	}

	var symbols []symbolInfo
	for hashID, pr := range pageranks {
		node, err := deps.Store.GetNode(hashID)
		if err != nil {
			continue
		}

		// Apply file filter if specified
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
		})
	}

	// Sort by PageRank descending
	sort.Slice(symbols, func(i, j int) bool {
		return symbols[i].PageRank > symbols[j].PageRank
	})

	if len(symbols) > p.Limit {
		symbols = symbols[:p.Limit]
	}

	return map[string]interface{}{
		"symbols": symbols,
		"count":   len(symbols),
	}, nil
}

func registerGetKeySymbolsTool(s *Server, deps ToolDeps) {
	desc := "Returns the most important symbols in the codebase ranked by PageRank centrality and degree statistics."

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
					"description": "Maximum number of symbols to return (default: 20)",
					"default":     20,
				},
				"file_filter": map[string]interface{}{
					"type":        "string",
					"description": "Optional file path prefix to scope results to a specific directory",
				},
			},
		},
	}, cliHandler)

	// SDK handler
	_ = s.RegisterSDKTool("get_key_symbols", desc, func(p GetKeySymbolsParams) (*mcp_golang.ToolResponse, error) {
		result, err := getKeySymbolsHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 8: search_code -----

func searchCodeHandler(deps ToolDeps, p SearchCodeParams) (interface{}, error) {
	if p.Pattern == "" {
		return nil, fmt.Errorf("'pattern' is required")
	}
	if p.Limit == 0 {
		p.Limit = 20
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
	resolvedRoot, _ := filepath.EvalSymlinks(deps.RepoRoot)
	if resolvedRoot == "" {
		resolvedRoot = deps.RepoRoot
	}

	type codeMatch struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Content string `json:"content"`
	}

	var matches []codeMatch
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
		if !strings.HasPrefix(absPath, resolvedRoot) {
			continue // path traversal attempt
		}

		f, err := os.Open(absPath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, codeMatch{
					File:    relPath,
					Line:    lineNum,
					Content: strings.TrimSpace(line),
				})
				if len(matches) >= p.Limit {
					f.Close()
					goto done
				}
			}
		}
		f.Close()
	}
done:

	return map[string]interface{}{
		"matches": matches,
		"count":   len(matches),
		"pattern": p.Pattern,
	}, nil
}

func registerSearchCodeTool(s *Server, deps ToolDeps) {
	desc := "Searches for a regex pattern across all indexed source files. Returns matching lines with file paths and line numbers."

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
					"description": "Optional glob pattern to filter files (e.g., '*.go', '*.js')",
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

	// SDK handler
	_ = s.RegisterSDKTool("search_code", desc, func(p SearchCodeParams) (*mcp_golang.ToolResponse, error) {
		result, err := searchCodeHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 9: detect_changes -----

func detectChangesHandler(deps ToolDeps, p DetectChangesParams) (interface{}, error) {
	if p.Since == "" {
		return nil, fmt.Errorf("'since' is required")
	}

	// Validate the git ref to prevent command injection
	// Only allow alphanumeric, ~, ^, ., -, /, @
	validRef := regexp.MustCompile(`^[a-zA-Z0-9~^.\-/@{}]+$`)
	if !validRef.MatchString(p.Since) {
		return nil, fmt.Errorf("invalid git ref: %s", p.Since)
	}

	// Run git diff to get changed files
	args := []string{"diff", "--name-only", p.Since}
	if p.Path != "" {
		// Validate path filter
		if strings.Contains(p.Path, "..") {
			return nil, fmt.Errorf("path traversal detected in path filter")
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

	// For each changed file, compare stored symbols with current state
	type symbolChange struct {
		Name     string `json:"name"`
		FilePath string `json:"file_path"`
		ID       string `json:"id"`
	}

	var changedSymbols []symbolChange
	var newSymbols []symbolChange
	var deletedSymbols []symbolChange

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
				deletedSymbols = append(deletedSymbols, symbolChange{
					Name:     n.SymbolName,
					FilePath: n.FilePath,
					ID:       n.ID,
				})
			}
			continue
		}

		// File exists but changed — stored symbols might be modified
		for _, n := range storedNodes {
			changedSymbols = append(changedSymbols, symbolChange{
				Name:     n.SymbolName,
				FilePath: n.FilePath,
				ID:       n.ID,
			})
		}

		// If no stored nodes exist, file might be new
		if len(storedNodes) == 0 {
			newSymbols = append(newSymbols, symbolChange{
				Name:     "(new file)",
				FilePath: filePath,
			})
		}
	}

	return map[string]interface{}{
		"changed_files":   changedFiles,
		"changed_symbols": changedSymbols,
		"new_symbols":     newSymbols,
		"deleted_symbols": deletedSymbols,
		"summary": fmt.Sprintf("%d files changed, %d symbols modified, %d new, %d deleted",
			len(changedFiles), len(changedSymbols), len(newSymbols), len(deletedSymbols)),
	}, nil
}

func registerDetectChangesTool(s *Server, deps ToolDeps) {
	desc := "Detects changed files and symbols since a given git ref. Compares current file state with the indexed symbol database."

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
			},
			"required": []string{"since"},
		},
	}, cliHandler)

	// SDK handler
	_ = s.RegisterSDKTool("detect_changes", desc, func(p DetectChangesParams) (*mcp_golang.ToolResponse, error) {
		result, err := detectChangesHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 10: get_architecture_summary -----

func architectureSummaryHandler(deps ToolDeps, p ArchitectureSummaryParams) (interface{}, error) {
	if deps.Graph == nil {
		return nil, fmt.Errorf("graph engine not initialized")
	}
	if p.Limit == 0 {
		p.Limit = 10
	}

	// Communities
	communities, modularity := deps.Graph.DetectCommunities()

	// Resolve community nodes to names
	type namedCommunity struct {
		ID      int      `json:"id"`
		Size    int      `json:"size"`
		Members []string `json:"members"`
	}
	var namedComms []namedCommunity
	for _, c := range communities {
		var members []string
		for _, hashID := range c.NodeIDs {
			if node, err := deps.Store.GetNode(hashID); err == nil {
				members = append(members, node.SymbolName)
			}
		}
		namedComms = append(namedComms, namedCommunity{
			ID:      c.ID,
			Size:    len(c.NodeIDs),
			Members: members,
		})
	}

	// Entry points: zero in-degree
	entryPointIDs := deps.Graph.GetEntryPoints()
	type namedNode struct {
		Name     string `json:"name"`
		FilePath string `json:"file_path"`
		ID       string `json:"id"`
	}
	var entryPoints []namedNode
	for _, hashID := range entryPointIDs {
		if node, err := deps.Store.GetNode(hashID); err == nil {
			entryPoints = append(entryPoints, namedNode{
				Name:     node.SymbolName,
				FilePath: node.FilePath,
				ID:       node.ID,
			})
		}
		if len(entryPoints) >= p.Limit {
			break
		}
	}

	// Hubs: highest out-degree
	hubEntries := deps.Graph.GetHubs(p.Limit)
	type hubInfo struct {
		Name      string `json:"name"`
		FilePath  string `json:"file_path"`
		OutDegree int    `json:"out_degree"`
	}
	var hubs []hubInfo
	for _, h := range hubEntries {
		if node, err := deps.Store.GetNode(h.HashID); err == nil {
			hubs = append(hubs, hubInfo{
				Name:      node.SymbolName,
				FilePath:  node.FilePath,
				OutDegree: h.OutDegree,
			})
		}
	}

	// Connectors: high betweenness + edges to multiple communities
	// Use pre-computed betweenness from node_scores table instead of O(V*E) recomputation
	betweenness, _ := deps.Store.GetAllBetweenness()
	connectorIDs := deps.Graph.GetConnectors(betweenness, p.Limit)
	var connectors []namedNode
	for _, hashID := range connectorIDs {
		if node, err := deps.Store.GetNode(hashID); err == nil {
			connectors = append(connectors, namedNode{
				Name:     node.SymbolName,
				FilePath: node.FilePath,
				ID:       node.ID,
			})
		}
	}

	return map[string]interface{}{
		"communities":      namedComms,
		"modularity":       modularity,
		"community_count":  len(communities),
		"entry_points":     entryPoints,
		"hubs":             hubs,
		"connectors":       connectors,
		"total_nodes":      deps.Graph.NodeCount(),
		"total_edges":      deps.Graph.EdgeCount(),
	}, nil
}

func registerArchitectureSummaryTool(s *Server, deps ToolDeps) {
	desc := "Provides a comprehensive architecture summary including communities, entry points (zero in-degree), hubs (high out-degree), and cross-community connectors."

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
					"description": "Maximum number of hubs/entry points to return (default: 10)",
					"default":     10,
				},
			},
		},
	}, cliHandler)

	// SDK handler
	_ = s.RegisterSDKTool("get_architecture_summary", desc, func(p ArchitectureSummaryParams) (*mcp_golang.ToolResponse, error) {
		result, err := architectureSummaryHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Tool 11: explore -----

func exploreHandler(deps ToolDeps, p ExploreParams) (interface{}, error) {
	if p.Symbol == "" {
		return nil, fmt.Errorf("'symbol' is required")
	}
	if p.Depth == 0 {
		p.Depth = 2
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
		ftsResults, err := deps.Search.Search(p.Symbol, 10, nil)
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

	result := map[string]interface{}{
		"matches": matches,
		"count":   len(matches),
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
	desc := "Explores the codebase by searching for a symbol and optionally collecting its dependencies, dependents, and hotspots."

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

	// SDK handler
	_ = s.RegisterSDKTool("explore", desc, func(p ExploreParams) (*mcp_golang.ToolResponse, error) {
		result, err := exploreHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
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
	if found == nil && deps.Search != nil {
		resolution = "fts"
		results, err := deps.Search.Search(p.Symbol, 1, nil)
		if err == nil && len(results) > 0 {
			n := results[0].Node
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

		// PageRank score
		pageranks := deps.Graph.PageRank()
		if pageranks != nil {
			result["pagerank"] = pageranks[resolvedID]
		}

		// Community membership (with early exit)
		communities, _ := deps.Graph.DetectCommunities()
		for _, c := range communities {
			found := false
			for _, nodeID := range c.NodeIDs {
				if nodeID == resolvedID {
					result["community"] = c.ID
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}

	return result, nil
}

func registerUnderstandTool(s *Server, deps ToolDeps) {
	desc := "Deep symbol understanding with 3-tier resolution (exact match, fuzzy match, file-scoped search) plus callers, callees, PageRank, and community membership."

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

	// SDK handler
	_ = s.RegisterSDKTool("understand", desc, func(p UnderstandParams) (*mcp_golang.ToolResponse, error) {
		result, err := understandHandler(deps, p)
		if err != nil {
			return nil, err
		}
		return toToolResponse(result)
	})
}

// ----- Helpers -----

// ----- MCP Resources (M9) -----

func registerResources(s *Server, deps ToolDeps) {
	// Register codebase graph statistics as a resource
	_ = s.sdk.RegisterResource(
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
	)
}

// ----- MCP Prompts (M9) -----

type explainSymbolArgs struct {
	Symbol string `json:"symbol" jsonschema:"required,description=The symbol name to explain"`
}

func registerPrompts(s *Server, deps ToolDeps) {
	// Register an "explain symbol" prompt template
	_ = s.sdk.RegisterPrompt(
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
	)
}

// toToolResponse converts a generic result (string or anything JSON-serializable)
// into the SDK's ToolResponse format.
func toToolResponse(result interface{}) (*mcp_golang.ToolResponse, error) {
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
	return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(text)), nil
}


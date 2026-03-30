package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Query string `json:"query" jsonschema:"required,description=Natural language or keyword query to search for relevant code"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=Maximum number of results to return (default: 10)"`
	Mode  string `json:"mode,omitempty" jsonschema:"description=Search mode: 'search' (default) for hybrid search or 'architecture' for community detection"`
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

// IndexParams are the parameters for the index tool.
type IndexParams struct {
	Path string `json:"path,omitempty" jsonschema:"description=Optional: specific path to re-index"`
}

// IndexFunc is the callback for triggering a re-index
type IndexFunc func(path string) error

// RegisterTools registers all 5 MCP tools with real implementations.
// Each tool is registered twice:
//   - As a ToolHandler (json.RawMessage) for CLI mode (GetHandler/GetTools)
//   - As a typed SDK handler for MCP protocol mode (SDK Serve)
func RegisterTools(s *Server, deps ToolDeps, indexFn IndexFunc) {
	registerContextTool(s, deps)
	registerImpactTool(s, deps)
	registerReadSymbolTool(s, deps)
	registerQueryTool(s, deps)
	registerIndexTool(s, deps, indexFn)
}

// ----- Tool 1: context -----

func contextHandler(deps ToolDeps, p ContextParams) (interface{}, error) {
	if p.Limit == 0 {
		p.Limit = 10
	}
	// Architecture mode: return community detection results
	if p.Mode == "architecture" {
		if deps.Graph == nil {
			return nil, fmt.Errorf("graph engine not initialized")
		}
		communities, modularity := deps.Graph.DetectCommunities()
		return map[string]interface{}{
			"mode":        "architecture",
			"communities": communities,
			"modularity":  modularity,
			"count":       len(communities),
		}, nil
	}
	if deps.Search == nil {
		return nil, fmt.Errorf("search engine not initialized")
	}
	results, err := deps.Search.Search(p.Query, p.Limit, nil)
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

// ----- Tool 5: index -----

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

// ----- Helpers -----

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

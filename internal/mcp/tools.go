package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// ContextParams are the parameters for the context tool
type ContextParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// ImpactParams are the parameters for the impact tool
type ImpactParams struct {
	SymbolID string `json:"symbol_id"`
	Depth    int    `json:"depth,omitempty"`
}

// ReadSymbolParams are the parameters for the read_symbol tool
type ReadSymbolParams struct {
	SymbolID string `json:"symbol_id"`
}

// QueryParams are the parameters for the query tool
type QueryParams struct {
	SQL string `json:"sql"`
}

// IndexParams are the parameters for the index tool
type IndexParams struct {
	Path string `json:"path,omitempty"`
}

// IndexFunc is the callback for triggering a re-index
type IndexFunc func(path string) error

// RegisterTools registers all 5 MCP tools with real implementations
func RegisterTools(s *Server, deps ToolDeps, indexFn IndexFunc) {
	// Tool 1: context — discovers relevant code via hybrid search
	s.RegisterTool(
		ToolDefinition{
			Name:        "context",
			Description: "Discovers relevant code symbols using hybrid lexical + semantic search. Returns ranked summaries of functions, classes, and structs matching the query.",
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
				},
				"required": []string{"query"},
			},
		},
		func(params json.RawMessage) (interface{}, error) {
			var p ContextParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
			if p.Limit == 0 {
				p.Limit = 10
			}
			if deps.Search == nil {
				return nil, fmt.Errorf("search engine not initialized")
			}
			results, err := deps.Search.Search(p.Query, p.Limit, nil)
			if err != nil {
				return nil, fmt.Errorf("search failed: %w", err)
			}
			return results, nil
		},
	)

	// Tool 2: impact — analyzes blast radius
	s.RegisterTool(
		ToolDefinition{
			Name:        "impact",
			Description: "Analyzes the blast radius of a code symbol by tracing all downstream dependents via BFS graph traversal.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p ImpactParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
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

			affected := deps.Graph.BlastRadius(nodeID, p.Depth)

			// Resolve affected IDs to node details
			type impactNode struct {
				ID         string `json:"id"`
				SymbolName string `json:"symbol_name"`
				FilePath   string `json:"file_path"`
			}
			var nodes []impactNode
			for _, id := range affected {
				if node, err := deps.Store.GetNode(id); err == nil {
					nodes = append(nodes, impactNode{
						ID:         node.ID,
						SymbolName: node.SymbolName,
						FilePath:   node.FilePath,
					})
				}
			}
			return map[string]interface{}{
				"symbol":         p.SymbolID,
				"depth":          p.Depth,
				"affected_count": len(nodes),
				"affected":       nodes,
			}, nil
		},
	)

	// Tool 3: read_symbol — retrieves exact source code by byte range
	s.RegisterTool(
		ToolDefinition{
			Name:        "read_symbol",
			Description: "Retrieves the exact source code of a symbol by reading only the specific byte range from disk.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p ReadSymbolParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}

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
			if !strings.HasPrefix(absPath, deps.RepoRoot) {
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
		},
	)

	// Tool 4: query — diagnostic SQL tool
	s.RegisterTool(
		ToolDefinition{
			Name:        "query",
			Description: "Executes a read-only SQL query against the structural database.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p QueryParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
			results, err := deps.Store.RawQuery(p.SQL)
			if err != nil {
				return nil, fmt.Errorf("query failed: %w", err)
			}
			return results, nil
		},
	)

	// Tool 5: index — re-index the repository
	s.RegisterTool(
		ToolDefinition{
			Name:        "index",
			Description: "Triggers a full re-index of the repository.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Optional: specific path to re-index",
					},
				},
			},
		},
		func(params json.RawMessage) (interface{}, error) {
			var p IndexParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
			if indexFn == nil {
				return nil, fmt.Errorf("index function not configured")
			}
			if err := indexFn(p.Path); err != nil {
				return nil, fmt.Errorf("indexing failed: %w", err)
			}
			return "Indexing completed successfully", nil
		},
	)
}

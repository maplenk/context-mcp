package mcp

import (
	"encoding/json"
	"fmt"
)

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

// RegisterDefaultTools registers all 5 MCP tools with stub handlers.
// These stubs will be replaced with real implementations in Commit 4.
func RegisterDefaultTools(s *Server) {
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
			return fmt.Sprintf("context search stub: query=%q limit=%d", p.Query, p.Limit), nil
		},
	)

	// Tool 2: impact — analyzes blast radius of a symbol
	s.RegisterTool(
		ToolDefinition{
			Name:        "impact",
			Description: "Analyzes the blast radius of a code symbol by tracing all downstream dependents via BFS graph traversal. Shows what would be affected by modifying the given symbol.",
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
			return fmt.Sprintf("impact analysis stub: symbol=%q depth=%d", p.SymbolID, p.Depth), nil
		},
	)

	// Tool 3: read_symbol — retrieves exact source code
	s.RegisterTool(
		ToolDefinition{
			Name:        "read_symbol",
			Description: "Retrieves the exact source code of a symbol by reading only the specific byte range from disk. Efficient and precise — no need to load the entire file.",
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
			return fmt.Sprintf("read_symbol stub: symbol=%q", p.SymbolID), nil
		},
	)

	// Tool 4: query — diagnostic database tool
	s.RegisterTool(
		ToolDefinition{
			Name:        "query",
			Description: "Executes a read-only SQL query against the structural database. Useful for diagnostic and exploratory queries about the codebase structure.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"sql": map[string]interface{}{
						"type":        "string",
						"description": "SQL query to execute (read-only)",
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
			return fmt.Sprintf("query stub: sql=%q", p.SQL), nil
		},
	)

	// Tool 5: index — manages daemon state
	s.RegisterTool(
		ToolDefinition{
			Name:        "index",
			Description: "Triggers a full re-index of the repository. Walks the file tree, parses all source files, generates embeddings, and rebuilds the structural graph.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Optional: specific file or directory path to re-index. Omit to re-index the entire repository.",
					},
				},
			},
		},
		func(params json.RawMessage) (interface{}, error) {
			var p IndexParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
			path := p.Path
			if path == "" {
				path = "(entire repository)"
			}
			return fmt.Sprintf("index stub: path=%s", path), nil
		},
	)
}

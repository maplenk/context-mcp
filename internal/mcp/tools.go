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
	Mode  string `json:"mode,omitempty"`
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

// TraceCallPathParams are the parameters for the trace_call_path tool
type TraceCallPathParams struct {
	From     string `json:"from"`
	To       string `json:"to"`
	MaxDepth int    `json:"max_depth,omitempty"`
}

// GetKeySymbolsParams are the parameters for the get_key_symbols tool
type GetKeySymbolsParams struct {
	Limit      int    `json:"limit,omitempty"`
	FileFilter string `json:"file_filter,omitempty"`
}

// SearchCodeParams are the parameters for the search_code tool
type SearchCodeParams struct {
	Pattern    string `json:"pattern"`
	FileFilter string `json:"file_filter,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// DetectChangesParams are the parameters for the detect_changes tool
type DetectChangesParams struct {
	Since string `json:"since"`
	Path  string `json:"path,omitempty"`
}

// ExploreParams are the parameters for the explore tool
type ExploreParams struct {
	Symbol      string `json:"symbol"`
	IncludeDeps bool   `json:"include_deps,omitempty"`
	Depth       int    `json:"depth,omitempty"`
}

// UnderstandParams are the parameters for the understand tool
type UnderstandParams struct {
	Symbol string `json:"symbol"`
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
					"mode": map[string]interface{}{
						"type":        "string",
						"description": "Search mode: 'search' (default) for hybrid search, 'architecture' for community detection",
						"default":     "search",
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

	// Tool 6: trace_call_path — bidirectional BFS to find call paths between two symbols
	s.RegisterTool(
		ToolDefinition{
			Name:        "trace_call_path",
			Description: "Finds call paths between two symbols using bidirectional BFS graph traversal. Useful for understanding how two parts of the codebase are connected.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p TraceCallPathParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
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
		},
	)

	// Tool 7: get_key_symbols — return top-K most important symbols by PageRank + degree
	s.RegisterTool(
		ToolDefinition{
			Name:        "get_key_symbols",
			Description: "Returns the most important symbols in the codebase ranked by PageRank centrality and degree statistics.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p GetKeySymbolsParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
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
		},
	)

	// Tool 8: search_code — regex/pattern-based code search across indexed files
	s.RegisterTool(
		ToolDefinition{
			Name:        "search_code",
			Description: "Searches for a regex pattern across all indexed source files. Returns matching lines with file paths and line numbers.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p SearchCodeParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
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
				// Apply file filter if specified
				if p.FileFilter != "" {
					matched, err := filepath.Match(p.FileFilter, filepath.Base(relPath))
					if err != nil || !matched {
						continue
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
		},
	)

	// Tool 9: detect_changes — git-based file/symbol change detection
	s.RegisterTool(
		ToolDefinition{
			Name:        "detect_changes",
			Description: "Detects changed files and symbols since a given git ref. Compares current file state with the indexed symbol database.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p DetectChangesParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
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
		},
	)

	// Tool 10: Enhanced architecture mode for the context tool is handled above.
	// Register a standalone get_architecture_summary tool for more detailed analysis.
	s.RegisterTool(
		ToolDefinition{
			Name:        "get_architecture_summary",
			Description: "Provides a comprehensive architecture summary including communities, entry points (zero in-degree), hubs (high out-degree), and cross-community connectors.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			if deps.Graph == nil {
				return nil, fmt.Errorf("graph engine not initialized")
			}

			var p struct {
				Limit int `json:"limit,omitempty"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
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
			betweenness := deps.Graph.ComputeBetweenness()
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
		},
	)

	// Tool 11: explore — multi-search combining name matching, file matching, and dependency collection
	s.RegisterTool(
		ToolDefinition{
			Name:        "explore",
			Description: "Explores the codebase by searching for a symbol and optionally collecting its dependencies, dependents, and hotspots.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p ExploreParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
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
				betweenness := deps.Graph.ComputeBetweenness()
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
		},
	)

	// Tool 12: understand — 3-tier symbol resolution with callers/callees/PageRank
	s.RegisterTool(
		ToolDefinition{
			Name:        "understand",
			Description: "Deep symbol understanding with 3-tier resolution (exact match, fuzzy match, file-scoped search) plus callers, callees, PageRank, and community membership.",
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
		},
		func(params json.RawMessage) (interface{}, error) {
			var p UnderstandParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid parameters: %w", err)
			}
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

				// Community membership
				communities, _ := deps.Graph.DetectCommunities()
				for _, c := range communities {
					for _, nodeID := range c.NodeIDs {
						if nodeID == resolvedID {
							result["community"] = c.ID
							break
						}
					}
				}
			}

			return result, nil
		},
	)
}

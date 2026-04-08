package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maplenk/context-mcp/internal/types"
)

func nodeTypeStringFromValue(value any) string {
	switch n := value.(type) {
	case int64:
		if n < 0 || n > math.MaxUint8 {
			return "unknown"
		}
		// #nosec G115 -- n is range-checked before conversion to NodeType's uint8 backing type.
		return types.NodeType(n).String()
	case float64:
		if n < 0 || n > math.MaxUint8 || math.Trunc(n) != n {
			return "unknown"
		}
		// #nosec G115 -- n is integral and range-checked before conversion.
		return types.NodeType(int(n)).String()
	case string:
		return n
	default:
		return "unknown"
	}
}

// RegisterResources registers all MCP resources with the server.
func RegisterResources(s *Server, deps ToolDeps) {
	registerRepoSummaryResource(s, deps)
	registerIndexStatsResource(s, deps)
	registerChangedSymbolsResource(s, deps)
	registerHotPathsResource(s, deps)
}

func registerRepoSummaryResource(s *Server, deps ToolDeps) {
	resource := mcp.NewResource("context-mcp://repo_summary", "Repository Summary",
		mcp.WithResourceDescription("High-level summary of the indexed repository"),
		mcp.WithMIMEType("application/json"),
	)
	s.AddResource(resource, func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		totalNodes := 0
		totalEdges := 0
		graphNodes := 0
		graphEdges := 0

		if deps.Store != nil {
			ids, err := deps.Store.GetAllNodeIDs()
			if err == nil {
				totalNodes = len(ids)
			}
			// Count edges via SQL
			rows, err := deps.Store.RawQuery("SELECT COUNT(*) as cnt FROM edges")
			if err == nil && len(rows) > 0 {
				if cnt, ok := rows[0]["cnt"]; ok {
					switch v := cnt.(type) {
					case int64:
						totalEdges = int(v)
					case float64:
						totalEdges = int(v)
					}
				}
			}
		}

		if deps.Graph != nil {
			graphNodes = deps.Graph.NodeCount()
			graphEdges = deps.Graph.EdgeCount()
		}

		data := map[string]interface{}{
			"repo_root":   deps.RepoRoot,
			"total_nodes": totalNodes,
			"total_edges": totalEdges,
			"graph_nodes": graphNodes,
			"graph_edges": graphEdges,
			"profile":     deps.Profile,
		}

		jsonBytes, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal repo_summary: %w", err)
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(jsonBytes),
			},
		}, nil
	})
}

func registerIndexStatsResource(s *Server, deps ToolDeps) {
	resource := mcp.NewResource("context-mcp://index_stats", "Index Statistics",
		mcp.WithResourceDescription("Detailed statistics about the code index"),
		mcp.WithMIMEType("application/json"),
	)
	s.AddResource(resource, func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		data := map[string]interface{}{
			"node_counts_by_type": map[string]int{},
			"total_nodes":         0,
			"total_edges":         0,
			"unique_files":        0,
			"top_files_by_nodes":  []interface{}{},
		}

		if deps.Store != nil {
			// Node counts by type
			typeRows, err := deps.Store.RawQuery("SELECT node_type, COUNT(*) as cnt FROM nodes GROUP BY node_type")
			if err == nil {
				typeCounts := make(map[string]int)
				totalNodes := 0
				for _, row := range typeRows {
					nodeType := ""
					count := 0
					if v, ok := row["node_type"]; ok {
						nodeType = nodeTypeStringFromValue(v)
					}
					if v, ok := row["cnt"]; ok {
						switch c := v.(type) {
						case int64:
							count = int(c)
						case float64:
							count = int(c)
						}
					}
					if nodeType != "" {
						typeCounts[nodeType] = count
						totalNodes += count
					}
				}
				data["node_counts_by_type"] = typeCounts
				data["total_nodes"] = totalNodes
			}

			// Total edges
			edgeRows, err := deps.Store.RawQuery("SELECT COUNT(*) as cnt FROM edges")
			if err == nil && len(edgeRows) > 0 {
				if cnt, ok := edgeRows[0]["cnt"]; ok {
					switch v := cnt.(type) {
					case int64:
						data["total_edges"] = int(v)
					case float64:
						data["total_edges"] = int(v)
					}
				}
			}

			// Unique files
			fileCountRows, err := deps.Store.RawQuery("SELECT COUNT(DISTINCT file_path) as cnt FROM nodes")
			if err == nil && len(fileCountRows) > 0 {
				if cnt, ok := fileCountRows[0]["cnt"]; ok {
					switch v := cnt.(type) {
					case int64:
						data["unique_files"] = int(v)
					case float64:
						data["unique_files"] = int(v)
					}
				}
			}

			// Top files by node count
			topFileRows, err := deps.Store.RawQuery("SELECT file_path, COUNT(*) as cnt FROM nodes GROUP BY file_path ORDER BY cnt DESC LIMIT 10")
			if err == nil {
				var topFiles []map[string]interface{}
				for _, row := range topFileRows {
					path := ""
					count := 0
					if v, ok := row["file_path"]; ok {
						path, _ = v.(string)
					}
					if v, ok := row["cnt"]; ok {
						switch c := v.(type) {
						case int64:
							count = int(c)
						case float64:
							count = int(c)
						}
					}
					if path != "" {
						topFiles = append(topFiles, map[string]interface{}{
							"path":  path,
							"count": count,
						})
					}
				}
				data["top_files_by_nodes"] = topFiles
			}
		}

		jsonBytes, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal index_stats: %w", err)
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(jsonBytes),
			},
		}, nil
	})
}

func registerChangedSymbolsResource(s *Server, deps ToolDeps) {
	resource := mcp.NewResource("context-mcp://changed_symbols", "Recently Changed Symbols",
		mcp.WithResourceDescription("Symbols in files changed since the last commit"),
		mcp.WithMIMEType("application/json"),
	)
	s.AddResource(resource, func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		var symbols []map[string]interface{}

		if deps.Store != nil && deps.RepoRoot != "" {
			// Get changed files from git
			cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "HEAD~1")
			cmd.Dir = deps.RepoRoot
			out, err := cmd.Output()
			if err == nil {
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					nodes, err := deps.Store.GetNodesByFile(line)
					if err != nil {
						continue
					}
					for _, n := range nodes {
						symbols = append(symbols, map[string]interface{}{
							"id":          n.ID,
							"symbol_name": n.SymbolName,
							"file_path":   n.FilePath,
							"node_type":   n.NodeType.String(),
						})
					}
				}
			}
		}

		if symbols == nil {
			symbols = []map[string]interface{}{}
		}

		data := map[string]interface{}{
			"changed_symbols": symbols,
			"count":           len(symbols),
		}

		jsonBytes, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal changed_symbols: %w", err)
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(jsonBytes),
			},
		}, nil
	})
}

func registerHotPathsResource(s *Server, deps ToolDeps) {
	resource := mcp.NewResource("context-mcp://hot_paths", "Hot Paths",
		mcp.WithResourceDescription("Most connected symbols by PageRank and call frequency"),
		mcp.WithMIMEType("application/json"),
	)
	s.AddResource(resource, func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		var hotPaths []map[string]interface{}

		if deps.Store != nil {
			rows, err := deps.Store.RawQuery(
				"SELECT n.id, n.symbol_name, n.file_path, n.node_type, s.pagerank, s.betweenness " +
					"FROM nodes n JOIN node_scores s ON n.id = s.node_id " +
					"ORDER BY s.pagerank DESC LIMIT 20")
			if err == nil {
				for _, row := range rows {
					nodeType := ""
					if v, ok := row["node_type"]; ok {
						nodeType = nodeTypeStringFromValue(v)
					}
					entry := map[string]interface{}{
						"id":          row["id"],
						"symbol_name": row["symbol_name"],
						"file_path":   row["file_path"],
						"node_type":   nodeType,
						"pagerank":    row["pagerank"],
						"betweenness": row["betweenness"],
					}
					hotPaths = append(hotPaths, entry)
				}
			}
		}

		if hotPaths == nil {
			hotPaths = []map[string]interface{}{}
		}

		data := map[string]interface{}{
			"hot_paths": hotPaths,
			"count":     len(hotPaths),
		}

		jsonBytes, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal hot_paths: %w", err)
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(jsonBytes),
			},
		}, nil
	})
}

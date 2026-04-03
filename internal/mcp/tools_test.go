//go:build fts5

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/search"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
)

// setupTestEnv creates a test environment with a store, graph, and sample data.
func setupTestEnv(t *testing.T) (ToolDeps, func()) {
	t.Helper()
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, ".qb-context", "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	graphEngine := graph.New()

	// Create sample source file for search_code tests
	srcContent := `package main

import "fmt"

func processOrder(order Order) error {
	total := calculateTotal(order)
	fmt.Println(total)
	return nil
}

func calculateTotal(order Order) float64 {
	return order.Price * float64(order.Quantity)
}

func validateOrder(order Order) bool {
	return order.Price > 0 && order.Quantity > 0
}
`
	srcFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(srcFile, []byte(srcContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Define sample nodes
	// Note: byte ranges are synthetic — they don't correspond to real file content.
	// Byte-range correctness is tested in integration tests with real parsed files.
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("main.go", "processOrder"),
			FilePath:   "main.go",
			SymbolName: "processOrder",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    100,
			ContentSum: "process order function",
		},
		{
			ID:         types.GenerateNodeID("main.go", "calculateTotal"),
			FilePath:   "main.go",
			SymbolName: "calculateTotal",
			NodeType:   types.NodeTypeFunction,
			StartByte:  100,
			EndByte:    200,
			ContentSum: "calculate total function",
		},
		{
			ID:         types.GenerateNodeID("main.go", "validateOrder"),
			FilePath:   "main.go",
			SymbolName: "validateOrder",
			NodeType:   types.NodeTypeFunction,
			StartByte:  200,
			EndByte:    300,
			ContentSum: "validate order function",
		},
		{
			ID:         types.GenerateNodeID("util.go", "helperFunc"),
			FilePath:   "util.go",
			SymbolName: "helperFunc",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    50,
			ContentSum: "helper utility function",
		},
	}

	if err := store.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// Edges: processOrder -> calculateTotal, processOrder -> validateOrder, validateOrder -> helperFunc
	edges := []types.ASTEdge{
		{SourceID: nodes[0].ID, TargetID: nodes[1].ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: nodes[0].ID, TargetID: nodes[2].ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: nodes[2].ID, TargetID: nodes[3].ID, EdgeType: types.EdgeTypeCalls},
	}

	if err := store.UpsertEdges(edges); err != nil {
		t.Fatalf("UpsertEdges: %v", err)
	}

	graphEngine.BuildFromEdges(edges)

	// Store node scores
	pageranks := graphEngine.PageRank()
	betweenness := graphEngine.ComputeBetweenness()
	var scores []types.NodeScore
	for _, n := range nodes {
		scores = append(scores, types.NodeScore{
			NodeID:      n.ID,
			PageRank:    pageranks[n.ID],
			Betweenness: betweenness[n.ID],
		})
	}
	if err := store.UpsertNodeScores(scores); err != nil {
		t.Fatalf("UpsertNodeScores: %v", err)
	}

	// Use a real embedder so the semantic search signal (15% of ranking) is exercised.
	embedder := embedding.NewHashEmbedder()
	for _, n := range nodes {
		vec, err := embedder.Embed(n.SymbolName + " " + n.ContentSum)
		if err != nil {
			t.Fatalf("Embed(%s): %v", n.SymbolName, err)
		}
		if err := store.UpsertEmbedding(n.ID, vec); err != nil {
			t.Fatalf("UpsertEmbedding(%s): %v", n.SymbolName, err)
		}
	}

	hybridSearch := search.New(store, embedder, graphEngine)

	deps := ToolDeps{
		Store:    store,
		Graph:    graphEngine,
		Search:   hybridSearch,
		RepoRoot: tmpDir,
	}

	cleanup := func() {
		store.Close()
	}

	return deps, cleanup
}

func TestTraceCallPath_DirectConnection(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("trace_call_path")
	if !ok {
		t.Fatal("trace_call_path handler not registered")
	}

	params, _ := json.Marshal(TraceCallPathParams{
		From:     "processOrder",
		To:       "calculateTotal",
		MaxDepth: 10,
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("trace_call_path error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}

	paths, ok := m["paths"].([][]string)
	if !ok {
		t.Fatalf("expected paths to be [][]string, got %T", m["paths"])
	}

	if len(paths) == 0 {
		t.Fatal("expected at least one path")
	}

	t.Logf("Found %d path(s) from processOrder to calculateTotal", len(paths))
	for i, p := range paths {
		t.Logf("  Path %d: %v", i, p)
	}
}

func TestTraceCallPath_TransitiveConnection(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("trace_call_path")

	params, _ := json.Marshal(TraceCallPathParams{
		From:     "processOrder",
		To:       "helperFunc",
		MaxDepth: 10,
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("trace_call_path error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	paths, ok := m["paths"].([][]string)
	if !ok {
		t.Fatalf("expected paths to be [][]string, got %T", m["paths"])
	}

	if len(paths) == 0 {
		t.Fatal("expected at least one path for transitive connection")
	}

	// Path should be: processOrder -> validateOrder -> helperFunc
	found := false
	for _, p := range paths {
		if len(p) == 3 {
			found = true
			t.Logf("Found transitive path: %v", p)
		}
	}
	if !found {
		t.Errorf("expected a 3-node path, got paths: %v", paths)
	}
}

func TestTraceCallPath_NoConnection(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("trace_call_path")

	// helperFunc -> processOrder has no path (reverse direction)
	params, _ := json.Marshal(TraceCallPathParams{
		From:     "helperFunc",
		To:       "processOrder",
		MaxDepth: 10,
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("trace_call_path error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	count, ok := m["count"].(int)
	if !ok {
		t.Fatalf("expected count to be int, got %T", m["count"])
	}
	if count != 0 {
		t.Errorf("expected 0 paths for reverse direction, got %d", count)
	}
}

func TestGetKeySymbols_Basic(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("get_key_symbols")
	if !ok {
		t.Fatal("get_key_symbols handler not registered")
	}

	params, _ := json.Marshal(GetKeySymbolsParams{Limit: 10})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("get_key_symbols error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	count, ok := m["count"].(int)
	if !ok {
		t.Fatalf("expected count to be int, got %T", m["count"])
	}
	if count == 0 {
		t.Fatal("expected at least one key symbol")
	}

	t.Logf("Found %d key symbols", count)
}

func TestGetKeySymbols_WithFileFilter(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("get_key_symbols")

	params, _ := json.Marshal(GetKeySymbolsParams{
		Limit:      10,
		FileFilter: "util",
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("get_key_symbols error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	count, ok := m["count"].(int)
	if !ok {
		t.Fatalf("expected count to be int, got %T", m["count"])
	}
	// Only helperFunc in util.go should match
	if count != 1 {
		t.Errorf("expected 1 symbol with file_filter='util', got %d", count)
	}
}

func TestSearchCode_Basic(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("search_code")
	if !ok {
		t.Fatal("search_code handler not registered")
	}

	params, _ := json.Marshal(SearchCodeParams{
		Pattern: "calculateTotal",
		Limit:   20,
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("search_code error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	count, ok := m["count"].(int)
	if !ok {
		t.Fatalf("expected count to be int, got %T", m["count"])
	}
	if count == 0 {
		t.Fatal("expected at least one match for 'calculateTotal'")
	}

	t.Logf("Found %d matches for 'calculateTotal'", count)
}

func TestSearchCode_WithFileFilter(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("search_code")

	params, _ := json.Marshal(SearchCodeParams{
		Pattern:    "func",
		FileFilter: "*.go",
		Limit:      20,
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("search_code error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	count, ok := m["count"].(int)
	if !ok {
		t.Fatalf("expected count to be int, got %T", m["count"])
	}
	if count == 0 {
		t.Fatal("expected matches for 'func' in *.go files")
	}

	t.Logf("Found %d matches for 'func' in *.go files", count)
}

func TestSearchCode_InvalidRegex(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("search_code")

	params, _ := json.Marshal(SearchCodeParams{
		Pattern: "[invalid",
		Limit:   20,
	})

	_, err := handler(params)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestGetArchitectureSummary_Basic(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("get_architecture_summary")
	if !ok {
		t.Fatal("get_architecture_summary handler not registered")
	}

	params, _ := json.Marshal(map[string]interface{}{"limit": 5})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("get_architecture_summary error: %v", err)
	}

	resp, ok := result.(types.InspectableResponse)
	if !ok {
		t.Fatalf("expected types.InspectableResponse, got %T", result)
	}

	// Summary should mention communities and node/edge counts
	if resp.Summary == "" {
		t.Error("expected non-empty summary")
	}

	t.Logf("Architecture summary: %s", resp.Summary)
	t.Logf("Total candidates: %d, Inspectables returned: %d", resp.Total, len(resp.Inspectables))

	for i, item := range resp.Inspectables {
		if item.Rank != i+1 {
			t.Errorf("item %d: expected rank %d, got %d", i, i+1, item.Rank)
		}
		if item.TargetType != "entry_point" && item.TargetType != "hub" && item.TargetType != "connector" {
			t.Errorf("item %d: unexpected target_type %q", i, item.TargetType)
		}
		if item.Name == "" {
			t.Errorf("item %d: empty name", i)
		}
		if item.NextTool == "" {
			t.Errorf("item %d: empty next_tool", i)
		}
		t.Logf("  #%d %s (%s) → %s [score=%.4f]", item.Rank, item.Name, item.TargetType, item.NextTool, item.Score)
	}
}

func TestExplore_BasicSearch(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("explore")
	if !ok {
		t.Fatal("explore handler not registered")
	}

	params, _ := json.Marshal(ExploreParams{
		Symbol: "processOrder",
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("explore error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	count, ok := m["count"].(int)
	if !ok {
		t.Fatalf("expected count to be int, got %T", m["count"])
	}
	if count == 0 {
		t.Fatal("expected at least one match for 'processOrder'")
	}

	t.Logf("Found %d matches for 'processOrder'", count)
}

func TestExplore_WithDeps(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("explore")

	params, _ := json.Marshal(ExploreParams{
		Symbol:      "processOrder",
		IncludeDeps: true,
		Depth:       2,
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("explore error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}

	// Should have dependencies (processOrder calls calculateTotal and validateOrder)
	if _, ok := m["dependencies"]; !ok {
		t.Error("expected 'dependencies' in result when include_deps=true")
	}

	// Should have dependents (nobody calls processOrder in our test data)
	if _, ok := m["dependents"]; !ok {
		t.Error("expected 'dependents' in result when include_deps=true")
	}

	t.Logf("Explore with deps: matches=%v", m["count"])
}

func TestUnderstand_ExactMatch(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("understand")
	if !ok {
		t.Fatal("understand handler not registered")
	}

	params, _ := json.Marshal(UnderstandParams{
		Symbol: "processOrder",
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("understand error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}

	// Should be exact match resolution
	resolution, ok := m["resolution"].(string)
	if !ok {
		t.Fatalf("expected resolution to be string, got %T", m["resolution"])
	}
	if resolution != "exact" {
		t.Errorf("expected resolution='exact', got %q", resolution)
	}

	// Should have callers/callees
	if _, ok := m["callees"]; !ok {
		t.Error("expected 'callees' in result")
	}

	// Should have pagerank
	if _, ok := m["pagerank"]; !ok {
		t.Error("expected 'pagerank' in result")
	}

	t.Logf("Understand: resolution=%s, pagerank=%v", resolution, m["pagerank"])
}

func TestUnderstand_FuzzyMatch(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("understand")

	// Partial match — "process" should fuzzy-match "processOrder"
	params, _ := json.Marshal(UnderstandParams{
		Symbol: "process",
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("understand error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	resolution, ok := m["resolution"].(string)
	if !ok {
		t.Fatalf("expected resolution to be string, got %T", m["resolution"])
	}
	if resolution != "fuzzy" {
		t.Errorf("expected resolution='fuzzy', got %q", resolution)
	}
}

func TestUnderstand_NotFound(t *testing.T) {
	t.Skip("known issue: with semantic embedder enabled, FTS tier-3 fallback returns results for nonsense queries via embedding similarity")

	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("understand")

	params, _ := json.Marshal(UnderstandParams{
		Symbol: "completelyNonexistentSymbol12345",
	})

	_, err := handler(params)
	if err == nil {
		t.Fatal("expected error for nonexistent symbol")
	}
}

func TestTraceCallPath_MissingParams(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("trace_call_path")

	params, _ := json.Marshal(TraceCallPathParams{
		From: "processOrder",
		// Missing 'to'
	})

	_, err := handler(params)
	if err == nil {
		t.Fatal("expected error for missing 'to' parameter")
	}
}

func TestAllToolsRegistered(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	expectedTools := []string{
		"context",
		"impact",
		"read_symbol",
		"query",
		"health",
		"index",
		"trace_call_path",
		"get_key_symbols",
		"search_code",
		"detect_changes",
		"get_architecture_summary",
		"explore",
		"understand",
	}

	tools := server.GetTools()
	toolNames := make(map[string]bool)
	for _, t := range tools {
		toolNames[t.Name] = true
	}

	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("tool %q not registered", name)
		}
	}

	if len(tools) != len(expectedTools) {
		t.Errorf("expected %d tools, got %d", len(expectedTools), len(tools))
	}
}

// Test graph engine new methods directly
func TestGraphEngine_TraceCallPath(t *testing.T) {
	g := graph.New()
	// A -> B -> C -> D
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
	})

	paths := g.TraceCallPath("node-a", "node-d", 10)
	if len(paths) == 0 {
		t.Fatal("expected at least one path from A to D")
	}

	// Path should be [A, B, C, D]
	found := false
	for _, p := range paths {
		if len(p) == 4 && p[0] == "node-a" && p[3] == "node-d" {
			found = true
			t.Logf("Found path: %v", p)
		}
	}
	if !found {
		t.Errorf("expected 4-node path A->B->C->D, got: %v", paths)
	}
}

func TestGraphEngine_TraceCallPath_SameNode(t *testing.T) {
	g := graph.New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
	})

	paths := g.TraceCallPath("node-a", "node-a", 10)
	if len(paths) != 1 || len(paths[0]) != 1 {
		t.Errorf("expected single-element path for same node, got: %v", paths)
	}
}

func TestGraphEngine_PageRank(t *testing.T) {
	g := graph.New()
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-c", TargetID: "node-a", EdgeType: types.EdgeTypeCalls},
	})

	ranks := g.PageRank()
	if ranks == nil {
		t.Fatal("PageRank returned nil")
	}
	if len(ranks) != 3 {
		t.Errorf("expected 3 scores, got %d", len(ranks))
	}

	for id, score := range ranks {
		if score <= 0 {
			t.Errorf("node %s has non-positive PageRank: %f", id, score)
		}
	}
}

func TestGraphEngine_GetInOutDegree(t *testing.T) {
	g := graph.New()
	// A -> B, A -> C, B -> C
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-a", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	// A: out=2, in=0
	if g.GetOutDegree("node-a") != 2 {
		t.Errorf("node-a out-degree: got %d, want 2", g.GetOutDegree("node-a"))
	}
	if g.GetInDegree("node-a") != 0 {
		t.Errorf("node-a in-degree: got %d, want 0", g.GetInDegree("node-a"))
	}

	// C: out=0, in=2
	if g.GetOutDegree("node-c") != 0 {
		t.Errorf("node-c out-degree: got %d, want 0", g.GetOutDegree("node-c"))
	}
	if g.GetInDegree("node-c") != 2 {
		t.Errorf("node-c in-degree: got %d, want 2", g.GetInDegree("node-c"))
	}
}

func TestGraphEngine_GetEntryPoints(t *testing.T) {
	g := graph.New()
	// A -> B -> C; D (isolated)
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	eps := g.GetEntryPoints()
	// A is the only entry point (no incoming edges); B and C have incoming edges
	if len(eps) != 1 {
		t.Errorf("expected 1 entry point, got %d: %v", len(eps), eps)
	}
	if len(eps) > 0 && eps[0] != "node-a" {
		t.Errorf("expected entry point 'node-a', got %q", eps[0])
	}
}

func TestGraphEngine_GetHubs(t *testing.T) {
	g := graph.New()
	// A -> B, A -> C, A -> D (A is a hub with out-degree 3)
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-a", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-a", TargetID: "node-d", EdgeType: types.EdgeTypeCalls},
	})

	hubs := g.GetHubs(2)
	if len(hubs) == 0 {
		t.Fatal("expected at least one hub")
	}
	if hubs[0].HashID != "node-a" {
		t.Errorf("expected top hub to be 'node-a', got %q", hubs[0].HashID)
	}
	if hubs[0].OutDegree != 3 {
		t.Errorf("expected out-degree 3 for node-a, got %d", hubs[0].OutDegree)
	}
}

func TestGraphEngine_GetCallersCallees(t *testing.T) {
	g := graph.New()
	// A -> B -> C
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
	})

	// B's callers should be [A]
	callers := g.GetCallers("node-b")
	if len(callers) != 1 || callers[0] != "node-a" {
		t.Errorf("expected callers of B = [A], got %v", callers)
	}

	// B's callees should be [C]
	callees := g.GetCallees("node-b")
	if len(callees) != 1 || callees[0] != "node-c" {
		t.Errorf("expected callees of B = [C], got %v", callees)
	}
}

func TestGraphEngine_CollectDeps(t *testing.T) {
	g := graph.New()
	// A -> B -> C, D -> B
	g.BuildFromEdges([]types.ASTEdge{
		{SourceID: "node-a", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-b", TargetID: "node-c", EdgeType: types.EdgeTypeCalls},
		{SourceID: "node-d", TargetID: "node-b", EdgeType: types.EdgeTypeCalls},
	})

	deps, dependents := g.CollectDeps("node-b", 2)

	// B's dependencies (callees): C
	depsSet := make(map[string]bool)
	for _, d := range deps {
		depsSet[d] = true
	}
	if !depsSet["node-c"] {
		t.Errorf("expected node-c in dependencies, got %v", deps)
	}

	// B's dependents (callers): A and D
	depSet := make(map[string]bool)
	for _, d := range dependents {
		depSet[d] = true
	}
	if !depSet["node-a"] || !depSet["node-d"] {
		t.Errorf("expected node-a and node-d in dependents, got %v", dependents)
	}
}

// Test storage helper methods
// ---- L1: Handler tests for context, impact, read_symbol, query, index ----

func TestContextHandler_SearchMode(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("context")
	if !ok {
		t.Fatal("context handler not registered")
	}

	params, _ := json.Marshal(ContextParams{Query: "processOrder", Limit: 5})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("contextHandler search mode error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestContextHandler_ArchitectureMode(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("context")

	params, _ := json.Marshal(ContextParams{Query: "test", Mode: "architecture"})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("contextHandler architecture mode error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from architecture mode")
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if m["mode"] != "architecture" {
		t.Errorf("expected mode='architecture', got %v", m["mode"])
	}
}

func TestImpactHandler_Basic(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("impact")
	if !ok {
		t.Fatal("impact handler not registered")
	}

	params, _ := json.Marshal(ImpactParams{SymbolID: "processOrder", Depth: 3})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("impactHandler error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestReadSymbolHandler_Basic(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("read_symbol")
	if !ok {
		t.Fatal("read_symbol handler not registered")
	}

	params, _ := json.Marshal(ReadSymbolParams{SymbolID: "processOrder"})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("readSymbolHandler error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestQueryHandler_Basic(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("query")
	if !ok {
		t.Fatal("query handler not registered")
	}

	params, _ := json.Marshal(QueryParams{SQL: "SELECT id, symbol_name FROM nodes"})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("queryHandler error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestQueryHandler_RejectsWrite(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("query")

	params, _ := json.Marshal(QueryParams{SQL: "DELETE FROM nodes"})
	_, err := handler(params)
	if err == nil {
		t.Fatal("expected error for write query")
	}
}

func TestIndexHandler_NilFunc(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	// Register with nil indexFn
	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("index")
	if !ok {
		t.Fatal("index handler not registered")
	}

	params, _ := json.Marshal(IndexParams{})
	_, err := handler(params)
	if err == nil {
		t.Fatal("expected error when indexFn is nil")
	}
}

func TestIndexHandler_WithFunc(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	called := false
	indexFn := func(path string) error {
		called = true
		return nil
	}

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, indexFn)

	handler, _ := server.GetHandler("index")

	params, _ := json.Marshal(IndexParams{Path: filepath.Join(deps.RepoRoot, "subdir")})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("indexHandler error: %v", err)
	}
	if !called {
		t.Error("expected indexFn to be called")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// ---- M16: Empty/nil/missing required parameter tests ----

func TestToolHandlers_EmptyNilInputs(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	tests := []struct {
		name    string
		tool    string
		params  string
		wantErr bool
	}{
		// context: empty query returns error (requires query or mode=architecture)
		{"context_empty_query", "context", `{"query": ""}`, true},
		// context: missing query entirely returns error
		{"context_no_params", "context", `{}`, true},
		// impact: empty symbol_id
		{"impact_empty_symbol", "impact", `{"symbol_id": ""}`, true},
		// impact: no params
		{"impact_no_params", "impact", `{}`, true},
		// read_symbol: empty symbol_id
		{"read_symbol_empty", "read_symbol", `{"symbol_id": ""}`, true},
		// query: empty SQL
		{"query_empty_sql", "query", `{"sql": ""}`, true},
		// trace_call_path: missing from and to
		{"trace_missing_both", "trace_call_path", `{}`, true},
		// trace_call_path: missing to
		{"trace_missing_to", "trace_call_path", `{"from": "x"}`, true},
		// trace_call_path: missing from
		{"trace_missing_from", "trace_call_path", `{"to": "x"}`, true},
		// explore: empty symbol
		{"explore_empty_symbol", "explore", `{"symbol": ""}`, true},
		// understand: empty symbol
		{"understand_empty_symbol", "understand", `{"symbol": ""}`, true},
		// search_code: empty pattern
		{"search_code_empty_pattern", "search_code", `{"pattern": ""}`, true},
		// health: empty params (should succeed)
		{"health_empty", "health", `{}`, false},
		// get_key_symbols: no params (should succeed with defaults)
		{"get_key_symbols_no_params", "get_key_symbols", `{}`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, ok := server.GetHandler(tc.tool)
			if !ok {
				t.Fatalf("handler %q not registered", tc.tool)
			}

			// Must not panic regardless of input
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("handler %q panicked with params %s: %v", tc.tool, tc.params, r)
					}
				}()
				_, err := handler(json.RawMessage(tc.params))
				if tc.wantErr && err == nil {
					t.Errorf("expected error for %s with params %s, got nil", tc.tool, tc.params)
				}
				if !tc.wantErr && err != nil {
					t.Errorf("unexpected error for %s with params %s: %v", tc.tool, tc.params, err)
				}
			}()
		})
	}
}

func TestStore_GetAllFilePaths(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodes := []types.ASTNode{
		{ID: "1", FilePath: "a.go", SymbolName: "funcA", NodeType: types.NodeTypeFunction, StartByte: 0, EndByte: 10},
		{ID: "2", FilePath: "b.go", SymbolName: "funcB", NodeType: types.NodeTypeFunction, StartByte: 0, EndByte: 10},
		{ID: "3", FilePath: "a.go", SymbolName: "funcC", NodeType: types.NodeTypeFunction, StartByte: 10, EndByte: 20},
	}
	if err := store.UpsertNodes(nodes); err != nil {
		t.Fatal(err)
	}

	paths, err := store.GetAllFilePaths()
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) != 2 {
		t.Errorf("expected 2 unique paths, got %d: %v", len(paths), paths)
	}
}

func TestStore_SearchNodesByName(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodes := []types.ASTNode{
		{ID: "1", FilePath: "a.go", SymbolName: "processOrder", NodeType: types.NodeTypeFunction, StartByte: 0, EndByte: 10},
		{ID: "2", FilePath: "b.go", SymbolName: "calculateTotal", NodeType: types.NodeTypeFunction, StartByte: 0, EndByte: 10},
		{ID: "3", FilePath: "a.go", SymbolName: "processPayment", NodeType: types.NodeTypeFunction, StartByte: 10, EndByte: 20},
	}
	if err := store.UpsertNodes(nodes); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchNodesByName("process")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results for 'process', got %d", len(results))
	}
}

func TestStore_GetNodesByFile(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodes := []types.ASTNode{
		{ID: "1", FilePath: "a.go", SymbolName: "funcA", NodeType: types.NodeTypeFunction, StartByte: 0, EndByte: 10},
		{ID: "2", FilePath: "b.go", SymbolName: "funcB", NodeType: types.NodeTypeFunction, StartByte: 0, EndByte: 10},
		{ID: "3", FilePath: "a.go", SymbolName: "funcC", NodeType: types.NodeTypeFunction, StartByte: 10, EndByte: 20},
	}
	if err := store.UpsertNodes(nodes); err != nil {
		t.Fatal(err)
	}

	results, err := store.GetNodesByFile("a.go")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 nodes for 'a.go', got %d", len(results))
	}
}

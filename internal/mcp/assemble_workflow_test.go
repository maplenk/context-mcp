//go:build fts5

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maplenk/context-mcp/internal/embedding"
	"github.com/maplenk/context-mcp/internal/graph"
	"github.com/maplenk/context-mcp/internal/search"
	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/types"
)

func newCustomToolDeps(t *testing.T, files map[string]string, nodes []types.ASTNode, edges []types.ASTEdge) (ToolDeps, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	for relPath, content := range files {
		absPath := filepath.Join(tmpDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	dbPath := filepath.Join(tmpDir, ".context-mcp", "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	if len(edges) > 0 {
		if err := store.UpsertEdges(edges); err != nil {
			t.Fatalf("UpsertEdges: %v", err)
		}
	}

	graphEngine := graph.New()
	graphEngine.BuildFromEdges(edges)

	pageranks := graphEngine.PageRank()
	betweenness := graphEngine.ComputeBetweenness()
	scores := make([]types.NodeScore, 0, len(nodes))
	for _, node := range nodes {
		scores = append(scores, types.NodeScore{
			NodeID:      node.ID,
			PageRank:    pageranks[node.ID],
			Betweenness: betweenness[node.ID],
		})
	}
	if err := store.UpsertNodeScores(scores); err != nil {
		t.Fatalf("UpsertNodeScores: %v", err)
	}

	embedder := embedding.NewHashEmbedder()
	for _, node := range nodes {
		vec, err := embedder.Embed(node.SymbolName + " " + node.ContentSum)
		if err != nil {
			t.Fatalf("Embed(%s): %v", node.SymbolName, err)
		}
		if err := store.UpsertEmbedding(node.ID, vec); err != nil {
			t.Fatalf("UpsertEmbedding(%s): %v", node.SymbolName, err)
		}
	}

	deps := ToolDeps{
		Store:    store,
		Graph:    graphEngine,
		Search:   search.New(store, embedder, graphEngine),
		RepoRoot: tmpDir,
	}
	return deps, func() { store.Close() }
}

func newRouteQueryDeps(t *testing.T) (ToolDeps, func()) {
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("routes.php", "POST /v1/orders"),
			FilePath:   "routes.php",
			SymbolName: "POST /v1/orders",
			NodeType:   types.NodeTypeRoute,
			ContentSum: "POST /v1/orders v1 order api endpoint",
		},
		{
			ID:         types.GenerateNodeID("routes.php", "GET /v3/orders/{id}"),
			FilePath:   "routes.php",
			SymbolName: "GET /v3/orders/{id}",
			NodeType:   types.NodeTypeRoute,
			ContentSum: "GET /v3/orders/{id} v3 order api endpoint",
		},
		{
			ID:         types.GenerateNodeID("routes_v2.php", "POST /v2/orders/create"),
			FilePath:   "routes_v2.php",
			SymbolName: "POST /v2/orders/create",
			NodeType:   types.NodeTypeRoute,
			ContentSum: "POST /v2/orders/create v2 create order api endpoint",
		},
	}
	return newCustomToolDeps(t, nil, nodes, nil)
}

func TestAssembleContext_TraceWorkflow(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("assemble_context")
	if !ok {
		t.Fatal("assemble_context handler not registered")
	}

	params, _ := json.Marshal(AssembleContextParams{
		Query: "order flow",
		Goal:  "trace_workflow",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("assemble_context error: %v", err)
	}

	resp := result.(AssembleContextResponse)
	if resp.Strategy != "goal=trace_workflow phase_grouped" {
		t.Fatalf("strategy = %q, want workflow strategy", resp.Strategy)
	}
	if len(resp.Items) == 0 {
		t.Fatal("expected assembled items")
	}
	if len(resp.Phases) == 0 {
		t.Fatal("expected populated phases")
	}
	if len(resp.RecommendedSteps) == 0 {
		t.Fatal("expected recommended steps")
	}

	itemIDs := make(map[string]bool, len(resp.Items))
	foundEntry := false
	for _, item := range resp.Items {
		itemIDs[item.ID] = true
		if item.Group == workflowPhaseEntry {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Fatal("expected at least one entry-point item")
	}

	for phase, ids := range resp.Phases {
		if len(ids) == 0 {
			t.Fatalf("phase %q should not be empty", phase)
		}
		for _, id := range ids {
			if !itemIDs[id] {
				t.Fatalf("phase %q references unknown item id %q", phase, id)
			}
		}
	}

	for _, step := range resp.RecommendedSteps {
		if step.Tool == "" || step.Reason == "" || len(step.Args) == 0 {
			t.Fatalf("invalid recommended step: %#v", step)
		}
	}
}

func TestAssembleContext_TraceWorkflow_Defaults(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Query: "processOrder flow",
		Goal:  "trace_workflow",
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(AssembleContextResponse)
	if payload.BudgetTokens != 12000 {
		t.Fatalf("budget_tokens = %d, want 12000", payload.BudgetTokens)
	}
	if payload.Mode != "signatures" {
		t.Fatalf("mode = %q, want signatures", payload.Mode)
	}

	mainCount := 0
	for _, item := range payload.Items {
		if item.FilePath == "main.go" {
			mainCount++
		}
	}
	if mainCount != 3 {
		t.Fatalf("main.go item count = %d, want 3 to confirm workflow max_per_file override", mainCount)
	}
}

func TestAssembleContext_TraceWorkflow_UserOverrides(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Query:        "processOrder flow",
		Goal:         "trace_workflow",
		BudgetTokens: 5000,
		Mode:         "full",
		MaxPerFile:   1,
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(AssembleContextResponse)
	if payload.BudgetTokens != 5000 {
		t.Fatalf("budget_tokens = %d, want 5000", payload.BudgetTokens)
	}
	if payload.Mode != "full" {
		t.Fatalf("mode = %q, want full", payload.Mode)
	}

	mainCount := 0
	for _, item := range payload.Items {
		if item.FilePath == "main.go" {
			mainCount++
		}
	}
	if mainCount != 1 {
		t.Fatalf("main.go item count = %d, want 1 when user sets max_per_file=1", mainCount)
	}
}

func TestClassifyWorkflowPhase_GraphBased(t *testing.T) {
	graphEngine := graph.New()
	route := types.ASTNode{ID: "route", SymbolName: "POST /v1/orders", NodeType: types.NodeTypeRoute}
	controller := types.ASTNode{ID: "controller", SymbolName: "HandleOrder", NodeType: types.NodeTypeMethod}
	repo := types.ASTNode{ID: "repo", SymbolName: "OrderRepository", NodeType: types.NodeTypeClass}

	graphEngine.BuildFromEdges([]types.ASTEdge{
		{SourceID: route.ID, TargetID: controller.ID, EdgeType: types.EdgeTypeHandles},
		{SourceID: controller.ID, TargetID: repo.ID, EdgeType: types.EdgeTypeCalls},
	})

	if got := classifyWorkflowPhase(route, graphEngine, false); got != workflowPhaseEntry {
		t.Fatalf("route phase = %q, want %q", got, workflowPhaseEntry)
	}
	if got := classifyWorkflowPhase(controller, graphEngine, false); got != workflowPhaseCore {
		t.Fatalf("controller phase = %q, want %q", got, workflowPhaseCore)
	}
	if got := classifyWorkflowPhase(repo, graphEngine, false); got != workflowPhaseState {
		t.Fatalf("repo phase = %q, want %q", got, workflowPhaseState)
	}
}

func TestClassifyWorkflowPhase_AsyncSignal(t *testing.T) {
	graphEngine := graph.New()
	graphEngine.BuildFromEdges([]types.ASTEdge{
		{SourceID: "controller", TargetID: "dispatch", EdgeType: types.EdgeTypeCalls},
		{SourceID: "controller", TargetID: "helper", EdgeType: types.EdgeTypeCalls},
	})

	asyncNode := types.ASTNode{ID: "dispatch", SymbolName: "DispatchOrderSync", NodeType: types.NodeTypeFunction}
	plainNode := types.ASTNode{ID: "helper", SymbolName: "OrderFormatter", NodeType: types.NodeTypeFunction}

	if got := classifyWorkflowPhase(asyncNode, graphEngine, true); got != workflowPhaseAsync {
		t.Fatalf("async node phase = %q, want %q", got, workflowPhaseAsync)
	}
	if got := classifyWorkflowPhase(plainNode, graphEngine, true); got != workflowPhaseOther {
		t.Fatalf("plain expanded node phase = %q, want %q", got, workflowPhaseOther)
	}
}

func TestClassifyWorkflowPhase_SparseGraph(t *testing.T) {
	processSrc := "package main\n\nfunc ProcessOrder() error { return nil }\n"
	validateSrc := "package main\n\nfunc ValidateOrder() bool { return true }\n"
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("routes.php", "POST /v1/orders"),
			FilePath:   "routes.php",
			SymbolName: "POST /v1/orders",
			NodeType:   types.NodeTypeRoute,
			ContentSum: "order flow route",
		},
		{
			ID:         types.GenerateNodeID("process.go", "ProcessOrder"),
			FilePath:   "process.go",
			SymbolName: "ProcessOrder",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    uint32(len(processSrc)),
			ContentSum: "order flow processor",
		},
		{
			ID:         types.GenerateNodeID("validate.go", "ValidateOrder"),
			FilePath:   "validate.go",
			SymbolName: "ValidateOrder",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    uint32(len(validateSrc)),
			ContentSum: "order flow validator",
		},
	}
	deps, cleanup := newCustomToolDeps(t, map[string]string{
		"process.go":  processSrc,
		"validate.go": validateSrc,
	}, nodes, nil)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Query: "order flow",
		Goal:  "trace_workflow",
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(AssembleContextResponse)
	if len(payload.Phases) != 1 {
		t.Fatalf("expected only entry-point phases for sparse graph, got %#v", payload.Phases)
	}
	if len(payload.Phases[workflowPhaseEntry]) != 1 {
		t.Fatalf("entry-point phase = %#v, want exactly one route", payload.Phases[workflowPhaseEntry])
	}
	for _, item := range payload.Items {
		if item.FilePath == "routes.php" {
			if item.Group != workflowPhaseEntry {
				t.Fatalf("route group = %q, want %q", item.Group, workflowPhaseEntry)
			}
			continue
		}
		if item.Group != "" {
			t.Fatalf("non-route item group = %q, want empty string in sparse graph fallback", item.Group)
		}
	}
}

func TestRecommendedSteps_TraceWorkflow(t *testing.T) {
	handlerSrc := "package main\n\nfunc HandleOrder() error { return nil }\n"
	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID("routes.php", "POST /v1/orders"),
			FilePath:   "routes.php",
			SymbolName: "POST /v1/orders",
			NodeType:   types.NodeTypeRoute,
			ContentSum: "order flow route",
		},
		{
			ID:         types.GenerateNodeID("handler.go", "HandleOrder"),
			FilePath:   "handler.go",
			SymbolName: "HandleOrder",
			NodeType:   types.NodeTypeMethod,
			StartByte:  0,
			EndByte:    uint32(len(handlerSrc)),
			ContentSum: "handle order flow",
		},
	}
	edges := []types.ASTEdge{
		{SourceID: nodes[0].ID, TargetID: nodes[1].ID, EdgeType: types.EdgeTypeHandles},
	}
	deps, cleanup := newCustomToolDeps(t, map[string]string{"handler.go": handlerSrc}, nodes, edges)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Query: "order flow",
		Goal:  "trace_workflow",
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(AssembleContextResponse)
	if len(payload.RecommendedSteps) == 0 || len(payload.RecommendedSteps) > 3 {
		t.Fatalf("recommended steps = %#v, want 1-3 steps", payload.RecommendedSteps)
	}

	seen := make(map[string]bool)
	foundTraceRoute := false
	foundRefinedAssemble := false
	for _, step := range payload.RecommendedSteps {
		key := recommendedStepKey(step)
		if seen[key] {
			t.Fatalf("duplicate recommended step: %#v", step)
		}
		seen[key] = true

		if step.Tool == "trace_route" {
			foundTraceRoute = true
		}
		if step.Tool == "assemble_context" && strings.Contains(step.Args["query"], "HandleOrder") && step.Args["mode"] == "snippets" {
			foundRefinedAssemble = true
		}
	}
	if !foundTraceRoute {
		t.Fatal("expected trace_route recommendation")
	}
	if !foundRefinedAssemble {
		t.Fatal("expected refined assemble_context recommendation for thin core logic")
	}
}

func TestFindRoutes_TokenizedQuery(t *testing.T) {
	deps, cleanup := newRouteQueryDeps(t)
	defer cleanup()

	result, err := findRoutesHandler(deps, FindRoutesParams{Query: "order", Limit: 5})
	if err != nil {
		t.Fatalf("findRoutesHandler error: %v", err)
	}

	payload := result.(map[string]any)
	routes := payload["routes"].([]routeResultItem)
	if len(routes) == 0 {
		t.Fatal("expected route results")
	}
	if !strings.Contains(strings.ToLower(routes[0].Symbol), "order") {
		t.Fatalf("top route %q should reflect tokenized order query", routes[0].Symbol)
	}
}

func TestFindRoutes_MultiToken(t *testing.T) {
	deps, cleanup := newRouteQueryDeps(t)
	defer cleanup()

	result, err := findRoutesHandler(deps, FindRoutesParams{Query: "create order", Limit: 5})
	if err != nil {
		t.Fatalf("findRoutesHandler error: %v", err)
	}

	payload := result.(map[string]any)
	routes := payload["routes"].([]routeResultItem)
	if len(routes) == 0 {
		t.Fatal("expected route results")
	}
	if routes[0].Symbol != "POST /v2/orders/create" {
		t.Fatalf("top route = %q, want POST /v2/orders/create", routes[0].Symbol)
	}
}

func TestFindRoutes_VersionToken(t *testing.T) {
	deps, cleanup := newRouteQueryDeps(t)
	defer cleanup()

	result, err := findRoutesHandler(deps, FindRoutesParams{Query: "v2 order", Limit: 5})
	if err != nil {
		t.Fatalf("findRoutesHandler error: %v", err)
	}

	payload := result.(map[string]any)
	routes := payload["routes"].([]routeResultItem)
	if len(routes) == 0 {
		t.Fatal("expected route results")
	}
	if !strings.Contains(routes[0].Symbol, "/v2/") {
		t.Fatalf("top route = %q, want a v2 route first", routes[0].Symbol)
	}
}

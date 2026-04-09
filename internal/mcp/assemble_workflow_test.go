//go:build fts5

package mcp

import (
	"encoding/json"
	"fmt"
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

const testMaxUint32 = ^uint32(0)

func testClampUint32Len(s string) uint32 {
	if uint64(len(s)) > uint64(testMaxUint32) {
		return testMaxUint32
	}
	// #nosec G115 -- len(s) is range-checked before conversion for test fixtures.
	return uint32(len(s))
}

func newCustomToolDeps(
	t *testing.T,
	files map[string]string,
	nodes []types.ASTNode,
	edges []types.ASTEdge,
) (deps ToolDeps, cleanup func()) {
	t.Helper()

	tmpDir := t.TempDir()
	for relPath, content := range files {
		absPath := filepath.Join(tmpDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o600); err != nil {
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

	deps = ToolDeps{
		Store:    store,
		Graph:    graphEngine,
		Search:   search.New(store, embedder, graphEngine),
		RepoRoot: tmpDir,
	}
	cleanup = func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close store: %v", err)
		}
	}
	return deps, cleanup
}

func newRouteQueryDeps(t *testing.T) (deps ToolDeps, cleanup func()) {
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
			ID:         types.GenerateNodeID("routes_v2.php", "POST /v2/orders"),
			FilePath:   "routes_v2.php",
			SymbolName: "POST /v2/orders",
			NodeType:   types.NodeTypeRoute,
			ContentSum: "POST /v2/orders v2 order api endpoint",
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

func testNode(filePath, symbolName string, nodeType types.NodeType, content string) types.ASTNode {
	return types.ASTNode{
		ID:         types.GenerateNodeID(filePath, symbolName),
		FilePath:   filePath,
		SymbolName: symbolName,
		NodeType:   nodeType,
		ContentSum: content,
	}
}

type verifyWorkflowFixture struct {
	Route           types.ASTNode
	UnresolvedRoute types.ASTNode
	BrokenRoute     types.ASTNode
	Handler         types.ASTNode
	Core            types.ASTNode
	State           types.ASTNode
	OrphanState     types.ASTNode
	AsyncDirect     types.ASTNode
	AsyncWeak       types.ASTNode
	DetachedEntry   types.ASTNode
	UnreachableCore types.ASTNode
	DetachedState   types.ASTNode
	AsyncTrigger    types.ASTNode
}

func newVerifyWorkflowDeps(t *testing.T) (deps ToolDeps, cleanup func(), fixture verifyWorkflowFixture) {
	t.Helper()

	fixture = verifyWorkflowFixture{
		Route:           testNode("routes.go", "POST /v1/orders", types.NodeTypeRoute, "verified order entry route"),
		UnresolvedRoute: testNode("routes.go", "POST /v1/pending-orders", types.NodeTypeRoute, "route with missing handler"),
		BrokenRoute:     testNode("routes.go", "POST /v1/broken-orders", types.NodeTypeRoute, "route missing handler edge"),
		Handler:         testNode("handlers.go", "HandleOrders", types.NodeTypeMethod, "route handler"),
		Core:            testNode("service.go", "ProcessOrder", types.NodeTypeFunction, "core order processor"),
		State:           testNode("repo.go", "OrderRepository", types.NodeTypeClass, "order repository"),
		OrphanState:     testNode("repo.go", "AuditStore", types.NodeTypeClass, "orphaned state store"),
		AsyncDirect:     testNode("async.go", "PublishOrderEvent", types.NodeTypeFunction, "order event publisher"),
		AsyncWeak:       testNode("async.go", "QueueOrderNotification", types.NodeTypeFunction, "weak async notification queue"),
		DetachedEntry:   testNode("detached.go", "DetachedKickoff", types.NodeTypeFunction, "disconnected kickoff"),
		UnreachableCore: testNode("detached.go", "ReconcileOrders", types.NodeTypeFunction, "disconnected core"),
		DetachedState:   testNode("detached.go", "DetachedStore", types.NodeTypeClass, "detached state"),
		AsyncTrigger:    testNode("async.go", "TriggerNotifications", types.NodeTypeFunction, "non-core async trigger"),
	}

	nodes := []types.ASTNode{
		fixture.Route,
		fixture.UnresolvedRoute,
		fixture.BrokenRoute,
		fixture.Handler,
		fixture.Core,
		fixture.State,
		fixture.OrphanState,
		fixture.AsyncDirect,
		fixture.AsyncWeak,
		fixture.DetachedEntry,
		fixture.UnreachableCore,
		fixture.DetachedState,
		fixture.AsyncTrigger,
	}
	edges := []types.ASTEdge{
		{SourceID: fixture.Route.ID, TargetID: fixture.Handler.ID, EdgeType: types.EdgeTypeHandles},
		{SourceID: fixture.UnresolvedRoute.ID, TargetID: "missing-handler", EdgeType: types.EdgeTypeHandles},
		{SourceID: fixture.Handler.ID, TargetID: fixture.Core.ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: fixture.Core.ID, TargetID: fixture.State.ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: fixture.Core.ID, TargetID: fixture.AsyncDirect.ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: fixture.AsyncTrigger.ID, TargetID: fixture.AsyncWeak.ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: fixture.DetachedEntry.ID, TargetID: fixture.UnreachableCore.ID, EdgeType: types.EdgeTypeCalls},
		{SourceID: fixture.UnreachableCore.ID, TargetID: fixture.DetachedState.ID, EdgeType: types.EdgeTypeCalls},
	}

	deps, cleanup = newCustomToolDeps(t, nil, nodes, edges)
	return deps, cleanup, fixture
}

func verifiedWorkflowItemByID(t *testing.T, items []types.VerifiedWorkflowItem, id string) types.VerifiedWorkflowItem {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("verified workflow item %q not found", id)
	return types.VerifiedWorkflowItem{}
}

func recommendedStepByTool(steps []types.RecommendedStep, tool string) (types.RecommendedStep, bool) {
	for _, step := range steps {
		if step.Tool == tool {
			return step, true
		}
	}
	return types.RecommendedStep{}, false
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
			EndByte:    testClampUint32Len(processSrc),
			ContentSum: "order flow processor",
		},
		{
			ID:         types.GenerateNodeID("validate.go", "ValidateOrder"),
			FilePath:   "validate.go",
			SymbolName: "ValidateOrder",
			NodeType:   types.NodeTypeFunction,
			StartByte:  0,
			EndByte:    testClampUint32Len(validateSrc),
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

func TestVerifyWorkflow_FullChain(t *testing.T) {
	deps, cleanup, fixture := newVerifyWorkflowDeps(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal:    "verify_workflow",
		Targets: []string{fixture.Route.ID, fixture.Handler.ID, fixture.Core.ID},
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	routeItem := verifiedWorkflowItemByID(t, payload.Items, fixture.Route.ID)
	if routeItem.Verification != "confirmed" || routeItem.Confidence != 1.0 {
		t.Fatalf("route verification = %#v, want confirmed with confidence 1.0", routeItem)
	}
	if got, want := strings.Join(routeItem.Path, " -> "), fixture.Route.SymbolName+" -> "+fixture.Handler.SymbolName; got != want {
		t.Fatalf("route path = %q, want %q", got, want)
	}

	handlerItem := verifiedWorkflowItemByID(t, payload.Items, fixture.Handler.ID)
	if handlerItem.Verification != "confirmed" || handlerItem.Confidence != 1.0 {
		t.Fatalf("handler verification = %#v, want confirmed with confidence 1.0", handlerItem)
	}
	if got, want := strings.Join(handlerItem.Path, " -> "), fixture.Route.SymbolName+" -> "+fixture.Handler.SymbolName; got != want {
		t.Fatalf("handler path = %q, want %q", got, want)
	}

	coreItem := verifiedWorkflowItemByID(t, payload.Items, fixture.Core.ID)
	if coreItem.Verification != "confirmed" || coreItem.Confidence != 1.0 {
		t.Fatalf("core verification = %#v, want confirmed with confidence 1.0", coreItem)
	}
	if got, want := strings.Join(coreItem.Path, " -> "), fixture.Route.SymbolName+" -> "+fixture.Handler.SymbolName+" -> "+fixture.Core.SymbolName; got != want {
		t.Fatalf("core path = %q, want %q", got, want)
	}
}

func TestVerifyWorkflow_UnreachableCore(t *testing.T) {
	deps, cleanup, fixture := newVerifyWorkflowDeps(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal:    "verify_workflow",
		Targets: []string{fixture.Route.ID, fixture.Handler.ID, fixture.UnreachableCore.ID},
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	item := verifiedWorkflowItemByID(t, payload.Items, fixture.UnreachableCore.ID)
	if item.Verification != "unreachable" || item.Confidence != 0.3 {
		t.Fatalf("unreachable core = %#v, want unreachable with confidence 0.3", item)
	}
	if len(item.Path) != 0 {
		t.Fatalf("unreachable core path = %#v, want no path", item.Path)
	}
}

func TestVerifyWorkflow_BrokenEntryPoint(t *testing.T) {
	deps, cleanup, fixture := newVerifyWorkflowDeps(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal:    "verify_workflow",
		Targets: []string{fixture.BrokenRoute.ID},
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	item := verifiedWorkflowItemByID(t, payload.Items, fixture.BrokenRoute.ID)
	if item.Verification != "broken" || item.Confidence != 0.0 {
		t.Fatalf("broken entry = %#v, want broken with confidence 0.0", item)
	}
	if item.FailReason != "no handler edge" {
		t.Fatalf("broken entry fail reason = %q, want no handler edge", item.FailReason)
	}
}

func TestVerifyWorkflow_OrphanedState(t *testing.T) {
	deps, cleanup, fixture := newVerifyWorkflowDeps(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal:    "verify_workflow",
		Targets: []string{fixture.Route.ID, fixture.Handler.ID, fixture.Core.ID, fixture.OrphanState.ID},
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	item := verifiedWorkflowItemByID(t, payload.Items, fixture.OrphanState.ID)
	if item.Verification != "orphaned" || item.Confidence != 0.3 {
		t.Fatalf("orphaned state = %#v, want orphaned with confidence 0.3", item)
	}
}

func TestVerifyWorkflow_NoConfirmedEntries(t *testing.T) {
	deps, cleanup, fixture := newVerifyWorkflowDeps(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal:    "verify_workflow",
		Targets: []string{fixture.BrokenRoute.ID, fixture.UnresolvedRoute.ID, fixture.UnreachableCore.ID},
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	item := verifiedWorkflowItemByID(t, payload.Items, fixture.UnreachableCore.ID)
	if item.Verification != "unresolved" || item.Confidence != 0.3 {
		t.Fatalf("core without confirmed entries = %#v, want unresolved with confidence 0.3", item)
	}
}

func TestVerifyWorkflow_RecommendedSteps(t *testing.T) {
	deps, cleanup, fixture := newVerifyWorkflowDeps(t)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal:    "verify_workflow",
		Targets: []string{fixture.UnresolvedRoute.ID, fixture.Route.ID, fixture.Handler.ID, fixture.UnreachableCore.ID, fixture.AsyncWeak.ID},
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	if len(payload.RecommendedSteps) != 3 {
		t.Fatalf("recommended steps = %#v, want exactly 3 capped steps", payload.RecommendedSteps)
	}

	traceRoute, ok := recommendedStepByTool(payload.RecommendedSteps, "trace_route")
	if !ok {
		t.Fatal("expected trace_route recommendation")
	}
	if traceRoute.Args["route"] != fixture.UnresolvedRoute.SymbolName {
		t.Fatalf("trace_route args = %#v, want route %q", traceRoute.Args, fixture.UnresolvedRoute.SymbolName)
	}

	tracePath, ok := recommendedStepByTool(payload.RecommendedSteps, "trace_call_path")
	if !ok {
		t.Fatal("expected trace_call_path recommendation")
	}
	if tracePath.Args["from"] != fixture.Handler.ID || tracePath.Args["to"] != fixture.UnreachableCore.ID {
		t.Fatalf("trace_call_path args = %#v, want from=%q to=%q", tracePath.Args, fixture.Handler.ID, fixture.UnreachableCore.ID)
	}

	readStep, ok := recommendedStepByTool(payload.RecommendedSteps, "read_symbol")
	if !ok {
		t.Fatal("expected read_symbol recommendation")
	}
	if readStep.Args["symbol_id"] != fixture.AsyncWeak.ID || readStep.Args["mode"] != "signature" {
		t.Fatalf("read_symbol args = %#v, want weak async signature lookup", readStep.Args)
	}
}

func TestVerifyWorkflow_TargetCap(t *testing.T) {
	nodes := make([]types.ASTNode, 0, 31)
	targets := make([]string, 0, 31)
	for i := range 31 {
		node := testNode(
			fmt.Sprintf("caps/%02d.go", i),
			fmt.Sprintf("CapState%02d", i),
			types.NodeTypeClass,
			fmt.Sprintf("cap state %02d", i),
		)
		nodes = append(nodes, node)
		targets = append(targets, node.ID)
	}

	deps, cleanup := newCustomToolDeps(t, nil, nodes, nil)
	defer cleanup()

	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal:    "verify_workflow",
		Targets: targets,
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	if !payload.Truncated {
		t.Fatal("expected verify_workflow response to be truncated")
	}
	if len(payload.Items) != 30 {
		t.Fatalf("item count = %d, want 30", len(payload.Items))
	}
	if payload.Items[29].ID != targets[29] {
		t.Fatalf("last kept target = %q, want %q", payload.Items[29].ID, targets[29])
	}
}

func TestVerifyWorkflow_OutputOrder(t *testing.T) {
	deps, cleanup, fixture := newVerifyWorkflowDeps(t)
	defer cleanup()

	missingID := "missing-node-id"
	resp, err := assembleContextHandler(deps, AssembleContextParams{
		Goal: "verify_workflow",
		Targets: []string{
			fixture.AsyncWeak.ID,
			fixture.Handler.ID,
			fixture.BrokenRoute.ID,
			missingID,
			fixture.Core.ID,
			fixture.OrphanState.ID,
			fixture.Route.ID,
		},
	})
	if err != nil {
		t.Fatalf("assembleContextHandler error: %v", err)
	}

	payload := resp.(types.VerifyWorkflowResponse)
	got := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		got = append(got, item.ID)
	}
	want := []string{
		fixture.BrokenRoute.ID,
		fixture.Route.ID,
		fixture.Handler.ID,
		fixture.Core.ID,
		fixture.OrphanState.ID,
		fixture.AsyncWeak.ID,
		missingID,
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("output order = %#v, want %#v", got, want)
	}
}

func TestRecommendedSteps_TraceWorkflow(t *testing.T) {
	t.Run("cap_and_verify_step", func(t *testing.T) {
		items := []AssembleContextItem{
			{ID: "route-id", Name: "POST /v1/orders", FilePath: "routes.go", Group: workflowPhaseEntry},
			{ID: "core-id", Name: "ProcessOrder", FilePath: "service.go", Group: workflowPhaseCore},
			{ID: "async-id", Name: "QueueOrderNotification", FilePath: "async.go", Group: workflowPhaseAsync},
			{ID: "state-id", Name: "OrderRepository", FilePath: "repo.go", Group: workflowPhaseState},
		}

		steps := buildWorkflowRecommendedSteps(items)
		if len(steps) != 4 {
			t.Fatalf("recommended steps = %#v, want 4 capped steps", steps)
		}

		verifyStep, ok := recommendedStepByTool(steps, "assemble_context")
		if !ok {
			t.Fatal("expected verify_workflow assemble_context recommendation")
		}
		if verifyStep.Args["goal"] != "verify_workflow" || verifyStep.Args["targets"] != "route-id,core-id,async-id,state-id" {
			t.Fatalf("verify step args = %#v, want verify_workflow targets", verifyStep.Args)
		}

		traceRoute, ok := recommendedStepByTool(steps, "trace_route")
		if !ok || traceRoute.Args["route"] != "POST /v1/orders" {
			t.Fatalf("trace_route step = %#v, want route recommendation", traceRoute)
		}

		foundCoreFlow := false
		foundAsyncSignature := false
		for _, step := range steps {
			if step.Tool == "read_symbol" && step.Args["symbol_id"] == "core-id" && step.Args["mode"] == "flow_summary" {
				foundCoreFlow = true
			}
			if step.Tool == "read_symbol" && step.Args["symbol_id"] == "async-id" && step.Args["mode"] == "signature" {
				foundAsyncSignature = true
			}
		}
		if !foundCoreFlow {
			t.Fatal("expected flow_summary suggestion for core logic")
		}
		if !foundAsyncSignature {
			t.Fatal("expected signature suggestion for async logic")
		}
	})

	t.Run("state_data_mapping", func(t *testing.T) {
		items := []AssembleContextItem{
			{ID: "route-id", Name: "POST /v1/orders", FilePath: "routes.go", Group: workflowPhaseEntry},
			{ID: "core-id", Name: "ProcessOrder", FilePath: "service.go", Group: workflowPhaseCore},
			{ID: "state-id", Name: "OrderRepository", FilePath: "repo.go", Group: workflowPhaseState},
		}

		steps := buildWorkflowRecommendedSteps(items)
		listStep, ok := recommendedStepByTool(steps, "list_file_symbols")
		if !ok {
			t.Fatal("expected list_file_symbols recommendation for state/data phase")
		}
		if listStep.Args["file_path"] != "repo.go" {
			t.Fatalf("list_file_symbols args = %#v, want repo.go", listStep.Args)
		}
	})
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

func TestFindRoutes_VersionBoost(t *testing.T) {
	deps, cleanup := newRouteQueryDeps(t)
	defer cleanup()

	result, err := findRoutesHandler(deps, FindRoutesParams{Query: "v1 orders", Limit: 5})
	if err != nil {
		t.Fatalf("findRoutesHandler error: %v", err)
	}

	payload := result.(map[string]any)
	routes := payload["routes"].([]routeResultItem)
	if len(routes) == 0 {
		t.Fatal("expected route results")
	}
	if !strings.Contains(routes[0].Symbol, "/v1/") {
		t.Fatalf("top route = %q, want a v1 route first", routes[0].Symbol)
	}
}

func TestFindRoutes_NoVersionGrouping(t *testing.T) {
	deps, cleanup := newRouteQueryDeps(t)
	defer cleanup()

	result, err := findRoutesHandler(deps, FindRoutesParams{Query: "orders", Limit: 10})
	if err != nil {
		t.Fatalf("findRoutesHandler error: %v", err)
	}

	payload := result.(map[string]any)
	routes := payload["routes"].([]routeResultItem)
	if len(routes) != 3 {
		t.Fatalf("route count = %d, want 3 grouped routes", len(routes))
	}

	baseCount := 0
	for _, route := range routes {
		if stripVersionPrefix(route.Path) == "/orders" {
			baseCount++
		}
	}
	if baseCount != 1 {
		t.Fatalf("base /orders routes = %d, want 1 after grouping", baseCount)
	}
}

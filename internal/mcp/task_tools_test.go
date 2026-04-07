//go:build fts5

package mcp

import (
	"encoding/json"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestCompareSymbols(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("compare_symbols")
	if !ok {
		t.Fatal("compare_symbols handler not registered")
	}

	params, _ := json.Marshal(CompareSymbolsParams{
		Left:  "processOrder",
		Right: "validateOrder",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("compare_symbols error: %v", err)
	}

	payload := result.(map[string]any)
	if payload["left"] == nil || payload["right"] == nil {
		t.Fatal("expected left/right summaries")
	}
	if payload["shared_callees"] == nil {
		t.Fatal("expected shared_callees field")
	}
}

func TestFindRoutes(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("find_routes")
	if !ok {
		t.Fatal("find_routes handler not registered")
	}

	params, _ := json.Marshal(FindRoutesParams{Query: "orders", Limit: 5})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("find_routes error: %v", err)
	}

	payload := result.(map[string]any)
	if payload["count"].(int) == 0 {
		t.Fatal("expected route results")
	}
	if payload["include_handlers"] != true {
		t.Fatalf("expected include_handlers=true by default, got %#v", payload["include_handlers"])
	}
}

func TestFindRoutes_ExplicitlyDisablesHandlers(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("find_routes")
	if !ok {
		t.Fatal("find_routes handler not registered")
	}

	params, _ := json.Marshal(FindRoutesParams{
		Query:           "orders",
		Limit:           5,
		IncludeHandlers: boolPtr(false),
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("find_routes error: %v", err)
	}

	payload := result.(map[string]any)
	if payload["include_handlers"] != false {
		t.Fatalf("expected include_handlers=false, got %#v", payload["include_handlers"])
	}

	routes := payload["routes"].([]routeResultItem)
	if len(routes) == 0 {
		t.Fatal("expected route results")
	}
	if routes[0].Handler != nil {
		t.Fatalf("expected compact route items without handler payload, got %#v", routes[0].Handler)
	}
}

func TestTraceRoute(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("trace_route")
	if !ok {
		t.Fatal("trace_route handler not registered")
	}

	params, _ := json.Marshal(TraceRouteParams{Route: "POST /v1/orders", Depth: 2})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("trace_route error: %v", err)
	}

	payload := result.(map[string]any)
	if payload["route"] == nil {
		t.Fatal("expected route payload")
	}
	if payload["handler"] == nil {
		t.Fatal("expected handler payload")
	}
}

func TestCompareRoutes(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("compare_routes")
	if !ok {
		t.Fatal("compare_routes handler not registered")
	}

	params, _ := json.Marshal(CompareRoutesParams{
		Left:  "POST /v1/orders",
		Right: "GET /v3/orders/{id}",
		Depth: 2,
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("compare_routes error: %v", err)
	}

	payload := result.(map[string]any)
	if payload["left"] == nil || payload["right"] == nil {
		t.Fatal("expected compared route payloads")
	}
}

func TestAssembleContext_TaskAliasAndGoal(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("assemble_context")
	if !ok {
		t.Fatal("assemble_context handler not registered")
	}

	params, _ := json.Marshal(AssembleContextParams{
		Task:    "orders",
		Goal:    "trace_route",
		Targets: []string{"POST /v1/orders"},
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("assemble_context error: %v", err)
	}

	resp := result.(AssembleContextResponse)
	if resp.Query != "orders" {
		t.Fatalf("query = %q, want %q", resp.Query, "orders")
	}
	if resp.Strategy == "" {
		t.Fatal("expected strategy for goal-aware assembly")
	}
	if len(resp.Items) == 0 {
		t.Fatal("expected assembled items")
	}
	if resp.Items[0].Group == "" {
		t.Fatal("expected goal-aware item grouping")
	}
}

func TestAssembleContext_TaskAliasAllowedBySchema(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	var assemble ToolDefinition
	found := false
	for _, tool := range server.GetTools() {
		if tool.Name == "assemble_context" {
			assemble = tool
			found = true
			break
		}
	}
	if !found {
		t.Fatal("assemble_context tool definition not found")
	}

	schema, ok := assemble.InputSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected schema type %T", assemble.InputSchema)
	}
	required, _ := schema["required"].([]string)
	for _, field := range required {
		if field == "query" {
			t.Fatal("query should not be schema-required so deprecated task alias can still pass MCP validation")
		}
	}
}

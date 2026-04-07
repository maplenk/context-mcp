//go:build fts5

package mcp

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestMinimalProfile_ToolCount(t *testing.T) {
	// Only discover_tools, execute_tool, health should be in the minimal profile
	minimalTools := []string{"discover_tools", "execute_tool", "health"}
	for _, name := range minimalTools {
		if !isToolInProfile(name, "minimal") {
			t.Errorf("expected %q to be in minimal profile", name)
		}
	}

	// These should NOT be in the minimal profile
	nonMinimalTools := []string{
		"context", "impact", "read_symbol", "list_file_symbols",
		"query", "index", "trace_call_path", "get_key_symbols",
		"search_code", "detect_changes", "get_architecture_summary",
		"explore", "understand", "assemble_context", "checkpoint_context",
		"read_delta",
	}
	for _, name := range nonMinimalTools {
		if isToolInProfile(name, "minimal") {
			t.Errorf("expected %q NOT to be in minimal profile", name)
		}
	}
}

func TestDiscoverTools_InspectionBundle(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}

	params, _ := json.Marshal(DiscoverToolsParams{
		Need: "find where orders are handled",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("discover_tools error: %v", err)
	}

	resp, ok := result.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result)
	}

	if resp.Bundle != "inspection" && resp.Bundle != "inspection+change_analysis" {
		t.Errorf("expected inspection bundle, got %q", resp.Bundle)
	}

	if resp.ActivatedCount == 0 {
		t.Error("expected at least one tool to be activated")
	}

	// Verify at least "context" was activated (it's in the inspection bundle)
	found := false
	for _, a := range resp.Activated {
		if a.Name == "context" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'context' to be activated in inspection bundle")
	}
}

func TestDiscoverTools_ChangeAnalysisBundle(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}

	params, _ := json.Marshal(DiscoverToolsParams{
		Need: "what will break if I change this",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("discover_tools error: %v", err)
	}

	resp, ok := result.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result)
	}

	// Should match change_analysis keywords (break, change)
	if resp.ActivatedCount == 0 {
		t.Error("expected at least one tool to be activated")
	}

	// Check that impact or detect_changes was activated
	found := false
	for _, a := range resp.Activated {
		if a.Name == "impact" || a.Name == "detect_changes" || a.Name == "trace_call_path" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a change_analysis tool to be activated")
	}
}

func TestDiscoverTools_ArchitectureBundle(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}

	params, _ := json.Marshal(DiscoverToolsParams{
		Need: "show me the architecture overview",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("discover_tools error: %v", err)
	}

	resp, ok := result.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result)
	}

	if resp.ActivatedCount == 0 {
		t.Error("expected at least one tool to be activated")
	}

	// Should contain architecture tools
	found := false
	for _, a := range resp.Activated {
		if a.Name == "get_architecture_summary" || a.Name == "explore" || a.Name == "get_key_symbols" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected an architecture tool to be activated")
	}
}

func TestDiscoverTools_AssemblyBundle(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}

	params, _ := json.Marshal(DiscoverToolsParams{
		Need: "assemble context with token budget",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("discover_tools error: %v", err)
	}

	resp, ok := result.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result)
	}

	if resp.ActivatedCount == 0 {
		t.Error("expected at least one tool to be activated")
	}

	// Should contain assembly tools
	found := false
	for _, a := range resp.Activated {
		if a.Name == "assemble_context" || a.Name == "checkpoint_context" || a.Name == "read_delta" || a.Name == "search_code" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected an assembly tool to be activated")
	}
}

func TestDiscoverTools_DirectActivate(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}

	params, _ := json.Marshal(DiscoverToolsParams{
		Need:     "activate specific tools",
		Activate: []string{"context", "impact"},
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("discover_tools error: %v", err)
	}

	resp, ok := result.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result)
	}

	if resp.Bundle != "direct" {
		t.Errorf("expected bundle 'direct', got %q", resp.Bundle)
	}

	if resp.ActivatedCount != 2 {
		t.Errorf("expected 2 activated tools, got %d", resp.ActivatedCount)
	}

	activatedNames := make([]string, len(resp.Activated))
	for i, a := range resp.Activated {
		activatedNames[i] = a.Name
	}
	sort.Strings(activatedNames)

	if len(activatedNames) != 2 || activatedNames[0] != "context" || activatedNames[1] != "impact" {
		t.Errorf("expected [context, impact], got %v", activatedNames)
	}
}

func TestDiscoverTools_ActivateCap(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}

	// Try to activate 7 tools (over the 5 limit)
	params, _ := json.Marshal(DiscoverToolsParams{
		Need:     "activate many tools",
		Activate: []string{"context", "impact", "read_symbol", "query", "explore", "understand", "search_code"},
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("discover_tools error: %v", err)
	}

	resp, ok := result.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result)
	}

	if resp.ActivatedCount > 5 {
		t.Errorf("expected at most 5 activated tools, got %d", resp.ActivatedCount)
	}

	if !resp.ActivationCapped {
		t.Error("expected activation_capped to be true")
	}
}

func TestDiscoverTools_Idempotent(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}

	// First call: activate inspection bundle
	params, _ := json.Marshal(DiscoverToolsParams{
		Need: "find where orders are handled",
	})
	result1, err := handler(params)
	if err != nil {
		t.Fatalf("first discover_tools call error: %v", err)
	}
	resp1, ok := result1.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result1)
	}
	if resp1.ActivatedCount == 0 {
		t.Fatal("first call should activate tools")
	}

	// Second call with same need: should find everything already active
	result2, err := handler(params)
	if err != nil {
		t.Fatalf("second discover_tools call error: %v", err)
	}
	resp2, ok := result2.(*DiscoverToolsResponse)
	if !ok {
		t.Fatalf("expected *DiscoverToolsResponse, got %T", result2)
	}
	if resp2.ActivatedCount != 0 {
		t.Errorf("second call should activate 0 tools, got %d", resp2.ActivatedCount)
	}
	if len(resp2.AlreadyActive) == 0 {
		t.Error("second call should report already_active tools")
	}
}

func TestExecuteTool_Proxy(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	// First activate the context tool
	discoverHandler, ok := srv.GetHandler("discover_tools")
	if !ok {
		t.Fatal("discover_tools handler not registered")
	}
	activateParams, _ := json.Marshal(DiscoverToolsParams{
		Need:     "activate",
		Activate: []string{"context"},
	})
	_, err := discoverHandler(activateParams)
	if err != nil {
		t.Fatalf("discover_tools error: %v", err)
	}

	// Now use execute_tool to call context (which is activated)
	handler, ok := srv.GetHandler("execute_tool")
	if !ok {
		t.Fatal("execute_tool handler not registered")
	}

	params, _ := json.Marshal(ExecuteToolParams{
		Name: "context",
		Args: map[string]any{"query": "processOrder"},
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("execute_tool error: %v", err)
	}

	// Tool is activated, so the result should NOT be wrapped in a proxy_warning map.
	// The context handler returns types.InspectableResponse, not map[string]interface{},
	// so a non-map result (or a map without proxy_warning) is correct.
	if resultMap, ok := result.(map[string]interface{}); ok {
		if _, hasWarning := resultMap["proxy_warning"]; hasWarning {
			t.Error("expected no proxy_warning for activated tool")
		}
	}
	// If result is not a map, that's fine — it means no proxy wrapping occurred
	if result == nil {
		t.Error("expected non-nil result")
	}
}

func TestExecuteTool_ProxyWarning(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	handler, ok := srv.GetHandler("execute_tool")
	if !ok {
		t.Fatal("execute_tool handler not registered")
	}

	// Call context without activating it first — should be blocked by profile check
	params, _ := json.Marshal(ExecuteToolParams{
		Name: "context",
		Args: map[string]any{"query": "processOrder"},
	})
	_, err := handler(params)
	if err == nil {
		t.Fatal("expected error for tool not in minimal profile, got nil")
	}
	if !strings.Contains(err.Error(), "not available in the") {
		t.Errorf("expected profile-block error, got: %v", err)
	}

	// Call health which IS in the minimal profile — should succeed with proxy_warning
	// (health is registered in minimal profile but may not be SDK-activated yet)
	healthParams, _ := json.Marshal(ExecuteToolParams{
		Name: "health",
		Args: map[string]any{},
	})
	result, err := handler(healthParams)
	if err != nil {
		t.Fatalf("execute_tool error for profile-allowed tool: %v", err)
	}
	// health is in the minimal profile and should be activated, so no proxy_warning expected
	_ = result
}

func TestSchemaFootprint_Minimal(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	// Measure minimal profile
	deps.Profile = "minimal"
	minSrv := NewServerWithIO(nil, nil)
	RegisterTools(minSrv, deps, nil)

	// Get the tool definitions for minimal tools
	minTools := minSrv.GetTools()
	var minimalSchemaTools []ToolDefinition
	for _, td := range minTools {
		if isToolInProfile(td.Name, "minimal") {
			minimalSchemaTools = append(minimalSchemaTools, td)
		}
	}

	minimalJSON, err := json.Marshal(minimalSchemaTools)
	if err != nil {
		t.Fatalf("marshal minimal schemas: %v", err)
	}

	if len(minimalJSON) > 2400 {
		t.Errorf("minimal profile schema footprint %d bytes exceeds 2400 bytes", len(minimalJSON))
	}

	// Measure core profile for relative comparison
	deps.Profile = "core"
	coreSrv := NewServerWithIO(nil, nil)
	RegisterTools(coreSrv, deps, nil)

	coreTools := coreSrv.GetTools()
	var coreSchemaTools []ToolDefinition
	for _, td := range coreTools {
		if isToolInProfile(td.Name, "core") {
			coreSchemaTools = append(coreSchemaTools, td)
		}
	}

	coreJSON, err := json.Marshal(coreSchemaTools)
	if err != nil {
		t.Fatalf("marshal core schemas: %v", err)
	}

	ratio := float64(len(minimalJSON)) / float64(len(coreJSON))
	if ratio > 0.35 {
		t.Errorf("minimal (%d bytes) is %.1f%% of core (%d bytes), expected < 35%%",
			len(minimalJSON), ratio*100, len(coreJSON))
	}
}

func TestDeferredRegistration(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()
	deps.Profile = "minimal"

	srv := NewServerWithIO(nil, nil)
	RegisterTools(srv, deps, nil)

	// Verify there are pending tools
	pending := srv.ListPending()
	if len(pending) == 0 {
		t.Fatal("expected pending tools in minimal profile")
	}

	// Verify "context" is pending (it's not in the minimal profile)
	found := false
	for _, name := range pending {
		if name == "context" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'context' in pending tools, got %v", pending)
	}

	// Verify "context" is not activated
	if srv.IsActivated("context") {
		t.Error("'context' should not be activated yet")
	}

	// Activate it
	if !srv.ActivateTool("context") {
		t.Error("ActivateTool('context') should return true")
	}

	// Now it should be activated
	if !srv.IsActivated("context") {
		t.Error("'context' should be activated now")
	}

	// And no longer pending
	pendingAfter := srv.ListPending()
	for _, name := range pendingAfter {
		if name == "context" {
			t.Error("'context' should no longer be in pending after activation")
		}
	}

	// Second activation should return false (already active)
	if srv.ActivateTool("context") {
		t.Error("second ActivateTool('context') should return false")
	}
}

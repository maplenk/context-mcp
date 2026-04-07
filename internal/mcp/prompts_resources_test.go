//go:build fts5

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestPromptsRegistered verifies that RegisterPrompts registers all 5 prompts
// and they can be listed via the MCP protocol.
func TestPromptsRegistered(t *testing.T) {
	_, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterPrompts(server)

	// Verify prompts are registered by checking via protocol
	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "prompts/list", map[string]interface{}{})

	srv := NewServerWithIO(input, output)
	RegisterPrompts(srv)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	// Find prompts/list response (id=2)
	var promptsResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			promptsResp = r
			break
		}
	}
	if promptsResp == nil {
		t.Fatalf("no response with id=2; got %d responses", len(responses))
	}

	result, ok := promptsResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", promptsResp)
	}
	prompts, ok := result["prompts"].([]interface{})
	if !ok {
		t.Fatalf("prompts field missing or wrong type: %v", result)
	}

	expectedPrompts := []string{
		"review_changes",
		"trace_impact",
		"prepare_fix_context",
		"onboard_repo",
		"collect_minimal_context",
	}

	promptNames := make(map[string]bool)
	for _, p := range prompts {
		pm, _ := p.(map[string]interface{})
		name, _ := pm["name"].(string)
		promptNames[name] = true
	}

	for _, expected := range expectedPrompts {
		if !promptNames[expected] {
			t.Errorf("expected prompt %q not found in prompts/list response", expected)
		}
	}

	if len(prompts) != len(expectedPrompts) {
		t.Errorf("expected %d prompts, got %d", len(expectedPrompts), len(prompts))
	}
}

// TestResourcesRegistered verifies that RegisterResources registers all 4 resources
// and they can be listed via the MCP protocol.
func TestResourcesRegistered(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "resources/list", map[string]interface{}{})

	srv := NewServerWithIO(input, output)
	RegisterResources(srv, deps)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	// Find resources/list response (id=2)
	var resourcesResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			resourcesResp = r
			break
		}
	}
	if resourcesResp == nil {
		t.Fatalf("no response with id=2; got %d responses", len(responses))
	}

	result, ok := resourcesResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", resourcesResp)
	}
	resources, ok := result["resources"].([]interface{})
	if !ok {
		t.Fatalf("resources field missing or wrong type: %v", result)
	}

	expectedResources := []string{
		"context-mcp://repo_summary",
		"context-mcp://index_stats",
		"context-mcp://changed_symbols",
		"context-mcp://hot_paths",
	}

	resourceURIs := make(map[string]bool)
	for _, r := range resources {
		rm, _ := r.(map[string]interface{})
		uri, _ := rm["uri"].(string)
		resourceURIs[uri] = true
	}

	for _, expected := range expectedResources {
		if !resourceURIs[expected] {
			t.Errorf("expected resource %q not found in resources/list response", expected)
		}
	}

	if len(resources) != len(expectedResources) {
		t.Errorf("expected %d resources, got %d", len(expectedResources), len(resources))
	}
}

// TestPromptGetReviewChanges verifies the review_changes prompt returns a valid message.
func TestPromptGetReviewChanges(t *testing.T) {
	_, cleanup := setupTestEnv(t)
	defer cleanup()

	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "prompts/get", map[string]interface{}{
		"name":      "review_changes",
		"arguments": map[string]interface{}{"since": "main"},
	})

	srv := NewServerWithIO(input, output)
	RegisterPrompts(srv)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	var promptResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			promptResp = r
			break
		}
	}
	if promptResp == nil {
		t.Fatalf("no response with id=2")
	}

	result, ok := promptResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", promptResp)
	}

	messages, ok := result["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing or empty: %v", result)
	}

	msg, _ := messages[0].(map[string]interface{})
	content, _ := msg["content"].(map[string]interface{})
	text, _ := content["text"].(string)

	if !strings.Contains(text, "main") {
		t.Errorf("expected prompt text to contain 'main', got %q", text)
	}
	if !strings.Contains(text, "detect_changes") {
		t.Errorf("expected prompt text to contain 'detect_changes', got %q", text)
	}
}

// TestPromptGetOnboardRepo verifies the onboard_repo prompt returns a valid message with no args.
func TestPromptGetOnboardRepo(t *testing.T) {
	_, cleanup := setupTestEnv(t)
	defer cleanup()

	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "prompts/get", map[string]interface{}{
		"name": "onboard_repo",
	})

	srv := NewServerWithIO(input, output)
	RegisterPrompts(srv)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	var promptResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			promptResp = r
			break
		}
	}
	if promptResp == nil {
		t.Fatalf("no response with id=2")
	}

	result, ok := promptResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", promptResp)
	}

	messages, ok := result["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing or empty: %v", result)
	}

	msg, _ := messages[0].(map[string]interface{})
	content, _ := msg["content"].(map[string]interface{})
	text, _ := content["text"].(string)

	if !strings.Contains(text, "get_architecture_summary") {
		t.Errorf("expected prompt text to contain 'get_architecture_summary', got %q", text)
	}
	if !strings.Contains(text, "get_key_symbols") {
		t.Errorf("expected prompt text to contain 'get_key_symbols', got %q", text)
	}
}

// TestPromptGetCollectMinimalContext verifies the collect_minimal_context prompt uses
// assemble_context's canonical query/budget_tokens inputs and includes the safer
// inspection sequence before any full read.
func TestPromptGetCollectMinimalContext(t *testing.T) {
	_, cleanup := setupTestEnv(t)
	defer cleanup()

	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "prompts/get", map[string]interface{}{
		"name": "collect_minimal_context",
		"arguments": map[string]interface{}{
			"query":         "refactor order flow",
			"budget_tokens":  "2400",
		},
	})

	srv := NewServerWithIO(input, output)
	RegisterPrompts(srv)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	var promptResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			promptResp = r
			break
		}
	}
	if promptResp == nil {
		t.Fatalf("no response with id=2")
	}

	result, ok := promptResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", promptResp)
	}

	messages, ok := result["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing or empty: %v", result)
	}

	msg, _ := messages[0].(map[string]interface{})
	content, _ := msg["content"].(map[string]interface{})
	text, _ := content["text"].(string)

	for _, want := range []string{
		`assemble_context with query="refactor order flow", budget_tokens=2400`,
		`list_file_symbols`,
		`read_symbol with mode="bounded"`,
		`read_symbol with mode="flow_summary"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected prompt text to contain %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "task=") || strings.Contains(text, "budget=") {
		t.Errorf("expected prompt text to avoid legacy task/budget args, got %q", text)
	}
}

// TestResourceReadRepoSummary verifies the repo_summary resource returns valid JSON.
func TestResourceReadRepoSummary(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "resources/read", map[string]interface{}{
		"uri": "context-mcp://repo_summary",
	})

	srv := NewServerWithIO(input, output)
	RegisterResources(srv, deps)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	var resourceResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			resourceResp = r
			break
		}
	}
	if resourceResp == nil {
		t.Fatalf("no response with id=2")
	}

	result, ok := resourceResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", resourceResp)
	}

	contents, ok := result["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		t.Fatalf("contents missing or empty: %v", result)
	}

	item, _ := contents[0].(map[string]interface{})
	text, _ := item["text"].(string)

	// Parse the JSON text to verify it's valid
	var summary map[string]interface{}
	if err := json.Unmarshal([]byte(text), &summary); err != nil {
		t.Fatalf("resource text is not valid JSON: %v", err)
	}

	// Verify expected fields exist
	if _, ok := summary["total_nodes"]; !ok {
		t.Error("expected 'total_nodes' field in repo summary")
	}
	if _, ok := summary["graph_nodes"]; !ok {
		t.Error("expected 'graph_nodes' field in repo summary")
	}
	if _, ok := summary["profile"]; !ok {
		t.Error("expected 'profile' field in repo summary")
	}
}

// TestResourceReadHotPaths verifies the hot_paths resource returns valid JSON with data.
func TestResourceReadHotPaths(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "resources/read", map[string]interface{}{
		"uri": "context-mcp://hot_paths",
	})

	srv := NewServerWithIO(input, output)
	RegisterResources(srv, deps)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	var resourceResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			resourceResp = r
			break
		}
	}
	if resourceResp == nil {
		t.Fatalf("no response with id=2")
	}

	result, ok := resourceResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", resourceResp)
	}

	contents, ok := result["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		t.Fatalf("contents missing or empty: %v", result)
	}

	item, _ := contents[0].(map[string]interface{})
	text, _ := item["text"].(string)

	var hotPaths map[string]interface{}
	if err := json.Unmarshal([]byte(text), &hotPaths); err != nil {
		t.Fatalf("resource text is not valid JSON: %v", err)
	}

	// Test env has 4 nodes with scores, so we should get results
	paths, ok := hotPaths["hot_paths"].([]interface{})
	if !ok {
		t.Fatal("expected 'hot_paths' array in response")
	}
	if len(paths) == 0 {
		t.Error("expected at least one hot path from test data")
	}

	count, _ := hotPaths["count"].(float64)
	if int(count) != len(paths) {
		t.Errorf("count (%v) does not match hot_paths length (%d)", count, len(paths))
	}
}

// TestResourceReadIndexStats verifies the index_stats resource returns node type counts.
func TestResourceReadIndexStats(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	input := &bytes.Buffer{}
	output := &syncBuffer{}

	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "resources/read", map[string]interface{}{
		"uri": "context-mcp://index_stats",
	})

	srv := NewServerWithIO(input, output)
	RegisterResources(srv, deps)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve()
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	var resourceResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			resourceResp = r
			break
		}
	}
	if resourceResp == nil {
		t.Fatalf("no response with id=2")
	}

	result, ok := resourceResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", resourceResp)
	}

	contents, ok := result["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		t.Fatalf("contents missing or empty: %v", result)
	}

	item, _ := contents[0].(map[string]interface{})
	text, _ := item["text"].(string)

	var stats map[string]interface{}
	if err := json.Unmarshal([]byte(text), &stats); err != nil {
		t.Fatalf("resource text is not valid JSON: %v", err)
	}

	// Test env has 4 function nodes plus 2 route nodes
	totalNodes, _ := stats["total_nodes"].(float64)
	if int(totalNodes) != 6 {
		t.Errorf("expected total_nodes=6, got %v", totalNodes)
	}

	typeCounts, ok := stats["node_counts_by_type"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'node_counts_by_type' map")
	}
	funcCount, _ := typeCounts["function"].(float64)
	if int(funcCount) != 4 {
		t.Errorf("expected 4 function nodes, got %v", funcCount)
	}
	routeCount, _ := typeCounts["route"].(float64)
	if int(routeCount) != 2 {
		t.Errorf("expected 2 route nodes, got %v", routeCount)
	}

	uniqueFiles, _ := stats["unique_files"].(float64)
	if int(uniqueFiles) != 3 {
		t.Errorf("expected 3 unique files, got %v", uniqueFiles)
	}
}

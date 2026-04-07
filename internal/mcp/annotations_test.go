package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestAnnotations_ContextToolHasMeta verifies that the context tool in tools/list
// response includes _meta with anthropic/alwaysLoad: true.
func TestAnnotations_ContextToolHasMeta(t *testing.T) {
	input := &bytes.Buffer{}
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
	sendJSONRPC(input, 2, "tools/list", map[string]interface{}{})

	output := &syncBuffer{}
	server := NewServerWithIO(input, output)

	// Register the context tool with annotations matching the real registration
	tool := mcp.NewTool("context",
		mcp.WithDescription("Context search tool"),
		mcp.WithTitleAnnotation("Context Search"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("query", mcp.Description("Search query")),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/alwaysLoad": true,
			"anthropic/searchHint": "ranked code discovery; start here for where to look",
		},
	}
	server.AddSDKTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background())
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	// Find the tools/list response (id=2)
	var toolsResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			toolsResp = r
			break
		}
	}
	if toolsResp == nil {
		t.Fatalf("no response with id=2; got %d responses", len(responses))
	}

	result, ok := toolsResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", toolsResp)
	}
	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools field missing or wrong type: %v", result["tools"])
	}

	// Find the context tool
	var contextTool map[string]interface{}
	for _, tl := range tools {
		toolMap, _ := tl.(map[string]interface{})
		if toolMap["name"] == "context" {
			contextTool = toolMap
			break
		}
	}
	if contextTool == nil {
		t.Fatal("context tool not found in tools/list response")
	}

	// Check _meta field
	meta, ok := contextTool["_meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("context tool missing _meta field or wrong type: %v", contextTool["_meta"])
	}
	alwaysLoad, ok := meta["anthropic/alwaysLoad"]
	if !ok {
		t.Fatal("_meta missing anthropic/alwaysLoad")
	}
	if alwaysLoad != true {
		t.Errorf("expected anthropic/alwaysLoad=true, got %v", alwaysLoad)
	}
	searchHint, ok := meta["anthropic/searchHint"]
	if !ok {
		t.Fatal("_meta missing anthropic/searchHint")
	}
	if searchHint != "ranked code discovery; start here for where to look" {
		t.Errorf("unexpected searchHint: %v", searchHint)
	}
}

// TestAnnotations_ReadOnlyHint verifies that at least one tool has
// annotations.readOnlyHint set to true.
func TestAnnotations_ReadOnlyHint(t *testing.T) {
	input := &bytes.Buffer{}
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
	sendJSONRPC(input, 2, "tools/list", map[string]interface{}{})

	output := &syncBuffer{}
	server := NewServerWithIO(input, output)

	// Register a read-only tool
	tool := mcp.NewTool("health",
		mcp.WithDescription("Health check"),
		mcp.WithTitleAnnotation("Health Check"),
		mcp.WithReadOnlyHintAnnotation(true),
	)
	tool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "server health and index statistics",
		},
	}
	server.AddSDKTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})

	// Register a non-read-only tool
	indexTool := mcp.NewTool("index",
		mcp.WithDescription("Re-index"),
		mcp.WithTitleAnnotation("Reindex"),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	indexTool.Meta = &mcp.Meta{
		AdditionalFields: map[string]any{
			"anthropic/searchHint": "trigger re-indexing of the repository",
		},
	}
	server.AddSDKTool(indexTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background())
	}()

	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	var toolsResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			toolsResp = r
			break
		}
	}
	if toolsResp == nil {
		t.Fatalf("no response with id=2; got %d responses", len(responses))
	}

	result, ok := toolsResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", toolsResp)
	}
	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools field missing or wrong type: %v", result["tools"])
	}

	// Check that at least one tool has readOnlyHint: true
	foundReadOnly := false
	foundNonReadOnly := false
	for _, tl := range tools {
		toolMap, _ := tl.(map[string]interface{})
		annotations, _ := toolMap["annotations"].(map[string]interface{})
		if annotations == nil {
			continue
		}
		if ro, ok := annotations["readOnlyHint"]; ok && ro == true {
			foundReadOnly = true
		}
		if toolMap["name"] == "index" {
			// index should NOT have readOnlyHint=true
			if ro, ok := annotations["readOnlyHint"]; ok && ro == true {
				t.Error("index tool should not have readOnlyHint=true")
			}
			// index should have destructiveHint=false
			if dh, ok := annotations["destructiveHint"]; ok && dh == false {
				foundNonReadOnly = true
			}
		}
	}
	if !foundReadOnly {
		t.Error("no tool found with annotations.readOnlyHint=true")
	}
	if !foundNonReadOnly {
		t.Error("index tool missing destructiveHint=false annotation")
	}
}

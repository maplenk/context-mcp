package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// runServer starts the server in a goroutine and returns when it finishes.
// Use a small input buffer so the server exits after processing all lines.
func runServerSync(server *Server) {
	done := make(chan struct{})
	go func() {
		server.Serve()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// server should have exited when the input was exhausted
	}
	// Allow goroutine-dispatched handlers to finish writing responses
	time.Sleep(100 * time.Millisecond)
}

// readResponses decodes all newline-delimited JSON responses from a buffer.
func readResponses(t *testing.T, buf *bytes.Buffer) []JSONRPCResponse {
	t.Helper()
	var responses []JSONRPCResponse
	dec := json.NewDecoder(buf)
	for dec.More() {
		var resp JSONRPCResponse
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("decode response: %v (raw=%q)", err, buf.String())
		}
		responses = append(responses, resp)
	}
	return responses
}

// findResponse returns the first response whose ID matches, or nil.
func findResponse(responses []JSONRPCResponse, id float64) *JSONRPCResponse {
	for i := range responses {
		if v, ok := responses[i].ID.(float64); ok && v == id {
			return &responses[i]
		}
	}
	return nil
}

// TestNewServerWithIO verifies that NewServerWithIO creates a non-nil server.
func TestNewServerWithIO(t *testing.T) {
	input := &bytes.Buffer{}
	output := &bytes.Buffer{}
	server := NewServerWithIO(input, output)
	if server == nil {
		t.Fatal("NewServerWithIO returned nil")
	}
}

// TestRegisterTool verifies that RegisterTool adds a tool to the server.
func TestRegisterTool(t *testing.T) {
	input := &bytes.Buffer{}
	output := &bytes.Buffer{}
	server := NewServerWithIO(input, output)

	server.RegisterTool(ToolDefinition{
		Name:        "my_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) {
		return "ok", nil
	})

	server.mu.Lock()
	toolCount := len(server.tools)
	_, handlerExists := server.handlers["my_tool"]
	server.mu.Unlock()

	if toolCount != 1 {
		t.Errorf("expected 1 tool registered, got %d", toolCount)
	}
	if !handlerExists {
		t.Error("handler for 'my_tool' not registered")
	}
}

// TestInitialize verifies that the initialize method returns the correct protocolVersion.
func TestInitialize(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	input := bytes.NewBufferString(req)
	output := &bytes.Buffer{}
	server := NewServerWithIO(input, output)

	runServerSync(server)

	responses := readResponses(t, output)
	resp := findResponse(responses, 1)
	if resp == nil {
		t.Fatalf("no response with id=1; got: %s", output.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result is not a map: %T", resp.Result)
	}
	version, ok := result["protocolVersion"].(string)
	if !ok {
		t.Fatalf("protocolVersion missing or wrong type: %v", result["protocolVersion"])
	}
	if version != "2024-11-05" {
		t.Errorf("expected protocolVersion='2024-11-05', got %q", version)
	}
}

// TestToolsList verifies that registered tools are returned in the tools/list response.
func TestToolsList(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"
	input := bytes.NewBufferString(req)
	output := &bytes.Buffer{}
	server := NewServerWithIO(input, output)

	server.RegisterTool(ToolDefinition{
		Name:        "search_code",
		Description: "Searches the code graph",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) {
		return nil, nil
	})

	runServerSync(server)

	responses := readResponses(t, output)
	resp := findResponse(responses, 2)
	if resp == nil {
		t.Fatalf("no response with id=2; got: %s", output.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result is not a map: %T", resp.Result)
	}
	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools field missing or wrong type: %v", result["tools"])
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}

	toolMap, ok := tools[0].(map[string]interface{})
	if !ok {
		t.Fatalf("tool entry is not a map: %T", tools[0])
	}
	if toolMap["name"] != "search_code" {
		t.Errorf("expected tool name 'search_code', got %v", toolMap["name"])
	}
}

// TestToolsCall_HandlerInvoked verifies that calling a tool invokes its handler.
func TestToolsCall_HandlerInvoked(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo_tool","arguments":{}}}` + "\n"
	input := bytes.NewBufferString(req)
	output := &bytes.Buffer{}
	server := NewServerWithIO(input, output)

	handlerCalled := false
	server.RegisterTool(ToolDefinition{
		Name:        "echo_tool",
		Description: "Echoes back",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) {
		handlerCalled = true
		return "handler was called", nil
	})

	runServerSync(server)

	if !handlerCalled {
		t.Error("handler was not invoked for tools/call")
	}

	responses := readResponses(t, output)
	resp := findResponse(responses, 3)
	if resp == nil {
		t.Fatalf("no response with id=3; got: %s", output.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %T", resp.Result)
	}
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("content missing or empty: %v", result["content"])
	}
	contentItem, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("content[0] not a map")
	}
	text, _ := contentItem["text"].(string)
	if !strings.Contains(text, "handler was called") {
		t.Errorf("expected response text to contain 'handler was called', got %q", text)
	}
}

// TestConcurrentRequests verifies that the server correctly handles multiple JSON-RPC
// requests arriving in a single input buffer, dispatching each to a goroutine and
// collecting all responses. This exercises the goroutine dispatch and the mutex in
// writeResponse.
func TestConcurrentRequests(t *testing.T) {
	const numRequests = 5
	var input strings.Builder
	for i := 1; i <= numRequests; i++ {
		input.WriteString(`{"jsonrpc":"2.0","id":` + fmt.Sprintf("%d", i) + `,"method":"tools/list","params":{}}` + "\n")
	}

	output := &bytes.Buffer{}
	server := NewServerWithIO(strings.NewReader(input.String()), output)

	runServerSync(server)

	responses := readResponses(t, output)
	if len(responses) != numRequests {
		t.Fatalf("expected %d responses, got %d (raw=%q)", numRequests, len(responses), output.String())
	}

	// Verify every expected ID has a matching response without an error.
	for i := 1; i <= numRequests; i++ {
		resp := findResponse(responses, float64(i))
		if resp == nil {
			t.Errorf("no response for request id=%d", i)
			continue
		}
		if resp.Error != nil {
			t.Errorf("request id=%d: unexpected error: %v", i, resp.Error)
		}
	}
}

// TestUnknownMethod_ReturnsError verifies that an unknown method returns a JSON-RPC error.
func TestUnknownMethod_ReturnsError(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":4,"method":"unknown/method","params":{}}` + "\n"
	input := bytes.NewBufferString(req)
	output := &bytes.Buffer{}
	server := NewServerWithIO(input, output)

	runServerSync(server)

	responses := readResponses(t, output)
	resp := findResponse(responses, 4)
	if resp == nil {
		t.Fatalf("no response with id=4; got: %s", output.String())
	}
	if resp.Error == nil {
		t.Fatal("expected an error response for unknown method, got nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected error code -32601 (Method not found), got %d", resp.Error.Code)
	}
}

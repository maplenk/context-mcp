package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	mcp_golang "github.com/metoro-io/mcp-golang"
)

// syncBuffer wraps bytes.Buffer with a mutex for thread-safe reads/writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) Read(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Read(p)
}

func (sb *syncBuffer) Bytes() []byte {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Bytes()
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
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

// TestRegisterTool verifies that RegisterTool adds a tool to the server's
// internal CLI handler map.
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

	tools := server.GetTools()
	if len(tools) != 1 {
		t.Errorf("expected 1 tool registered, got %d", len(tools))
	}

	handler, ok := server.GetHandler("my_tool")
	if !ok {
		t.Error("handler for 'my_tool' not registered")
	}
	if handler == nil {
		t.Error("handler for 'my_tool' is nil")
	}
}

// TestGetTools verifies that GetTools returns a copy of registered tools.
func TestGetTools(t *testing.T) {
	server := NewServerWithIO(&bytes.Buffer{}, &bytes.Buffer{})

	server.RegisterTool(ToolDefinition{
		Name:        "tool_a",
		Description: "Tool A",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) { return nil, nil })

	server.RegisterTool(ToolDefinition{
		Name:        "tool_b",
		Description: "Tool B",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) { return nil, nil })

	tools := server.GetTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "tool_a" {
		t.Errorf("expected first tool 'tool_a', got %q", tools[0].Name)
	}
	if tools[1].Name != "tool_b" {
		t.Errorf("expected second tool 'tool_b', got %q", tools[1].Name)
	}
}

// TestGetHandler_NotFound verifies that GetHandler returns false for unknown tools.
func TestGetHandler_NotFound(t *testing.T) {
	server := NewServerWithIO(&bytes.Buffer{}, &bytes.Buffer{})
	_, ok := server.GetHandler("nonexistent")
	if ok {
		t.Error("expected GetHandler to return false for nonexistent tool")
	}
}

// TestToolHandler_Invocation verifies that a CLI handler can be invoked
// directly via GetHandler.
func TestToolHandler_Invocation(t *testing.T) {
	server := NewServerWithIO(&bytes.Buffer{}, &bytes.Buffer{})

	called := false
	server.RegisterTool(ToolDefinition{
		Name:        "echo",
		Description: "Echo tool",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) {
		called = true
		return "echoed", nil
	})

	handler, ok := server.GetHandler("echo")
	if !ok {
		t.Fatal("handler not found")
	}
	result, err := handler(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("handler was not invoked")
	}
	if result != "echoed" {
		t.Errorf("expected 'echoed', got %v", result)
	}
}

// TestToolHandler_Error verifies that a CLI handler error is returned correctly.
func TestToolHandler_Error(t *testing.T) {
	server := NewServerWithIO(&bytes.Buffer{}, &bytes.Buffer{})

	server.RegisterTool(ToolDefinition{
		Name:        "fail",
		Description: "Failing tool",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("intentional error")
	})

	handler, _ := server.GetHandler("fail")
	_, err := handler(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from handler")
	}
	if !strings.Contains(err.Error(), "intentional error") {
		t.Errorf("expected 'intentional error', got %q", err.Error())
	}
}

// sendJSONRPC is a helper that writes a JSON-RPC request line into a writer.
func sendJSONRPC(w io.Writer, id interface{}, method string, params interface{}) error {
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		paramsRaw = b
	}
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if paramsRaw != nil {
		req["params"] = json.RawMessage(paramsRaw)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// readableBuffer is an interface for buffers that support concurrent-safe Bytes() and String().
type readableBuffer interface {
	Bytes() []byte
	String() string
}

// readJSONRPCResponse reads newline-delimited JSON-RPC responses from the
// output buffer until the expected count is reached or a timeout fires.
func readJSONRPCResponses(t *testing.T, buf readableBuffer, want int, timeout time.Duration) []map[string]interface{} {
	t.Helper()
	deadline := time.After(timeout)
	var responses []map[string]interface{}

	for len(responses) < want {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d responses, got %d (raw=%q)", want, len(responses), buf.String())
		default:
		}

		data := buf.Bytes()
		if len(data) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Try to decode all available responses
		dec := json.NewDecoder(bytes.NewReader(data))
		var decoded []map[string]interface{}
		for dec.More() {
			var resp map[string]interface{}
			if err := dec.Decode(&resp); err != nil {
				break
			}
			decoded = append(decoded, resp)
		}
		responses = decoded
		if len(responses) < want {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return responses
}

// TestSDKServe_Initialize verifies that the SDK-backed server responds to
// an initialize request with the correct protocol version and server info.
func TestSDKServe_Initialize(t *testing.T) {
	input := &bytes.Buffer{}
	output := &syncBuffer{}

	// Write initialize request
	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "test-client",
			"version": "1.0",
		},
	})

	server := NewServerWithIO(input, output)

	// Register a dummy tool so the server has tools capability
	server.RegisterTool(ToolDefinition{
		Name:        "test",
		Description: "test",
		InputSchema: map[string]interface{}{},
	}, func(params json.RawMessage) (interface{}, error) {
		return nil, nil
	})

	// Start server in goroutine
	done := make(chan error, 1)
	go func() {
		done <- server.Serve()
	}()

	// Wait for response
	responses := readJSONRPCResponses(t, output, 1, 3*time.Second)
	if len(responses) < 1 {
		t.Fatal("no response received for initialize")
	}

	// Check server finished without fatal error
	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
		// Server may still be running, that's OK
	}

	resp := responses[0]
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result is not a map: %T — full response: %v", resp["result"], resp)
	}

	version, _ := result["protocolVersion"].(string)
	if version != "2024-11-05" {
		t.Errorf("expected protocolVersion '2024-11-05', got %q", version)
	}

	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("serverInfo missing or wrong type: %v", result["serverInfo"])
	}
	if serverInfo["name"] != "qb-context" {
		t.Errorf("expected server name 'qb-context', got %v", serverInfo["name"])
	}
}

// TestSDKServe_ToolsList verifies that tools registered with the SDK are
// returned via the tools/list protocol method.
func TestSDKServe_ToolsList(t *testing.T) {
	// Build a combined input: initialize then tools/list
	input := &bytes.Buffer{}
	sendJSONRPC(input, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
	})
	// notifications/initialized (no id)
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(input, "%s\n", notif)
	sendJSONRPC(input, 2, "tools/list", map[string]interface{}{})

	output := &syncBuffer{}
	server := NewServerWithIO(input, output)

	// Register via SDK
	_ = server.RegisterSDKTool("search_code", "Searches the code graph", func(args testSearchArgs) (*ToolResponse, error) {
		return nil, nil
	})

	done := make(chan error, 1)
	go func() {
		done <- server.Serve()
	}()

	// We expect at least 2 responses (initialize + tools/list)
	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	// Check server finished without fatal error
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
	if len(tools) < 1 {
		t.Errorf("expected at least 1 tool, got %d", len(tools))
	}

	// Verify our tool is in the list
	found := false
	for _, tool := range tools {
		toolMap, _ := tool.(map[string]interface{})
		if toolMap["name"] == "search_code" {
			found = true
			break
		}
	}
	if !found {
		t.Error("tool 'search_code' not found in tools/list response")
	}
}

// TestSDKServe_ToolsCall verifies that calling a tool via the SDK protocol
// invokes the handler and returns the correct content.
func TestSDKServe_ToolsCall(t *testing.T) {
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
	sendJSONRPC(input, 2, "tools/call", map[string]interface{}{
		"name":      "echo_tool",
		"arguments": map[string]interface{}{"message": "hello SDK"},
	})

	output := &syncBuffer{}
	server := NewServerWithIO(input, output)

	handlerCalled := false
	_ = server.RegisterSDKTool("echo_tool", "Echoes input", func(args testEchoArgs) (*ToolResponse, error) {
		handlerCalled = true
		return NewToolResponse(NewTextContent(fmt.Sprintf("echo: %s", args.Message))), nil
	})

	done := make(chan error, 1)
	go func() {
		done <- server.Serve()
	}()

	// Wait for 2 responses (initialize + tools/call)
	responses := readJSONRPCResponses(t, output, 2, 3*time.Second)

	// Check server finished without fatal error
	select {
	case err := <-done:
		if err != nil {
			t.Logf("server exited with: %v", err)
		}
	default:
	}

	if !handlerCalled {
		t.Error("SDK tool handler was not invoked")
	}

	// Find tools/call response
	var callResp map[string]interface{}
	for _, r := range responses {
		if id, ok := r["id"].(float64); ok && id == 2 {
			callResp = r
			break
		}
	}
	if callResp == nil {
		t.Fatalf("no response with id=2; got %d responses", len(responses))
	}

	result, ok := callResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %v", callResp)
	}
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("content missing or empty: %v", result)
	}
	item, _ := content[0].(map[string]interface{})
	text, _ := item["text"].(string)
	if !strings.Contains(text, "echo: hello SDK") {
		t.Errorf("expected 'echo: hello SDK' in text, got %q", text)
	}
}

// TestConcurrentToolRegistration verifies that multiple tools can be
// registered concurrently without data races.
func TestConcurrentToolRegistration(t *testing.T) {
	server := NewServerWithIO(&bytes.Buffer{}, &bytes.Buffer{})

	const numTools = 5
	done := make(chan struct{})
	for i := 0; i < numTools; i++ {
		go func(idx int) {
			server.RegisterTool(ToolDefinition{
				Name:        fmt.Sprintf("tool_%d", idx),
				Description: fmt.Sprintf("Tool %d", idx),
				InputSchema: map[string]interface{}{},
			}, func(params json.RawMessage) (interface{}, error) {
				return nil, nil
			})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < numTools; i++ {
		<-done
	}

	tools := server.GetTools()
	if len(tools) != numTools {
		t.Errorf("expected %d tools, got %d", numTools, len(tools))
	}
}

// ToolResponse and NewToolResponse / NewTextContent are re-exported from the
// SDK for convenience in tests.
type ToolResponse = mcp_golang.ToolResponse

var (
	NewToolResponse = mcp_golang.NewToolResponse
	NewTextContent  = mcp_golang.NewTextContent
)

// testSearchArgs is a named struct for the SDK search tool handler in tests.
type testSearchArgs struct {
	Query string `json:"query" jsonschema:"required,description=Search query"`
}

// testEchoArgs is a named struct for the SDK echo tool handler in tests.
type testEchoArgs struct {
	Message string `json:"message" jsonschema:"required,description=Message to echo"`
}

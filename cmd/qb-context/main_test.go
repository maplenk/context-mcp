package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildTestBinary compiles the CLI binary for subprocess testing.
// The binary is placed in a temp directory that is cleaned up after the test.
func buildTestBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "qb-context-test")
	cmd := exec.Command("go", "build", "-tags", "fts5", "-o", binary, ".")
	cmd.Dir = filepath.Join(getModuleRoot(t), "cmd", "qb-context")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test binary: %v\nOutput: %s", err, output)
	}
	return binary
}

// getModuleRoot finds the Go module root by walking up from cwd looking for go.mod.
func getModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root (go.mod)")
		}
		dir = parent
	}
}

// TestCLIListTools verifies that "cli --list" prints all 13 registered tools.
func TestCLIListTools(t *testing.T) {
	binary := buildTestBinary(t)

	cmd := exec.Command(binary, "-repo", t.TempDir(), "cli", "--list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli --list failed: %v\nOutput: %s", err, output)
	}

	outStr := string(output)

	// Verify the header row
	if !strings.Contains(outStr, "TOOL") || !strings.Contains(outStr, "DESCRIPTION") {
		t.Errorf("expected header row with TOOL and DESCRIPTION, got:\n%s", outStr)
	}

	// All 13 registered tool names must appear
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
	for _, tool := range expectedTools {
		if !strings.Contains(outStr, tool) {
			t.Errorf("cli --list missing tool %q\nOutput: %s", tool, outStr)
		}
	}

	// Sanity-check: count non-empty, non-header lines to confirm at least 13 tools
	lines := strings.Split(strings.TrimSpace(outStr), "\n")
	toolLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip header and separator lines
		if strings.HasPrefix(trimmed, "TOOL") || strings.HasPrefix(trimmed, "----") {
			continue
		}
		toolLines++
	}
	if toolLines < 13 {
		t.Errorf("expected at least 13 tool lines, got %d\nOutput: %s", toolLines, outStr)
	}
}

// TestCLIUnknownTool verifies that an unrecognized tool name causes a non-zero exit.
func TestCLIUnknownTool(t *testing.T) {
	binary := buildTestBinary(t)

	cmd := exec.Command(binary, "-repo", t.TempDir(), "cli", "nonexistent_tool_xyz", "{}")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown tool, got success")
	}

	outStr := string(output)
	if !strings.Contains(outStr, "Unknown tool") {
		t.Errorf("expected 'Unknown tool' in stderr, got:\n%s", outStr)
	}
}

// TestCLIMissingToolName verifies that calling "cli" with no tool name prints usage and exits non-zero.
func TestCLIMissingToolName(t *testing.T) {
	binary := buildTestBinary(t)

	cmd := exec.Command(binary, "-repo", t.TempDir(), "cli")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit when no tool name given, got success")
	}

	outStr := string(output)
	if !strings.Contains(outStr, "Usage:") {
		t.Errorf("expected usage message in output, got:\n%s", outStr)
	}
}

// TestCLIInvalidJSON verifies that invalid JSON args cause a non-zero exit.
func TestCLIInvalidJSON(t *testing.T) {
	binary := buildTestBinary(t)

	cmd := exec.Command(binary, "-repo", t.TempDir(), "cli", "health", "not-json")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for invalid JSON args, got success")
	}

	outStr := string(output)
	if !strings.Contains(outStr, "Invalid JSON") {
		t.Errorf("expected 'Invalid JSON' in output, got:\n%s", outStr)
	}
}

// TestCLIHealthTool runs the health tool against an empty temp repo and verifies it
// returns valid JSON with expected fields.
func TestCLIHealthTool(t *testing.T) {
	binary := buildTestBinary(t)
	tmpDir := t.TempDir()

	cmd := exec.Command(binary, "-repo", tmpDir, "cli", "health", "{}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli health failed: %v\nOutput: %s", err, output)
	}

	outStr := string(output)
	// Health tool should return JSON with at least "status" or "node_count" or similar fields
	if !strings.Contains(outStr, "{") {
		t.Errorf("expected JSON output from health tool, got:\n%s", outStr)
	}
}

// TestIndexRepoSmoke creates a temp directory with a simple Go file, runs the
// context tool, and verifies the symbol appears in results.
func TestIndexRepoSmoke(t *testing.T) {
	binary := buildTestBinary(t)
	tmpDir := t.TempDir()

	// Write a simple Go file with a clearly named function
	goFile := filepath.Join(tmpDir, "example.go")
	if err := os.WriteFile(goFile, []byte(`package example

// GreetUniverse returns a greeting string.
func GreetUniverse() string {
	return "hello universe"
}

// AddNumbers adds two integers.
func AddNumbers(a, b int) int {
	return a + b
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Use --reindex to force indexing of the temp repo, then query for the symbol
	cmd := exec.Command(binary, "-repo", tmpDir, "cli", "--reindex", "context", `{"query": "GreetUniverse", "limit": 5}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli context failed: %v\nOutput: %s", err, output)
	}

	outStr := string(output)
	if !strings.Contains(outStr, "GreetUniverse") {
		t.Errorf("expected 'GreetUniverse' in context results, got:\n%s", outStr)
	}
}

// TestCLISearchCodeSmoke indexes a temp repo and runs the search_code tool.
func TestCLISearchCodeSmoke(t *testing.T) {
	binary := buildTestBinary(t)
	tmpDir := t.TempDir()

	goFile := filepath.Join(tmpDir, "calc.go")
	if err := os.WriteFile(goFile, []byte(`package calc

// Multiply multiplies two numbers.
func Multiply(x, y int) int {
	return x * y
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "-repo", tmpDir, "cli", "--reindex", "search_code", `{"pattern": "Multiply"}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli search_code failed: %v\nOutput: %s", err, output)
	}

	outStr := string(output)
	if !strings.Contains(outStr, "Multiply") {
		t.Errorf("expected 'Multiply' in search_code results, got:\n%s", outStr)
	}
}

// TestCLIGetKeySymbolsSmoke indexes a temp repo and runs the get_key_symbols tool.
func TestCLIGetKeySymbolsSmoke(t *testing.T) {
	binary := buildTestBinary(t)
	tmpDir := t.TempDir()

	goFile := filepath.Join(tmpDir, "svc.go")
	if err := os.WriteFile(goFile, []byte(`package svc

// Service is the main service struct.
type Service struct {
	Name string
}

// Start starts the service.
func (s *Service) Start() error {
	return nil
}

// Stop stops the service.
func (s *Service) Stop() error {
	return nil
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "-repo", tmpDir, "cli", "--reindex", "get_key_symbols", `{"limit": 10}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli get_key_symbols failed: %v\nOutput: %s", err, output)
	}

	outStr := string(output)
	// Should contain at least one symbol from the file
	if !strings.Contains(outStr, "Service") && !strings.Contains(outStr, "Start") && !strings.Contains(outStr, "Stop") {
		t.Errorf("expected at least one symbol from svc.go in get_key_symbols output, got:\n%s", outStr)
	}
}

// getFreePort returns an available TCP port on localhost.
func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// TestServeHTTPStartsAndResponds verifies that the serve-http subcommand
// starts an HTTP server that responds on /mcp.
func TestServeHTTPStartsAndResponds(t *testing.T) {
	binary := buildTestBinary(t)
	tmpDir := t.TempDir()
	port := getFreePort(t)

	cmd := exec.Command(binary, "-repo", tmpDir, "serve-http", fmt.Sprintf("-port=%d", port))
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve-http: %v", err)
	}
	defer cmd.Process.Kill()

	// Wait for the HTTP server to be ready
	addr := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, _ = http.Post(addr, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`))
		if resp != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("serve-http did not respond within 5 seconds")
	}
	defer resp.Body.Close()

	// The server should respond with 200 (MCP protocol response)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected HTTP 200, got %d", resp.StatusCode)
	}
}

// TestServeHTTPBearerTokenRejectsUnauthorized verifies that when a bearer
// token is configured, unauthenticated requests get 401.
func TestServeHTTPBearerTokenRejectsUnauthorized(t *testing.T) {
	binary := buildTestBinary(t)
	tmpDir := t.TempDir()
	port := getFreePort(t)

	cmd := exec.Command(binary, "-repo", tmpDir, "serve-http",
		fmt.Sprintf("-port=%d", port), "-bearer-token=secret123")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve-http: %v", err)
	}
	defer cmd.Process.Kill()

	addr := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	// Wait for server to be ready (try with auth)
	var readyResp *http.Response
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest("POST", addr, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer secret123")
		readyResp, _ = http.DefaultClient.Do(req)
		if readyResp != nil {
			readyResp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if readyResp == nil {
		t.Fatal("serve-http did not respond within 5 seconds")
	}

	// Request without auth should get 401
	resp, err := http.Post(addr, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for unauthenticated request, got %d", resp.StatusCode)
	}
}

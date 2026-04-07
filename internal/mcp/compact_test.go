//go:build fts5

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maplenk/context-mcp/internal/types"
)

func TestStripInspectable(t *testing.T) {
	i := types.Inspectable{
		Rank:     1,
		Name:     "foo",
		FilePath: "bar.go",
		ID:       "abc",
		Score:    0.95,
		Reason:   "high betweenness",
		WhyNow:   "recently changed",
		NextTool: "read_symbol",
		NextArgs: map[string]string{"symbol_id": "abc"},
	}
	c := stripInspectable(i)
	if c.Reason != "" || c.WhyNow != "" || c.NextTool != "" || c.NextArgs != nil {
		t.Error("compact should strip Reason, WhyNow, NextTool, NextArgs")
	}
	if c.Rank != 1 || c.Name != "foo" || c.Score != 0.95 {
		t.Error("compact should preserve Rank, Name, Score")
	}
	if c.FilePath != "bar.go" || c.ID != "abc" {
		t.Error("compact should preserve FilePath, ID")
	}
}

func TestCompactMode_Context(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("context")
	if !ok {
		t.Fatal("context handler not registered")
	}

	// Normal response
	normalResult, err := handler(json.RawMessage(`{"query": "process order", "limit": 5}`))
	if err != nil {
		t.Fatalf("normal handler error: %v", err)
	}
	normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")

	// Compact response
	compactResult, err := handler(json.RawMessage(`{"query": "process order", "limit": 5, "compact": true}`))
	if err != nil {
		t.Fatalf("compact handler error: %v", err)
	}
	compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")

	normalSize := len(normalBytes)
	compactSize := len(compactBytes)
	t.Logf("context: normal=%d bytes, compact=%d bytes, reduction=%.1f%%",
		normalSize, compactSize, 100*(1-float64(compactSize)/float64(normalSize)))

	if compactSize >= normalSize {
		t.Errorf("compact response (%d) should be smaller than normal (%d)", compactSize, normalSize)
	}
}

func TestCompactMode_Impact(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("impact")
	if !ok {
		t.Fatal("impact handler not registered")
	}

	// Normal response
	normalResult, err := handler(json.RawMessage(`{"symbol_id": "processOrder", "depth": 5}`))
	if err != nil {
		t.Fatalf("normal handler error: %v", err)
	}
	normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")

	// Compact response
	compactResult, err := handler(json.RawMessage(`{"symbol_id": "processOrder", "depth": 5, "compact": true}`))
	if err != nil {
		t.Fatalf("compact handler error: %v", err)
	}
	compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")

	normalSize := len(normalBytes)
	compactSize := len(compactBytes)
	t.Logf("impact: normal=%d bytes, compact=%d bytes, reduction=%.1f%%",
		normalSize, compactSize, 100*(1-float64(compactSize)/float64(normalSize)))

	if compactSize >= normalSize {
		t.Errorf("compact response (%d) should be smaller than normal (%d)", compactSize, normalSize)
	}
}

func TestCompactMode_DetectChanges(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("detect_changes")
	if !ok {
		t.Fatal("detect_changes handler not registered")
	}

	// Normal response
	normalResult, err := handler(json.RawMessage(`{"since": "HEAD~5", "limit": 5}`))
	if err != nil {
		if strings.Contains(err.Error(), "git") || strings.Contains(err.Error(), "fatal") ||
			strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "exec") {
			t.Skipf("skipping detect_changes: git not available in test env: %v", err)
		}
		t.Fatalf("normal handler error: %v", err)
	}
	normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")

	// Compact response
	compactResult, err := handler(json.RawMessage(`{"since": "HEAD~5", "limit": 5, "compact": true}`))
	if err != nil {
		t.Fatalf("compact handler error: %v", err)
	}
	compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")

	normalSize := len(normalBytes)
	compactSize := len(compactBytes)
	t.Logf("detect_changes: normal=%d bytes, compact=%d bytes, reduction=%.1f%%",
		normalSize, compactSize, 100*(1-float64(compactSize)/float64(normalSize)))

	if compactSize >= normalSize {
		t.Errorf("compact response (%d) should be smaller than normal (%d)", compactSize, normalSize)
	}
}

func TestCompactMode_ArchitectureSummary(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("get_architecture_summary")
	if !ok {
		t.Fatal("get_architecture_summary handler not registered")
	}

	// Normal response
	normalResult, err := handler(json.RawMessage(`{"limit": 5}`))
	if err != nil {
		t.Fatalf("normal handler error: %v", err)
	}
	normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")

	// Compact response
	compactResult, err := handler(json.RawMessage(`{"limit": 5, "compact": true}`))
	if err != nil {
		t.Fatalf("compact handler error: %v", err)
	}
	compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")

	normalSize := len(normalBytes)
	compactSize := len(compactBytes)
	t.Logf("architecture_summary: normal=%d bytes, compact=%d bytes, reduction=%.1f%%",
		normalSize, compactSize, 100*(1-float64(compactSize)/float64(normalSize)))

	if compactSize >= normalSize {
		t.Errorf("compact response (%d) should be smaller than normal (%d)", compactSize, normalSize)
	}
}

func TestCompactMode_Explore(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("explore")
	if !ok {
		t.Fatal("explore handler not registered")
	}

	// Normal response
	normalResult, err := handler(json.RawMessage(`{"symbol": "processOrder"}`))
	if err != nil {
		t.Fatalf("normal handler error: %v", err)
	}
	normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")

	// Compact response
	compactResult, err := handler(json.RawMessage(`{"symbol": "processOrder", "compact": true}`))
	if err != nil {
		t.Fatalf("compact handler error: %v", err)
	}
	compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")

	normalSize := len(normalBytes)
	compactSize := len(compactBytes)
	t.Logf("explore: normal=%d bytes, compact=%d bytes, reduction=%.1f%%",
		normalSize, compactSize, 100*(1-float64(compactSize)/float64(normalSize)))

	if compactSize >= normalSize {
		t.Errorf("compact response (%d) should be smaller than normal (%d)", compactSize, normalSize)
	}
}

func TestCompactMode_Understand(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("understand")
	if !ok {
		t.Fatal("understand handler not registered")
	}

	// Normal response
	normalResult, err := handler(json.RawMessage(`{"symbol": "processOrder"}`))
	if err != nil {
		t.Fatalf("normal handler error: %v", err)
	}
	normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")

	// Compact response
	compactResult, err := handler(json.RawMessage(`{"symbol": "processOrder", "compact": true}`))
	if err != nil {
		t.Fatalf("compact handler error: %v", err)
	}
	compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")

	normalSize := len(normalBytes)
	compactSize := len(compactBytes)
	t.Logf("understand: normal=%d bytes, compact=%d bytes, reduction=%.1f%%",
		normalSize, compactSize, 100*(1-float64(compactSize)/float64(normalSize)))

	if compactSize >= normalSize {
		t.Errorf("compact response (%d) should be smaller than normal (%d)", compactSize, normalSize)
	}
}

func TestCompactMode_AssembleContext(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("assemble_context")
	if !ok {
		t.Fatal("assemble_context handler not registered")
	}

	// Normal response
	normalResult, err := handler(json.RawMessage(`{"query": "process order", "budget_tokens": 4000}`))
	if err != nil {
		t.Fatalf("normal handler error: %v", err)
	}
	normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")

	// Compact response
	compactResult, err := handler(json.RawMessage(`{"query": "process order", "budget_tokens": 4000, "compact": true}`))
	if err != nil {
		t.Fatalf("compact handler error: %v", err)
	}
	compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")

	normalSize := len(normalBytes)
	compactSize := len(compactBytes)
	t.Logf("assemble_context: normal=%d bytes, compact=%d bytes, reduction=%.1f%%",
		normalSize, compactSize, 100*(1-float64(compactSize)/float64(normalSize)))

	if compactSize >= normalSize {
		t.Errorf("compact response (%d) should be smaller than normal (%d)", compactSize, normalSize)
	}
}

func TestCompactMode_TaskTools(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	cases := []struct {
		name   string
		tool   string
		normal string
		compact string
	}{
		{
			name:   "compare_symbols",
			tool:   "compare_symbols",
			normal: `{"left":"processOrder","right":"validateOrder"}`,
			compact: `{"left":"processOrder","right":"validateOrder","compact":true}`,
		},
		{
			name:   "find_routes",
			tool:   "find_routes",
			normal: `{"query":"orders","limit":5}`,
			compact: `{"query":"orders","limit":5,"compact":true}`,
		},
		{
			name:   "trace_route",
			tool:   "trace_route",
			normal: `{"route":"POST /v1/orders","depth":2}`,
			compact: `{"route":"POST /v1/orders","depth":2,"compact":true}`,
		},
		{
			name:   "compare_routes",
			tool:   "compare_routes",
			normal: `{"left":"POST /v1/orders","right":"GET /v3/orders/{id}","depth":2}`,
			compact: `{"left":"POST /v1/orders","right":"GET /v3/orders/{id}","depth":2,"compact":true}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, ok := server.GetHandler(tc.tool)
			if !ok {
				t.Fatalf("%s handler not registered", tc.tool)
			}

			normalResult, err := handler(json.RawMessage(tc.normal))
			if err != nil {
				t.Fatalf("normal handler error: %v", err)
			}
			compactResult, err := handler(json.RawMessage(tc.compact))
			if err != nil {
				t.Fatalf("compact handler error: %v", err)
			}

			normalBytes, _ := json.MarshalIndent(normalResult, "", "  ")
			compactBytes, _ := json.MarshalIndent(compactResult, "", "  ")
			if len(compactBytes) >= len(normalBytes) {
				t.Fatalf("compact response (%d) should be smaller than normal (%d)", len(compactBytes), len(normalBytes))
			}

			compactJSON := string(compactBytes)
			if strings.Contains(compactJSON, `"summary"`) {
				t.Fatalf("compact response should not include summary: %s", compactJSON)
			}
		})
	}
}

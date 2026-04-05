//go:build fts5

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

const hardSizeLimit = 8192 // 8 KiB Codex compatibility guardrail

func TestResponseSize_Context(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("context")
	if !ok {
		t.Fatal("context handler not registered")
	}

	result, err := handler(json.RawMessage(`{"query": "process order", "limit": 5}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	size := len(jsonBytes)
	t.Logf("context response size: %d bytes (limit: %d, design target: ~3000)", size, hardSizeLimit)

	if size > hardSizeLimit {
		t.Errorf("context response %d bytes exceeds %d byte guardrail", size, hardSizeLimit)
	}
}

func TestResponseSize_GetArchitectureSummary(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("get_architecture_summary")
	if !ok {
		t.Fatal("get_architecture_summary handler not registered")
	}

	result, err := handler(json.RawMessage(`{"limit": 5}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	size := len(jsonBytes)
	t.Logf("get_architecture_summary response size: %d bytes (limit: %d, design target: ~3000)", size, hardSizeLimit)

	if size > hardSizeLimit {
		t.Errorf("get_architecture_summary response %d bytes exceeds %d byte guardrail", size, hardSizeLimit)
	}
}

func TestResponseSize_DetectChanges(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("detect_changes")
	if !ok {
		t.Fatal("detect_changes handler not registered")
	}

	result, err := handler(json.RawMessage(`{"since": "HEAD~5", "limit": 5}`))
	if err != nil {
		// Skip gracefully if the error is git-related (test env has no real git history)
		if strings.Contains(err.Error(), "git") || strings.Contains(err.Error(), "fatal") ||
			strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "exec") {
			t.Skipf("skipping detect_changes: git not available in test env: %v", err)
		}
		t.Fatalf("handler error: %v", err)
	}

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	size := len(jsonBytes)
	t.Logf("detect_changes response size: %d bytes (limit: %d, design target: ~2000)", size, hardSizeLimit)

	if size > hardSizeLimit {
		t.Errorf("detect_changes response %d bytes exceeds %d byte guardrail", size, hardSizeLimit)
	}
}

func TestResponseSize_Impact(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("impact")
	if !ok {
		t.Fatal("impact handler not registered")
	}

	result, err := handler(json.RawMessage(`{"symbol_id": "processOrder", "depth": 5}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	size := len(jsonBytes)
	t.Logf("impact response size: %d bytes (limit: %d, design target: ~5000)", size, hardSizeLimit)

	if size > hardSizeLimit {
		t.Errorf("impact response %d bytes exceeds %d byte guardrail", size, hardSizeLimit)
	}
}

func TestResponseSize_AllDiscovery(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	tests := []struct {
		name         string
		toolName     string
		params       string
		designTarget int
		skipOnGitErr bool
	}{
		{
			name:         "context",
			toolName:     "context",
			params:       `{"query": "process order", "limit": 5}`,
			designTarget: 3000,
		},
		{
			name:         "get_architecture_summary",
			toolName:     "get_architecture_summary",
			params:       `{"limit": 5}`,
			designTarget: 3000,
		},
		{
			name:         "detect_changes",
			toolName:     "detect_changes",
			params:       `{"since": "HEAD~5", "limit": 5}`,
			designTarget: 2000,
			skipOnGitErr: true,
		},
		{
			name:         "impact",
			toolName:     "impact",
			params:       `{"symbol_id": "processOrder", "depth": 5}`,
			designTarget: 5000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, ok := server.GetHandler(tc.toolName)
			if !ok {
				t.Fatalf("%s handler not registered", tc.toolName)
			}

			result, err := handler(json.RawMessage(tc.params))
			if err != nil {
				if tc.skipOnGitErr && (strings.Contains(err.Error(), "git") ||
					strings.Contains(err.Error(), "fatal") ||
					strings.Contains(err.Error(), "not a git repository") ||
					strings.Contains(err.Error(), "exec")) {
					t.Skipf("skipping %s: git not available in test env: %v", tc.toolName, err)
				}
				t.Fatalf("handler error: %v", err)
			}

			jsonBytes, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}

			size := len(jsonBytes)
			t.Logf("%s response size: %d bytes (limit: %d, design target: ~%d)",
				tc.toolName, size, hardSizeLimit, tc.designTarget)

			if size > hardSizeLimit {
				t.Errorf("%s response %d bytes exceeds %d byte guardrail",
					tc.toolName, size, hardSizeLimit)
			}
		})
	}
}

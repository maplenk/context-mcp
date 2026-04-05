//go:build fts5

package mcp

import (
	"encoding/json"
	"testing"
)

func TestAssembleContext_Snippets(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, ok := server.GetHandler("assemble_context")
	if !ok {
		t.Fatal("assemble_context handler not registered")
	}

	params, _ := json.Marshal(AssembleContextParams{
		Query:        "processOrder",
		BudgetTokens: 4000,
		Mode:         "snippets",
	})

	result, err := handler(params)
	if err != nil {
		t.Fatalf("assemble_context error: %v", err)
	}

	resp, ok := result.(AssembleContextResponse)
	if !ok {
		t.Fatalf("expected AssembleContextResponse, got %T", result)
	}

	if resp.Mode != "snippets" {
		t.Errorf("expected mode 'snippets', got %q", resp.Mode)
	}
	if resp.UsedTokens > resp.BudgetTokens {
		t.Errorf("used_tokens (%d) exceeds budget (%d)", resp.UsedTokens, resp.BudgetTokens)
	}
	if len(resp.Items) == 0 {
		t.Fatal("expected at least one item")
	}
	for _, item := range resp.Items {
		if item.Content == "" {
			t.Errorf("item %q has empty content", item.Name)
		}
	}
	t.Logf("Assembled %d items, %d/%d tokens", len(resp.Items), resp.UsedTokens, resp.BudgetTokens)
}

func TestAssembleContext_Summary(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("assemble_context")
	params, _ := json.Marshal(AssembleContextParams{Query: "order", BudgetTokens: 4000, Mode: "summary"})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	resp := result.(AssembleContextResponse)
	if resp.Mode != "summary" {
		t.Errorf("expected 'summary', got %q", resp.Mode)
	}
	if len(resp.Items) == 0 {
		t.Fatal("expected items")
	}
	t.Logf("Summary: %d items, %d tokens", len(resp.Items), resp.UsedTokens)
}

func TestAssembleContext_BudgetEnforcement(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("assemble_context")
	params, _ := json.Marshal(AssembleContextParams{Query: "order", BudgetTokens: 100, Mode: "snippets"})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	resp := result.(AssembleContextResponse)
	if resp.UsedTokens > resp.BudgetTokens {
		t.Errorf("used %d > budget %d", resp.UsedTokens, resp.BudgetTokens)
	}
	t.Logf("Budget: %d items, %d/%d tokens, %d excluded", len(resp.Items), resp.UsedTokens, resp.BudgetTokens, resp.Excluded)
}

func TestAssembleContext_AllModes(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("assemble_context")
	for _, mode := range []string{"summary", "signatures", "snippets", "bundle", "full"} {
		t.Run(mode, func(t *testing.T) {
			params, _ := json.Marshal(AssembleContextParams{
				Query: "order", BudgetTokens: 4000, Mode: mode, IncludeNeighbors: mode == "full",
			})
			result, err := handler(params)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			resp := result.(AssembleContextResponse)
			if resp.Mode != mode {
				t.Errorf("expected %q, got %q", mode, resp.Mode)
			}
			if resp.UsedTokens > resp.BudgetTokens {
				t.Errorf("over budget: %d > %d", resp.UsedTokens, resp.BudgetTokens)
			}
			if len(resp.Items) == 0 {
				t.Errorf("no items for mode %q", mode)
			}
			t.Logf("Mode %s: %d items, %d/%d tokens", mode, len(resp.Items), resp.UsedTokens, resp.BudgetTokens)
		})
	}
}

func TestAssembleContext_InvalidMode(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("assemble_context")
	params, _ := json.Marshal(AssembleContextParams{Query: "order", Mode: "invalid_mode"})
	_, err := handler(params)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestAssembleContext_Defaults(t *testing.T) {
	deps, cleanup := setupTestEnv(t)
	defer cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, deps, nil)

	handler, _ := server.GetHandler("assemble_context")
	params, _ := json.Marshal(AssembleContextParams{Query: "processOrder"})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	resp := result.(AssembleContextResponse)
	if resp.Mode != "snippets" {
		t.Errorf("expected default 'snippets', got %q", resp.Mode)
	}
	if resp.BudgetTokens != 4000 {
		t.Errorf("expected default 4000, got %d", resp.BudgetTokens)
	}
}

func TestAssembleContext_ProfileGating(t *testing.T) {
	if isToolInProfile("assemble_context", "core") {
		t.Error("assemble_context should NOT be in core profile")
	}
	if !isToolInProfile("assemble_context", "extended") {
		t.Error("assemble_context should be in extended profile")
	}
	if !isToolInProfile("assemble_context", "full") {
		t.Error("assemble_context should be in full profile")
	}
}

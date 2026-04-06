package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	sdkmcp "github.com/mark3labs/mcp-go/mcp"
)

const maxToolResultSize = 1024 * 1024

func extractToolResultText(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) != 1 {
		t.Fatalf("expected exactly one content item, got %d", len(result.Content))
	}
	textContent, ok := result.Content[0].(sdkmcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return textContent.Text
}

func TestToCallToolResult_TruncatesJSONToValidEnvelope(t *testing.T) {
	result, err := toCallToolResult(map[string]string{
		"data": strings.Repeat(`\"`, maxToolResultSize),
	})
	if err != nil {
		t.Fatalf("toCallToolResult error: %v", err)
	}

	text := extractToolResultText(t, result)
	if len(text) > maxToolResultSize {
		t.Fatalf("truncated JSON output exceeds cap: %d > %d", len(text), maxToolResultSize)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("truncated JSON output must remain valid JSON: %v", err)
	}
	if parsed["truncated"] != true {
		t.Fatalf("expected truncated marker, got %v", parsed["truncated"])
	}
	if partial, _ := parsed["partial_data"].(string); partial == "" {
		t.Fatal("expected non-empty partial_data")
	}
}

func TestToCallToolResult_TruncatesPlainTextAtUTF8Boundary(t *testing.T) {
	raw := strings.Repeat("🙂", (maxToolResultSize/4)+32)
	result, err := toCallToolResult(raw)
	if err != nil {
		t.Fatalf("toCallToolResult error: %v", err)
	}

	text := extractToolResultText(t, result)
	if len(text) > maxToolResultSize {
		t.Fatalf("truncated plain-text output exceeds cap: %d > %d", len(text), maxToolResultSize)
	}
	if !utf8.ValidString(text) {
		t.Fatal("truncated plain-text output must remain valid UTF-8")
	}
	if !strings.Contains(text, "[truncated, response exceeded 1MB]") {
		t.Fatalf("expected truncation suffix, got %q", text[len(text)-64:])
	}
}

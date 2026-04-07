//go:build fts5

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// extractText extracts the text from a CallToolResult by type-asserting Content to TextContent.
func extractText(result *gomcp.CallToolResult) string {
	for _, c := range result.Content {
		if tc, ok := c.(gomcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func TestOutputStore_StoreRetrieve(t *testing.T) {
	store := NewOutputStore(10, 5*time.Minute)
	data := []byte("hello world, this is test data")

	handle := store.Store("test_tool", data)
	if handle == "" {
		t.Fatal("expected non-empty handle")
	}

	retrieved, total, err := store.Retrieve(handle, 0, 1000)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if total != len(data) {
		t.Errorf("expected total=%d, got %d", len(data), total)
	}
	if string(retrieved) != string(data) {
		t.Errorf("expected %q, got %q", string(data), string(retrieved))
	}
}

func TestOutputStore_Pagination(t *testing.T) {
	store := NewOutputStore(10, 5*time.Minute)
	data := []byte(strings.Repeat("abcdefghij", 100)) // 1000 bytes

	handle := store.Store("test_tool", data)

	// First page
	page1, total, err := store.Retrieve(handle, 0, 400)
	if err != nil {
		t.Fatalf("Retrieve page1: %v", err)
	}
	if total != 1000 {
		t.Errorf("expected total=1000, got %d", total)
	}
	if len(page1) != 400 {
		t.Errorf("expected 400 bytes, got %d", len(page1))
	}

	// Second page
	page2, total2, err := store.Retrieve(handle, 400, 400)
	if err != nil {
		t.Fatalf("Retrieve page2: %v", err)
	}
	if total2 != 1000 {
		t.Errorf("expected total=1000, got %d", total2)
	}
	if len(page2) != 400 {
		t.Errorf("expected 400 bytes, got %d", len(page2))
	}

	// Third page (remaining 200)
	page3, total3, err := store.Retrieve(handle, 800, 400)
	if err != nil {
		t.Fatalf("Retrieve page3: %v", err)
	}
	if total3 != 1000 {
		t.Errorf("expected total=1000, got %d", total3)
	}
	if len(page3) != 200 {
		t.Errorf("expected 200 bytes, got %d", len(page3))
	}

	// Beyond end
	page4, total4, err := store.Retrieve(handle, 1000, 400)
	if err != nil {
		t.Fatalf("Retrieve page4: %v", err)
	}
	if total4 != 1000 {
		t.Errorf("expected total=1000, got %d", total4)
	}
	if len(page4) != 0 {
		t.Errorf("expected 0 bytes, got %d", len(page4))
	}
}

func TestOutputStore_TTLEviction(t *testing.T) {
	store := NewOutputStore(10, 50*time.Millisecond)
	data := []byte("ephemeral data")

	handle := store.Store("test_tool", data)

	// Should be retrievable immediately
	_, _, err := store.Retrieve(handle, 0, 100)
	if err != nil {
		t.Fatalf("expected data to be available immediately: %v", err)
	}

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Store something else to trigger eviction
	store.Store("other_tool", []byte("trigger eviction"))

	// Original handle should be gone
	_, _, err = store.Retrieve(handle, 0, 100)
	if err == nil {
		t.Fatal("expected error for expired handle")
	}
}

func TestOutputStore_MaxEntries(t *testing.T) {
	store := NewOutputStore(3, 5*time.Minute)

	// Store 4 entries (max is 3)
	h1 := store.Store("tool1", []byte("data1"))
	store.Store("tool2", []byte("data2"))
	store.Store("tool3", []byte("data3"))
	store.Store("tool4", []byte("data4"))

	// First entry should have been evicted
	_, _, err := store.Retrieve(h1, 0, 100)
	if err == nil {
		t.Fatal("expected oldest entry to be evicted")
	}
}

func TestOutputStore_HandleNotFound(t *testing.T) {
	store := NewOutputStore(10, 5*time.Minute)

	_, _, err := store.Retrieve("nonexistent", 0, 100)
	if err == nil {
		t.Fatal("expected error for nonexistent handle")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestToCallToolResultWithName_Small(t *testing.T) {
	// Initialize globalOutputStore for the test
	globalOutputStore = NewOutputStore(50, 10*time.Minute)
	defer func() { globalOutputStore = nil }()

	// Small response (< 8KB) should pass through unchanged
	small := map[string]interface{}{
		"data": "small response",
	}
	result, err := toCallToolResultWithName(small, "context")
	if err != nil {
		t.Fatalf("toCallToolResultWithName: %v", err)
	}

	// Verify it's not sandboxed
	text := extractText(result)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if _, ok := parsed["sandboxed"]; ok {
		t.Error("small response should not be sandboxed")
	}
}

func TestToCallToolResultWithName_Warning(t *testing.T) {
	// Initialize globalOutputStore for the test
	globalOutputStore = NewOutputStore(50, 10*time.Minute)
	defer func() { globalOutputStore = nil }()

	// Response between 8KB and 16KB should warn but not sandbox
	medium := map[string]interface{}{
		"data": strings.Repeat("x", 10000), // ~10KB JSON
	}
	result, err := toCallToolResultWithName(medium, "context")
	if err != nil {
		t.Fatalf("toCallToolResultWithName: %v", err)
	}

	text := extractText(result)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if _, ok := parsed["sandboxed"]; ok {
		t.Error("medium response (8-16KB) should not be sandboxed")
	}
}

func TestToCallToolResultWithName_Sandbox(t *testing.T) {
	// Initialize globalOutputStore for the test
	globalOutputStore = NewOutputStore(50, 10*time.Minute)
	defer func() { globalOutputStore = nil }()

	// Large response (> 16KB) should be sandboxed
	large := map[string]interface{}{
		"data": strings.Repeat("x", 20000),
	}
	result, err := toCallToolResultWithName(large, "context")
	if err != nil {
		t.Fatalf("toCallToolResultWithName: %v", err)
	}

	text := extractText(result)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}

	// Verify sandbox envelope
	if parsed["sandboxed"] != true {
		t.Error("expected sandboxed=true")
	}
	if parsed["handle"] == nil || parsed["handle"] == "" {
		t.Error("expected non-empty handle")
	}
	if parsed["tool"] != "context" {
		t.Errorf("expected tool=context, got %v", parsed["tool"])
	}
	if parsed["preview"] == nil {
		t.Error("expected preview to be set")
	}
	if parsed["recovery_hint"] == nil {
		t.Error("expected recovery_hint to be set")
	}
	if parsed["next_tool"] != "retrieve_output" {
		t.Errorf("expected next_tool=retrieve_output, got %v", parsed["next_tool"])
	}

	// Verify next_args
	nextArgs, ok := parsed["next_args"].(map[string]interface{})
	if !ok {
		t.Fatal("expected next_args to be a map")
	}
	if nextArgs["handle"] != parsed["handle"] {
		t.Error("next_args.handle should match handle")
	}

	// Verify size_bytes is reasonable
	sizeBytes, ok := parsed["size_bytes"].(float64)
	if !ok || sizeBytes < 20000 {
		t.Errorf("expected size_bytes >= 20000, got %v", parsed["size_bytes"])
	}
}

func TestRetrieveOutput_Integration(t *testing.T) {
	// Initialize globalOutputStore for the test
	store := NewOutputStore(50, 10*time.Minute)
	globalOutputStore = store
	defer func() { globalOutputStore = nil }()

	// Step 1: Create a large response that triggers sandboxing
	largeData := strings.Repeat("integration-test-data-", 1000) // ~22KB
	large := map[string]interface{}{
		"data": largeData,
	}
	result, err := toCallToolResultWithName(large, "understand")
	if err != nil {
		t.Fatalf("toCallToolResultWithName: %v", err)
	}

	// Step 2: Parse the sandbox envelope
	text := extractText(result)

	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("failed to parse sandbox envelope: %v", err)
	}
	if envelope["sandboxed"] != true {
		t.Fatal("expected sandboxed response")
	}

	handle := envelope["handle"].(string)

	// Step 3: Use retrieve_output handler to paginate
	page1Result, err := retrieveOutputHandler(store, RetrieveOutputParams{
		Handle: handle,
		Offset: 0,
		Limit:  4000,
	})
	if err != nil {
		t.Fatalf("retrieveOutputHandler page1: %v", err)
	}

	page1 := page1Result.(map[string]interface{})
	if page1["has_more"] != true {
		t.Error("expected has_more=true for first page")
	}
	returnedBytes := page1["returned_bytes"].(int)
	if returnedBytes != 4000 {
		t.Errorf("expected 4000 returned bytes, got %d", returnedBytes)
	}
	totalSize := page1["total_size"].(int)
	if totalSize == 0 {
		t.Error("expected non-zero total_size")
	}

	// Step 4: Retrieve next page using next_args
	nextArgs := page1["next_args"].(map[string]interface{})
	nextOffset := int(nextArgs["offset"].(int))
	nextLimit := int(nextArgs["limit"].(int))

	page2Result, err := retrieveOutputHandler(store, RetrieveOutputParams{
		Handle: handle,
		Offset: nextOffset,
		Limit:  nextLimit,
	})
	if err != nil {
		t.Fatalf("retrieveOutputHandler page2: %v", err)
	}

	page2 := page2Result.(map[string]interface{})
	if page2["total_size"].(int) != totalSize {
		t.Error("total_size should remain consistent across pages")
	}
}

func TestRetrieveOutput_LimitClamping(t *testing.T) {
	store := NewOutputStore(10, 5*time.Minute)
	store.Store("test", []byte("test data"))

	// Test default limit
	_, err := retrieveOutputHandler(store, RetrieveOutputParams{
		Handle: "nonexistent",
		Limit:  0, // should default to 4000
	})
	// Error is expected (nonexistent handle), but limit should be set
	if err == nil {
		t.Fatal("expected error for nonexistent handle")
	}

	// Test max limit clamping
	handle := store.Store("test", []byte(strings.Repeat("x", 20000)))
	result, err := retrieveOutputHandler(store, RetrieveOutputParams{
		Handle: handle,
		Limit:  99999, // should be clamped to 16000
	})
	if err != nil {
		t.Fatalf("retrieveOutputHandler: %v", err)
	}
	resp := result.(map[string]interface{})
	if resp["returned_bytes"].(int) > 16000 {
		t.Errorf("returned bytes should be clamped to 16000, got %d", resp["returned_bytes"].(int))
	}
}

func TestTruncatePreview(t *testing.T) {
	// Short text - no truncation
	short := "hello"
	if got := truncatePreview(short, 100); got != short {
		t.Errorf("expected %q, got %q", short, got)
	}

	// Long text - truncated with ellipsis
	long := strings.Repeat("x", 200)
	got := truncatePreview(long, 50)
	if len(got) > 54 { // 50 + "..."
		t.Errorf("expected truncated length <= 53, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("expected truncated text to end with ...")
	}
}

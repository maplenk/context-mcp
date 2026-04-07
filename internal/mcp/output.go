package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// OutputStore holds large tool responses for paginated retrieval.
type OutputStore struct {
	mu      sync.Mutex
	entries map[string]*outputEntry
	maxSize int
	ttl     time.Duration
}

type outputEntry struct {
	data      []byte
	createdAt time.Time
	toolName  string
	totalSize int
}

// NewOutputStore creates a new output store with the given max entries and TTL.
func NewOutputStore(maxSize int, ttl time.Duration) *OutputStore {
	return &OutputStore{
		entries: make(map[string]*outputEntry),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Store saves data and returns a handle for later retrieval.
func (s *OutputStore) Store(toolName string, data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.evictExpired()

	// Generate handle
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate handle: %w", err)
	}
	handle := hex.EncodeToString(b)

	s.entries[handle] = &outputEntry{
		data:      data,
		createdAt: time.Now(),
		toolName:  toolName,
		totalSize: len(data),
	}

	// Evict oldest if over capacity
	if len(s.entries) > s.maxSize {
		var oldestHandle string
		var oldestTime time.Time
		for h, e := range s.entries {
			if oldestHandle == "" || e.createdAt.Before(oldestTime) {
				oldestHandle = h
				oldestTime = e.createdAt
			}
		}
		if oldestHandle != "" {
			delete(s.entries, oldestHandle)
		}
	}

	return handle, nil
}

// Retrieve returns a slice of the stored data.
func (s *OutputStore) Retrieve(handle string, offset, limit int) ([]byte, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[handle]
	if !ok {
		return nil, 0, fmt.Errorf("handle %q not found or expired", handle)
	}

	total := len(entry.data)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []byte{}, total, nil
	}

	end := offset + limit
	if end > total {
		end = total
	}

	return entry.data[offset:end], total, nil
}

// evictExpired removes entries older than TTL. Must be called with lock held.
func (s *OutputStore) evictExpired() {
	now := time.Now()
	for h, e := range s.entries {
		if now.Sub(e.createdAt) > s.ttl {
			delete(s.entries, h)
		}
	}
}

// RetrieveOutputParams are the parameters for the retrieve_output tool.
type RetrieveOutputParams struct {
	Handle string `json:"handle" jsonschema:"required,description=Handle from a sandboxed response"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Byte offset to start reading from (default: 0)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Maximum bytes to return (default: 4000, max: 16000)"`
}

func retrieveOutputHandler(store *OutputStore, p RetrieveOutputParams) (interface{}, error) {
	if p.Handle == "" {
		return nil, fmt.Errorf("'handle' is required")
	}
	if p.Limit <= 0 {
		p.Limit = 4000
	}
	if p.Limit > 16000 {
		p.Limit = 16000
	}

	data, total, err := store.Retrieve(p.Handle, p.Offset, p.Limit)
	if err != nil {
		return nil, err
	}

	returned := len(data)
	remaining := total - (p.Offset + returned)
	if remaining < 0 {
		remaining = 0
	}

	resp := map[string]interface{}{
		"content":         string(data),
		"total_size":      total,
		"returned_bytes":  returned,
		"remaining_bytes": remaining,
		"has_more":        remaining > 0,
	}

	if remaining > 0 {
		resp["next_args"] = map[string]interface{}{
			"handle": p.Handle,
			"offset": p.Offset + returned,
			"limit":  p.Limit,
		}
	}

	return resp, nil
}

func registerRetrieveOutputTool(s *Server, deps ToolDeps) {
	desc := "Retrieve paginated content from a sandboxed (oversized) tool response."

	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p RetrieveOutputParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		if deps.OutputStore == nil {
			return nil, fmt.Errorf("output store not initialized")
		}
		return retrieveOutputHandler(deps.OutputStore, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "retrieve_output",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"handle": map[string]interface{}{
					"type":        "string",
					"description": "Handle from a sandboxed response",
				},
				"offset": map[string]interface{}{
					"type":        "integer",
					"description": "Byte offset to start reading from (default: 0)",
					"default":     0,
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum bytes to return (default: 4000, max: 16000)",
					"default":     4000,
				},
			},
			"required": []string{"handle"},
		},
	}, cliHandler)

	// SDK handler — registered in ALL profiles (infrastructure tool)
	tool := mcp.NewTool("retrieve_output",
		mcp.WithDescription(desc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("handle", mcp.Description("Handle from a sandboxed response"), mcp.Required()),
		mcp.WithNumber("offset", mcp.Description("Byte offset to start reading from (default: 0)")),
		mcp.WithNumber("limit", mcp.Description("Maximum bytes to return (default: 4000, max: 16000)")),
	)
	s.AddSDKTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p RetrieveOutputParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		if deps.OutputStore == nil {
			return mcp.NewToolResultError("output store not initialized"), nil
		}
		result, err := retrieveOutputHandler(deps.OutputStore, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	})
}

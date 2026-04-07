package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"sort"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolDefinition describes an MCP tool for the tools/list response.
// Retained for CLI compatibility and tool introspection.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// ToolHandler is the function signature for tool implementations.
// Retained for CLI compatibility (GetHandler / runCLI).
type ToolHandler func(params json.RawMessage) (interface{}, error)

// PendingSDKTool holds a pre-built SDK tool ready for dynamic activation.
type PendingSDKTool struct {
	Tool    mcp.Tool
	Handler server.ToolHandlerFunc
}

// Server wraps the mark3labs/mcp-go SDK server while preserving
// the GetHandler / GetTools interface used by CLI mode.
type Server struct {
	mcpServer    *server.MCPServer
	tools        []ToolDefinition
	handlers     map[string]ToolHandler
	mu           sync.Mutex
	pendingTools map[string]*PendingSDKTool // deferred tools awaiting activation
	activated    map[string]bool            // tracks which tools have been activated

	// For testing with custom I/O (nil means use stdio)
	input  io.Reader
	output io.Writer
}

// NewServer creates a new MCP server backed by the SDK, using stdin/stdout.
func NewServer() *Server {
	return &Server{
		mcpServer: server.NewMCPServer("context-mcp", Version,
			server.WithToolCapabilities(true),
			server.WithResourceCapabilities(true, false),
			server.WithPromptCapabilities(true),
		),
		handlers:     make(map[string]ToolHandler),
		pendingTools: make(map[string]*PendingSDKTool),
		activated:    make(map[string]bool),
	}
}

// NewServerWithIO creates a new MCP server with custom I/O (for testing).
func NewServerWithIO(input io.Reader, output io.Writer) *Server {
	return &Server{
		mcpServer: server.NewMCPServer("context-mcp", Version,
			server.WithToolCapabilities(true),
			server.WithResourceCapabilities(true, false),
			server.WithPromptCapabilities(true),
		),
		handlers:     make(map[string]ToolHandler),
		pendingTools: make(map[string]*PendingSDKTool),
		activated:    make(map[string]bool),
		input:        input,
		output:       output,
	}
}

// RegisterTool registers a tool definition and a json.RawMessage-based
// handler for CLI mode only. Call AddSDKTool separately to register
// the typed handler with the MCP SDK for protocol mode.
func (s *Server) RegisterTool(def ToolDefinition, handler ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Prevent duplicate registrations
	for _, t := range s.tools {
		if t.Name == def.Name {
			return // Already registered
		}
	}
	s.tools = append(s.tools, def)
	s.handlers[def.Name] = handler
}

// AddSDKTool registers a tool with the underlying MCP SDK server.
func (s *Server) AddSDKTool(tool mcp.Tool, handler server.ToolHandlerFunc) {
	s.mcpServer.AddTool(tool, handler)
}

// AddResource registers a resource with the underlying MCP SDK server.
func (s *Server) AddResource(resource mcp.Resource, handler server.ResourceHandlerFunc) {
	s.mcpServer.AddResource(resource, handler)
}

// AddPrompt registers a prompt with the underlying MCP SDK server.
func (s *Server) AddPrompt(prompt mcp.Prompt, handler server.PromptHandlerFunc) {
	s.mcpServer.AddPrompt(prompt, handler)
}

// GetHandler returns the handler for a named tool, or false if not found.
func (s *Server) GetHandler(name string) (ToolHandler, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handlers[name]
	return h, ok
}

// GetTools returns a copy of all registered tool definitions.
func (s *Server) GetTools() []ToolDefinition {
	s.mu.Lock()
	defer s.mu.Unlock()
	tools := make([]ToolDefinition, len(s.tools))
	copy(tools, s.tools)
	return tools
}

// StorePendingTool saves an SDK tool for later activation.
func (s *Server) StorePendingTool(name string, tool mcp.Tool, handler server.ToolHandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingTools[name] = &PendingSDKTool{Tool: tool, Handler: handler}
}

// ActivateTool moves a tool from pending to active via mcpServer.AddTool().
// Returns true if the tool was activated, false if not found or already active.
func (s *Server) ActivateTool(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activated[name] {
		return false
	}
	pt, ok := s.pendingTools[name]
	if !ok {
		return false
	}
	s.mcpServer.AddTool(pt.Tool, pt.Handler)
	s.activated[name] = true
	delete(s.pendingTools, name)
	return true
}

// ListPending returns names of tools that haven't been activated yet.
func (s *Server) ListPending() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.pendingTools))
	for name := range s.pendingTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsActivated returns true if a tool has been activated (or was initially registered).
func (s *Server) IsActivated(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activated[name]
}

// MCPServer returns the underlying mark3labs MCPServer for custom transport usage
// (e.g., streamable HTTP).
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}

// Serve starts the MCP server using the SDK's protocol handler.
// It blocks until the transport is closed (stdin EOF) or an error occurs.
func (s *Server) Serve() error {
	// Route logging to stderr to avoid corrupting the JSON-RPC stream.
	log.SetOutput(os.Stderr)

	if s.input != nil && s.output != nil {
		// Testing mode: use StdioServer with custom I/O
		stdioServer := server.NewStdioServer(s.mcpServer)
		return stdioServer.Listen(context.Background(), s.input, s.output)
	}

	// Production mode: serve on stdin/stdout
	return server.ServeStdio(s.mcpServer)
}

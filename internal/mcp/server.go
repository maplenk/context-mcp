package mcp

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"

	mcp_golang "github.com/metoro-io/mcp-golang"
	"github.com/metoro-io/mcp-golang/transport/stdio"
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

// Server wraps the metoro-io/mcp-golang SDK server while preserving
// the GetHandler / GetTools interface used by CLI mode.
type Server struct {
	sdk      *mcp_golang.Server
	tools    []ToolDefinition
	handlers map[string]ToolHandler
	mu       sync.Mutex
}

// NewServer creates a new MCP server backed by the SDK, using stdin/stdout.
func NewServer() *Server {
	return &Server{
		sdk: mcp_golang.NewServer(
			stdio.NewStdioServerTransport(),
			mcp_golang.WithName("qb-context"),
			mcp_golang.WithVersion("0.1.0"),
		),
		handlers: make(map[string]ToolHandler),
	}
}

// NewServerWithIO creates a new MCP server with custom I/O (for testing).
// Uses the SDK's stdio transport with the provided reader/writer.
func NewServerWithIO(input io.Reader, output io.Writer) *Server {
	return &Server{
		sdk: mcp_golang.NewServer(
			stdio.NewStdioServerTransportWithIO(input, output),
			mcp_golang.WithName("qb-context"),
			mcp_golang.WithVersion("0.1.0"),
		),
		handlers: make(map[string]ToolHandler),
	}
}

// RegisterTool registers a tool definition and a json.RawMessage-based
// handler for CLI mode only. Call RegisterSDKTool separately to register
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

// RegisterSDKTool registers a typed handler with the underlying SDK server.
// The handler parameter must conform to the SDK's expectations:
//
//	func(ArgsStruct) (*mcp_golang.ToolResponse, error)
//
// where ArgsStruct is a struct with json/jsonschema tags.
func (s *Server) RegisterSDKTool(name, description string, handler interface{}) error {
	return s.sdk.RegisterTool(name, description, handler)
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

// Serve starts the MCP server using the SDK's protocol handler.
// It blocks until the transport is closed (stdin EOF) or an error occurs.
func (s *Server) Serve() error {
	// Route logging to stderr to avoid corrupting the JSON-RPC stream.
	log.SetOutput(os.Stderr)

	return s.sdk.Serve()
}

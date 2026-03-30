package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// JSONRPCRequest represents an incoming JSON-RPC 2.0 request
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents an outgoing JSON-RPC 2.0 response
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ToolDefinition describes an MCP tool for the tools/list response
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// ToolHandler is the function signature for tool implementations
type ToolHandler func(params json.RawMessage) (interface{}, error)

// maxConcurrentRequests caps the number of request-handler goroutines that
// may run simultaneously, preventing unbounded goroutine/memory growth.
const maxConcurrentRequests = 10

// Server is the MCP server that communicates over stdio
type Server struct {
	tools    []ToolDefinition
	handlers map[string]ToolHandler
	mu       sync.Mutex
	sema     chan struct{} // semaphore for bounded concurrency
	input    io.Reader
	output   io.Writer
}

// NewServer creates a new MCP server using stdin/stdout
func NewServer() *Server {
	return &Server{
		handlers: make(map[string]ToolHandler),
		sema:     make(chan struct{}, maxConcurrentRequests),
		input:    os.Stdin,
		output:   os.Stdout,
	}
}

// NewServerWithIO creates a new MCP server with custom I/O (for testing)
func NewServerWithIO(input io.Reader, output io.Writer) *Server {
	return &Server{
		handlers: make(map[string]ToolHandler),
		sema:     make(chan struct{}, maxConcurrentRequests),
		input:    input,
		output:   output,
	}
}

// RegisterTool registers a tool with its handler
func (s *Server) RegisterTool(def ToolDefinition, handler ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = append(s.tools, def)
	s.handlers[def.Name] = handler
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

// Serve starts the server and processes requests until EOF or error
func (s *Server) Serve() error {
	scanner := bufio.NewScanner(s.input)
	// Increase buffer size for large requests
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("Failed to parse JSON-RPC request: %v", err)
			s.sendError(nil, -32700, "Parse error", nil)
			continue
		}

		s.sema <- struct{}{} // acquire concurrency slot
		go func(r JSONRPCRequest) {
			defer func() { <-s.sema }() // release slot when done
			s.handleRequest(r)
		}(req)
	}

	return scanner.Err()
}

// handleRequest routes a request to the appropriate handler
func (s *Server) handleRequest(req JSONRPCRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolsCall(req)
	case "notifications/initialized":
		// Client acknowledgment, no response needed
		return
	case "ping":
		s.sendResult(req.ID, map[string]string{})
	default:
		// Don't respond to notifications (requests without an ID)
		if req.ID == nil {
			return
		}
		s.sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method), nil)
	}
}

// handleInitialize responds to the MCP initialize request
func (s *Server) handleInitialize(req JSONRPCRequest) {
	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "qb-context",
			"version": "0.1.0",
		},
	}
	s.sendResult(req.ID, result)
}

// handleToolsList returns the list of available tools
func (s *Server) handleToolsList(req JSONRPCRequest) {
	s.mu.Lock()
	tools := make([]ToolDefinition, len(s.tools))
	copy(tools, s.tools)
	s.mu.Unlock()

	result := map[string]interface{}{
		"tools": tools,
	}
	s.sendResult(req.ID, result)
}

// handleToolsCall dispatches a tool call to its handler
func (s *Server) handleToolsCall(req JSONRPCRequest) {
	var callParams struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}

	if err := json.Unmarshal(req.Params, &callParams); err != nil {
		s.sendError(req.ID, -32602, "Invalid params", nil)
		return
	}

	s.mu.Lock()
	handler, ok := s.handlers[callParams.Name]
	s.mu.Unlock()

	if !ok {
		s.sendError(req.ID, -32602, fmt.Sprintf("Unknown tool: %s", callParams.Name), nil)
		return
	}

	result, err := handler(callParams.Arguments)
	if err != nil {
		// Return tool error as content, not as JSON-RPC error
		s.sendResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": fmt.Sprintf("Error: %v", err),
				},
			},
			"isError": true,
		})
		return
	}

	// Format successful result
	var text string
	switch v := result.(type) {
	case string:
		text = v
	default:
		jsonBytes, marshalErr := json.MarshalIndent(v, "", "  ")
		if marshalErr != nil {
			text = fmt.Sprintf("Error marshaling result: %v", marshalErr)
		} else {
			text = string(jsonBytes)
		}
	}

	s.sendResult(req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": text,
			},
		},
	})
}

// sendResult sends a successful JSON-RPC response
func (s *Server) sendResult(id interface{}, result interface{}) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	s.writeResponse(resp)
}

// sendError sends a JSON-RPC error response
func (s *Server) sendError(id interface{}, code int, message string, data interface{}) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	s.writeResponse(resp)
}

// writeResponse serializes and writes a response to stdout
func (s *Server) writeResponse(resp JSONRPCResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("Failed to marshal response: %v", err)
		return
	}

	// Write as a single line followed by newline
	fmt.Fprintf(s.output, "%s\n", data)
}

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// taskBundle groups related tools for activation as a unit.
type taskBundle struct {
	Name        string
	Description string
	Tools       []string
	Keywords    []string
}

var taskBundles = []taskBundle{
	{
		Name:        "inspection",
		Description: "Search, read, and understand code symbols",
		Tools:       []string{"context", "read_symbol", "list_file_symbols", "understand"},
		Keywords:    []string{"search", "find", "read", "code", "source", "definition", "symbol", "inspect", "look", "understand"},
	},
	{
		Name:        "change_analysis",
		Description: "Assess impact, trace call paths, detect changes",
		Tools:       []string{"impact", "detect_changes", "trace_call_path"},
		Keywords:    []string{"impact", "blast", "radius", "risk", "change", "trace", "call", "path", "break", "affect", "downstream", "diff"},
	},
	{
		Name:        "architecture",
		Description: "Explore architecture, key symbols, and codebase structure",
		Tools:       []string{"get_architecture_summary", "explore", "get_key_symbols"},
		Keywords:    []string{"architecture", "structure", "modules", "overview", "navigate", "key", "hub", "entry", "onboard"},
	},
	{
		Name:        "assembly",
		Description: "Token-budgeted context assembly and snapshots",
		Tools:       []string{"assemble_context", "checkpoint_context", "read_delta", "search_code"},
		Keywords:    []string{"assemble", "budget", "token", "context", "checkpoint", "delta", "snapshot", "regex", "search", "pattern"},
	},
}

// DiscoverToolsParams are the parameters for the discover_tools tool.
type DiscoverToolsParams struct {
	Need     string   `json:"need,omitempty" jsonschema:"description=Describe your task and the most relevant tool bundle will be activated."`
	Activate []string `json:"activate,omitempty" jsonschema:"description=Specific tool names to activate directly (bypasses bundle matching, max 5)"`
}

// ActivatedTool describes a newly activated tool.
type ActivatedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DiscoverToolsResponse is the response from discover_tools.
type DiscoverToolsResponse struct {
	Bundle           string          `json:"bundle"`
	Reason           string          `json:"reason"`
	Activated        []ActivatedTool `json:"activated"`
	ActivatedCount   int             `json:"activated_count"`
	ActivationCapped bool            `json:"activation_capped"`
	AlreadyActive    []string        `json:"already_active,omitempty"`
	Pending          []string        `json:"pending"`
}

const maxActivatePerCall = 5

// discoverToolsHandler implements the discover_tools tool.
func discoverToolsHandler(s *Server, p DiscoverToolsParams) (*DiscoverToolsResponse, error) {
	// Direct activation mode
	if len(p.Activate) > 0 {
		return activateDirectly(s, p.Activate)
	}

	if p.Need == "" {
		return nil, fmt.Errorf("'need' is required when 'activate' is not provided")
	}

	// Score bundles by keyword hits
	needLower := strings.ToLower(p.Need)
	words := strings.Fields(needLower)

	type scored struct {
		bundle  taskBundle
		score   int
		matched []string
	}
	var scores []scored
	for _, b := range taskBundles {
		var sc int
		var matched []string
		for _, kw := range b.Keywords {
			for _, w := range words {
				if strings.Contains(w, kw) || strings.Contains(kw, w) {
					sc++
					matched = append(matched, kw)
					break
				}
			}
		}
		scores = append(scores, scored{bundle: b, score: sc, matched: matched})
	}

	// Sort by score descending
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	// Pick top bundle(s)
	var bundlesToActivate []scored
	if len(scores) > 0 && scores[0].score >= 2 {
		bundlesToActivate = append(bundlesToActivate, scores[0])
		// Activate second bundle only if confidence is low (top < 2x second)
		if len(scores) > 1 && scores[1].score > 0 && scores[0].score < 2*scores[1].score {
			bundlesToActivate = append(bundlesToActivate, scores[1])
		}
	} else if len(scores) > 0 && scores[0].score > 0 {
		bundlesToActivate = append(bundlesToActivate, scores[0])
	}

	// Fallback: per-tool keyword matching
	if len(bundlesToActivate) == 0 {
		return fallbackToolMatch(s, words)
	}

	// Activate bundles
	var activated []ActivatedTool
	var alreadyActive []string
	bundleName := bundlesToActivate[0].bundle.Name
	reason := fmt.Sprintf("Matched keywords: %s", strings.Join(bundlesToActivate[0].matched, ", "))

	if len(bundlesToActivate) > 1 {
		bundleName = bundlesToActivate[0].bundle.Name + "+" + bundlesToActivate[1].bundle.Name
		allMatched := make([]string, 0, len(bundlesToActivate[0].matched)+len(bundlesToActivate[1].matched))
		allMatched = append(allMatched, bundlesToActivate[0].matched...)
		allMatched = append(allMatched, bundlesToActivate[1].matched...)
		reason = fmt.Sprintf("Matched keywords: %s", strings.Join(allMatched, ", "))
	}

	capped := false
	for _, bs := range bundlesToActivate {
		for _, toolName := range bs.bundle.Tools {
			if len(activated) >= maxActivatePerCall {
				capped = true
				break
			}
			if s.IsActivated(toolName) {
				alreadyActive = append(alreadyActive, toolName)
				continue
			}
			if s.ActivateTool(toolName) {
				// Get description from CLI tools
				desc := getToolDescription(s, toolName)
				activated = append(activated, ActivatedTool{Name: toolName, Description: desc})
			}
		}
		if capped {
			break
		}
	}

	return &DiscoverToolsResponse{
		Bundle:           bundleName,
		Reason:           reason,
		Activated:        activated,
		ActivatedCount:   len(activated),
		ActivationCapped: capped,
		AlreadyActive:    alreadyActive,
		Pending:          s.ListPending(),
	}, nil
}

// activateDirectly activates specific tools by name.
func activateDirectly(s *Server, names []string) (*DiscoverToolsResponse, error) {
	capped := len(names) > maxActivatePerCall
	if len(names) > maxActivatePerCall {
		names = names[:maxActivatePerCall]
	}

	var activated []ActivatedTool
	var alreadyActive []string

	for _, name := range names {
		if s.IsActivated(name) {
			alreadyActive = append(alreadyActive, name)
			continue
		}
		if s.ActivateTool(name) {
			desc := getToolDescription(s, name)
			activated = append(activated, ActivatedTool{Name: name, Description: desc})
		}
	}

	return &DiscoverToolsResponse{
		Bundle:           "direct",
		Reason:           fmt.Sprintf("Directly activated: %s", strings.Join(names, ", ")),
		Activated:        activated,
		ActivatedCount:   len(activated),
		ActivationCapped: capped,
		AlreadyActive:    alreadyActive,
		Pending:          s.ListPending(),
	}, nil
}

// fallbackToolMatch activates individual tools based on keyword matching.
func fallbackToolMatch(s *Server, words []string) (*DiscoverToolsResponse, error) {
	// Build tool -> keywords map from all bundles
	toolKeywords := make(map[string][]string)
	for _, b := range taskBundles {
		for _, t := range b.Tools {
			toolKeywords[t] = append(toolKeywords[t], b.Keywords...)
		}
	}

	type toolScore struct {
		name    string
		score   int
		matched []string
	}

	var scores []toolScore
	for tool, keywords := range toolKeywords {
		var sc int
		var matched []string
		for _, kw := range keywords {
			for _, w := range words {
				if strings.Contains(w, kw) || strings.Contains(kw, w) {
					sc++
					matched = append(matched, kw)
					break
				}
			}
		}
		if sc > 0 {
			scores = append(scores, toolScore{name: tool, score: sc, matched: matched})
		}
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	// Max 3 tools via fallback
	maxFallback := 3
	if len(scores) > maxFallback {
		scores = scores[:maxFallback]
	}

	var activated []ActivatedTool
	var alreadyActive []string
	var allMatched []string

	for _, ts := range scores {
		if s.IsActivated(ts.name) {
			alreadyActive = append(alreadyActive, ts.name)
			continue
		}
		if s.ActivateTool(ts.name) {
			desc := getToolDescription(s, ts.name)
			activated = append(activated, ActivatedTool{Name: ts.name, Description: desc})
		}
		allMatched = append(allMatched, ts.matched...)
	}

	return &DiscoverToolsResponse{
		Bundle:           "fallback",
		Reason:           fmt.Sprintf("Matched keywords: %s", strings.Join(allMatched, ", ")),
		Activated:        activated,
		ActivatedCount:   len(activated),
		ActivationCapped: false,
		AlreadyActive:    alreadyActive,
		Pending:          s.ListPending(),
	}, nil
}

// getToolDescription gets a tool's description from the CLI tools list.
func getToolDescription(s *Server, name string) string {
	for _, t := range s.GetTools() {
		if t.Name == name {
			return t.Description
		}
	}
	return ""
}

// --- execute_tool ---

// ExecuteToolParams are the parameters for the execute_tool fallback proxy.
type ExecuteToolParams struct {
	Name string         `json:"name" jsonschema:"required,description=Name of the tool to execute"`
	Args map[string]any `json:"args" jsonschema:"required,description=Tool arguments as a JSON object"`
}

// executeToolHandler proxies tool calls through the CLI handler system.
func executeToolHandler(s *Server, p ExecuteToolParams) (interface{}, error) {
	handler, ok := s.GetHandler(p.Name)
	if !ok {
		return nil, fmt.Errorf("tool %q not found", p.Name)
	}

	argsJSON, err := json.Marshal(p.Args)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal args: %w", err)
	}

	result, err := handler(argsJSON)
	if err != nil {
		return nil, err
	}

	// Wrap with warning if tool is not activated
	if !s.IsActivated(p.Name) {
		return map[string]interface{}{
			"proxy_warning": map[string]interface{}{
				"tool_not_activated": true,
				"message":           fmt.Sprintf("Tool '%s' is not yet activated. Consider calling discover_tools first for native tool access.", p.Name),
			},
			"result": result,
		}, nil
	}

	return result, nil
}

// --- Registration ---

func registerDiscoverToolsTool(s *Server, deps ToolDeps) {
	desc := "Start here in minimal mode. Activates the most relevant tool bundle for your task. Bundles: inspection (search/read/understand code), change_analysis (impact/trace/detect changes), architecture (structure/modules/key symbols), assembly (token-budgeted context/snapshots). After activation, new tools appear in your tool list — call them directly."

	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p DiscoverToolsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return discoverToolsHandler(s, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "discover_tools",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"need": map[string]interface{}{
					"type":        "string",
					"description": "Describe your task and the most relevant tool bundle will be activated",
				},
				"activate": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Specific tool names to activate directly (max 5)",
				},
			},
		},
	}, cliHandler)

	// discover_tools SDK registration
	tool := mcp.NewTool("discover_tools",
		mcp.WithDescription(desc),
		mcp.WithString("need", mcp.Description("Describe your task and the most relevant tool bundle will be activated")),
		mcp.WithArray("activate", mcp.Description("Specific tool names to activate directly (max 5)"), mcp.WithStringItems()),
	)
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p DiscoverToolsParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := discoverToolsHandler(s, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("discover_tools", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("discover_tools", tool, sdkHandler)
	}
}

func registerExecuteToolTool(s *Server, deps ToolDeps) {
	desc := "Fallback for clients that have not yet activated or refreshed tool lists. Prefer calling discover_tools first, then use native tool calls when available."

	cliHandler := func(params json.RawMessage) (interface{}, error) {
		var p ExecuteToolParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
		return executeToolHandler(s, p)
	}

	s.RegisterTool(ToolDefinition{
		Name:        "execute_tool",
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Name of the tool to execute",
				},
				"args": map[string]interface{}{
					"type":        "object",
					"description": "Tool arguments as a JSON object",
				},
			},
			"required": []string{"name", "args"},
		},
	}, cliHandler)

	tool := mcp.NewTool("execute_tool",
		mcp.WithDescription(desc),
		mcp.WithString("name", mcp.Description("Name of the tool to execute"), mcp.Required()),
		mcp.WithObject("args", mcp.Description("Tool arguments as a JSON object"), mcp.Required()),
	)
	sdkHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p ExecuteToolParams
		if err := req.BindArguments(&p); err != nil {
			return mcp.NewToolResultError("invalid parameters: " + err.Error()), nil
		}
		result, err := executeToolHandler(s, p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toCallToolResult(result)
	}

	if isToolInProfile("execute_tool", deps.Profile) {
		s.AddSDKTool(tool, sdkHandler)
	} else {
		s.StorePendingTool("execute_tool", tool, sdkHandler)
	}
}

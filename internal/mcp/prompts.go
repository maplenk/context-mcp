package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// RegisterPrompts registers all MCP prompt templates with the server.
func RegisterPrompts(s *Server) {
	registerReviewChangesPrompt(s)
	registerTraceImpactPrompt(s)
	registerPrepareFixContextPrompt(s)
	registerOnboardRepoPrompt(s)
	registerCollectMinimalContextPrompt(s)
}

func registerReviewChangesPrompt(s *Server) {
	s.AddPrompt(
		mcp.NewPrompt("review_changes",
			mcp.WithPromptDescription("Review recent code changes and their impact"),
			mcp.WithArgument("since", mcp.ArgumentDescription("Git ref to compare against (e.g., HEAD~5, main). Default: HEAD~1")),
		),
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			since := req.Params.Arguments["since"]
			if since == "" {
				since = "HEAD~1"
			}
			return mcp.NewGetPromptResult(
				"Review changes since "+since,
				[]mcp.PromptMessage{
					mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(fmt.Sprintf(`Review recent code changes and their impact.

Steps:
1. Call detect_changes with since="%s" to find what changed
2. For the top 3 highest-impact changes, call understand on each symbol to see its relationships
3. Call impact on the #1 ranked change to see its blast radius
4. Summarize: what changed, what other code is affected, and any potential risks or regressions`, since))),
				},
			), nil
		},
	)
}

func registerTraceImpactPrompt(s *Server) {
	s.AddPrompt(
		mcp.NewPrompt("trace_impact",
			mcp.WithPromptDescription("Trace the blast radius of changes to a symbol"),
			mcp.WithArgument("symbol", mcp.ArgumentDescription("Symbol name or ID to trace"), mcp.RequiredArgument()),
		),
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			symbol := req.Params.Arguments["symbol"]
			if symbol == "" {
				return nil, fmt.Errorf("symbol argument is required")
			}
			return mcp.NewGetPromptResult(
				"Trace impact of "+symbol,
				[]mcp.PromptMessage{
					mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(fmt.Sprintf(`Trace the blast radius of changes to the symbol "%s".

Steps:
1. Call impact on "%s" to find all downstream dependents and upstream dependencies
2. Call trace_call_path for the top 3 upstream callers to understand how they reach this symbol
3. Summarize the blast radius: how many symbols are affected, which files are involved, and recommend specific test coverage for the impacted areas`, symbol, symbol))),
				},
			), nil
		},
	)
}

func registerPrepareFixContextPrompt(s *Server) {
	s.AddPrompt(
		mcp.NewPrompt("prepare_fix_context",
			mcp.WithPromptDescription("Gather context needed to fix a bug in a specific area"),
			mcp.WithArgument("description", mcp.ArgumentDescription("Description of the bug to fix"), mcp.RequiredArgument()),
			mcp.WithArgument("file", mcp.ArgumentDescription("Suspected file path (optional)")),
		),
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			description := req.Params.Arguments["description"]
			if description == "" {
				return nil, fmt.Errorf("description argument is required")
			}
			file := req.Params.Arguments["file"]
			fileHint := ""
			if file != "" {
				fileHint = fmt.Sprintf("\nHint: The bug is suspected to be in file: %s", file)
			}
			return mcp.NewGetPromptResult(
				"Prepare fix context for: "+description,
				[]mcp.PromptMessage{
					mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(fmt.Sprintf(`Gather context needed to fix this bug: "%s"%s

Steps:
1. Call context with the bug description to find relevant code symbols
2. Call read_symbol on the top 3 results to read their source code
3. Call understand on the most relevant symbol to see its relationships and dependencies
4. Prepare a concise summary of the code area, the likely root cause, and a suggested fix approach`, description, fileHint))),
				},
			), nil
		},
	)
}

func registerOnboardRepoPrompt(s *Server) {
	s.AddPrompt(
		mcp.NewPrompt("onboard_repo",
			mcp.WithPromptDescription("Get oriented in this codebase"),
		),
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return mcp.NewGetPromptResult(
				"Onboard to this repository",
				[]mcp.PromptMessage{
					mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(`Get oriented in this codebase.

Steps:
1. Call context with query "main entry points and architecture" to find key code
2. Call get_architecture_summary to understand the high-level module structure and communities
3. Call get_key_symbols to find the most important symbols by PageRank and connectivity
4. Summarize the repo's architecture, key modules, main entry points, and how the major components interact`)),
				},
			), nil
		},
	)
}

func registerCollectMinimalContextPrompt(s *Server) {
	s.AddPrompt(
		mcp.NewPrompt("collect_minimal_context",
			mcp.WithPromptDescription("Collect the minimum context needed for a task within a token budget"),
			mcp.WithArgument("task", mcp.ArgumentDescription("Description of the task"), mcp.RequiredArgument()),
			mcp.WithArgument("budget", mcp.ArgumentDescription("Token budget (default: 4000)")),
		),
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			task := req.Params.Arguments["task"]
			if task == "" {
				return nil, fmt.Errorf("task argument is required")
			}
			budget := req.Params.Arguments["budget"]
			if budget == "" {
				budget = "4000"
			}
			return mcp.NewGetPromptResult(
				"Collect minimal context for: "+task,
				[]mcp.PromptMessage{
					mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(fmt.Sprintf(`Collect the minimum context needed for this task: "%s"
Token budget: %s

Steps:
1. Call context with the task description to find relevant symbols
2. Call assemble_context with task="%s", budget=%s, and mode="snippets" to get optimally ranked context within the token budget
3. Present the assembled context in order of relevance, showing symbol names, file paths, and code snippets`, task, budget, task, budget))),
				},
			), nil
		},
	)
}

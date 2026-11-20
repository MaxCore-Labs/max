package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/app/tools"
)

type emptyArgs struct{}

type notifyOwnerArgs struct {
	Text    string `json:"text" jsonschema:"notification message for the owner"`
	Urgency string `json:"urgency,omitempty" jsonschema:"normal or high"`
}

type askOwnerArgs struct {
	Question string   `json:"question" jsonschema:"single concise question for the owner"`
	Choices  []string `json:"choices,omitempty" jsonschema:"optional candidate answers"`
}

type noteUpdateArgs struct {
	Content string `json:"content" jsonschema:"markdown content for NOTES.md (<= 80 lines)"`
}

func registerCoreTools(mcpSrv *mcp.Server, svc *tools.Service, caller tools.Caller) {
	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "ping",
		Description: "Verify Max MCP connectivity and active agent run identity.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		return toMCPResult(svc.CapOutput(caller, "ping", svc.Ping(ctx, caller).Text)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "notify_owner",
		Description: "Send a non-blocking notification to the company owner's Inbox.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args notifyOwnerArgs) (*mcp.CallToolResult, any, error) {
		res := svc.NotifyOwner(ctx, caller, args.Text, args.Urgency)
		return toMCPResult(maybeCap(svc, caller, "notify_owner", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "ask_owner",
		Description: "Ask the owner one question and park the conversation; end your turn right after.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args askOwnerArgs) (*mcp.CallToolResult, any, error) {
		res := svc.AskOwner(ctx, caller, args.Question, args.Choices)
		return toMCPResult(maybeCap(svc, caller, "ask_owner", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "note_update",
		Description: "Replace private working notes in agents/<slug>/notes/NOTES.md (max 80 lines).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args noteUpdateArgs) (*mcp.CallToolResult, any, error) {
		res := svc.NoteUpdate(ctx, caller, args.Content)
		return toMCPResult(maybeCap(svc, caller, "note_update", res)), nil, nil
	})

	registerCoordTools(mcpSrv, svc, caller)
	registerMemoryTools(mcpSrv, svc, caller)
	registerProposeTools(mcpSrv, svc, caller)
	registerBrowserTools(mcpSrv, svc, caller)
}

func maybeCap(svc *tools.Service, c tools.Caller, name string, r tools.Result) tools.Result {
	if r.IsError {
		return r
	}
	return svc.CapOutput(c, name, r.Text)
}

// RegisteredToolNames returns the exact MCP tool names exposed to agents (used in security boundary tests).
func RegisteredToolNames(_ *tools.Service) []string {
	return []string{
		"ping",
		"notify_owner",
		"ask_owner",
		"note_update",
		"task_create",
		"task_update",
		"task_comment",
		"task_get",
		"task_list",
		"delegate",
		"message_agent",
		"request_review",
		"memory_search",
		"memory_save",
		"remember_fact",
		"routine_propose",
		"agent_propose",
		"instruction_propose",
		"handoff",
	}
}

func toMCPResult(r tools.Result) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: r.IsError,
		Content: []mcp.Content{
			&mcp.TextContent{Text: r.Text},
		},
	}
}

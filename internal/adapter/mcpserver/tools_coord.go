package mcpserver

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/app/tools"
)

type taskCreateArgs struct {
	Title        string   `json:"title" jsonschema:"concise task title"`
	Description  string   `json:"description" jsonschema:"task scope and context"`
	DoneCriteria string   `json:"done_criteria,omitempty" jsonschema:"objective completion criteria"`
	GoalID       string   `json:"goal_id,omitempty" jsonschema:"optional goal id"`
	BlockedBy    []string `json:"blocked_by,omitempty" jsonschema:"task ids that must complete first"`
	NeedsReview  bool     `json:"needs_review,omitempty" jsonschema:"whether completion requires peer/owner review"`
	DueAt        string   `json:"due_at,omitempty" jsonschema:"optional RFC3339 deadline"`
}

type taskUpdateArgs struct {
	TaskID        string `json:"task_id" jsonschema:"target task id"`
	Status        string `json:"status" jsonschema:"todo, in_progress, blocked, waiting_owner, in_review, done, or cancelled"`
	BlockedReason string `json:"blocked_reason,omitempty" jsonschema:"dependency, stalled, error, or needs_owner"`
}

type taskCommentArgs struct {
	TaskID string `json:"task_id" jsonschema:"target task id"`
	Body   string `json:"body" jsonschema:"comment markdown"`
}

type taskGetArgs struct {
	TaskID string `json:"task_id" jsonschema:"target task id"`
}

type taskListArgs struct {
	Status string `json:"status,omitempty" jsonschema:"optional status filter"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum rows (<= 100)"`
}

type delegateArgs struct {
	To           string   `json:"to" jsonschema:"target agent slug or id"`
	Title        string   `json:"title" jsonschema:"child task title"`
	Description  string   `json:"description" jsonschema:"child task instructions"`
	DoneCriteria string   `json:"done_criteria,omitempty" jsonschema:"child completion criteria"`
	BlockedBy    []string `json:"blocked_by,omitempty" jsonschema:"blocker task ids"`
	NeedsReview  bool     `json:"needs_review,omitempty" jsonschema:"require review before done"`
	DueAt        string   `json:"due_at,omitempty" jsonschema:"optional RFC3339 deadline"`
}

type messageAgentArgs struct {
	To   string `json:"to" jsonschema:"recipient agent slug or id"`
	Body string `json:"body" jsonschema:"message body recorded on the active task"`
}

func parseOptionalRFC3339(raw string) *time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	return nil
}

func registerCoordTools(mcpSrv *mcp.Server, svc *tools.Service, caller tools.Caller) {
	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "task_create",
		Description: "Create a root task tracked in Max.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a taskCreateArgs) (*mcp.CallToolResult, any, error) {
		var goalPtr *string
		if strings.TrimSpace(a.GoalID) != "" {
			g := strings.TrimSpace(a.GoalID)
			goalPtr = &g
		}
		res := svc.TaskCreate(ctx, caller, a.Title, a.Description, a.DoneCriteria, goalPtr, a.BlockedBy, a.NeedsReview, parseOptionalRFC3339(a.DueAt))
		return toMCPResult(maybeCap(svc, caller, "task_create", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "task_update",
		Description: "Transition a task's status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a taskUpdateArgs) (*mcp.CallToolResult, any, error) {
		res := svc.TaskUpdate(ctx, caller, a.TaskID, a.Status, a.BlockedReason)
		return toMCPResult(maybeCap(svc, caller, "task_update", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "task_comment",
		Description: "Append a progress or review comment to a task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a taskCommentArgs) (*mcp.CallToolResult, any, error) {
		res := svc.TaskComment(ctx, caller, a.TaskID, a.Body)
		return toMCPResult(maybeCap(svc, caller, "task_comment", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "task_get",
		Description: "Read task details and recent comments.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a taskGetArgs) (*mcp.CallToolResult, any, error) {
		return toMCPResult(svc.TaskGet(ctx, caller, a.TaskID)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "task_list",
		Description: "List tasks visible to your agent and team.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a taskListArgs) (*mcp.CallToolResult, any, error) {
		return toMCPResult(svc.TaskList(ctx, caller, a.Status, a.Limit)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "delegate",
		Description: "Delegate a child task to another agent and park on children.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a delegateArgs) (*mcp.CallToolResult, any, error) {
		res := svc.Delegate(ctx, caller, a.To, a.Title, a.Description, a.DoneCriteria, a.BlockedBy, a.NeedsReview, parseOptionalRFC3339(a.DueAt))
		return toMCPResult(maybeCap(svc, caller, "delegate", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "message_agent",
		Description: "Send a task-scoped message to another agent.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a messageAgentArgs) (*mcp.CallToolResult, any, error) {
		res := svc.MessageAgent(ctx, caller, a.To, a.Body)
		return toMCPResult(maybeCap(svc, caller, "message_agent", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "request_review",
		Description: "Submit the active task for review and end your turn.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		res := svc.RequestReview(ctx, caller)
		return toMCPResult(maybeCap(svc, caller, "request_review", res)), nil, nil
	})
}

package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/app/tools"
	"max/internal/domain"
)

type rememberFactArgs struct {
	Slug          string `json:"slug" jsonschema:"kebab-case fact slug"`
	Type          string `json:"type,omitempty" jsonschema:"owner or company"`
	Statement     string `json:"statement" jsonschema:"concise verified fact statement"`
	Confidence    int    `json:"confidence" jsonschema:"confidence score 2-5"`
	EvidenceMsgID string `json:"evidence_msg_id" jsonschema:"exact owner message ID proving the fact"`
	Supersedes    string `json:"supersedes,omitempty" jsonschema:"optional older fact slug superseded by this fact"`
}

type routineProposeArgs struct {
	Slug     string `json:"slug" jsonschema:"kebab-case routine slug"`
	Title    string `json:"title" jsonschema:"routine display title"`
	Schedule string `json:"schedule" jsonschema:"5-field cron schedule"`
	Body     string `json:"body" jsonschema:"routine markdown instructions"`
}

type agentProposeArgs struct {
	Slug     string `json:"slug" jsonschema:"kebab-case agent slug"`
	Template string `json:"template" jsonschema:"role template slug"`
	Team     string `json:"team" jsonschema:"target team slug"`
	Reason   string `json:"reason" jsonschema:"business justification for the owner"`
}

type instructionProposeArgs struct {
	TargetPath string `json:"target_path" jsonschema:"vault path to AGENT.md, TEAM.md, or company/COMPANY.md"`
	Title      string `json:"title" jsonschema:"short summary of the proposed edit"`
	Diff       string `json:"diff" jsonschema:"unified diff against target_path"`
}

func registerProposeTools(mcpSrv *mcp.Server, svc *tools.Service, caller tools.Caller) {
	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "remember_fact",
		Description: "Record a verified owner/company fact citing an explicit owner message ID.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a rememberFactArgs) (*mcp.CallToolResult, any, error) {
		res := svc.RememberFact(ctx, caller, a.Slug, a.Type, a.Statement, a.Confidence, domain.ID(a.EvidenceMsgID), a.Supersedes)
		return toMCPResult(maybeCap(svc, caller, "remember_fact", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "routine_propose",
		Description: "Propose a new recurring routine (created disabled until owner approves).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a routineProposeArgs) (*mcp.CallToolResult, any, error) {
		res := svc.RoutinePropose(ctx, caller, a.Slug, a.Title, a.Schedule, a.Body)
		return toMCPResult(maybeCap(svc, caller, "routine_propose", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "agent_propose",
		Description: "Propose instantiating a new agent from a role template (disabled until owner approves).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a agentProposeArgs) (*mcp.CallToolResult, any, error) {
		res := svc.AgentPropose(ctx, caller, a.Slug, a.Template, a.Team, a.Reason)
		return toMCPResult(maybeCap(svc, caller, "agent_propose", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "instruction_propose",
		Description: "Propose a unified diff edit to AGENT.md, TEAM.md, or company/COMPANY.md for owner approval.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a instructionProposeArgs) (*mcp.CallToolResult, any, error) {
		res := svc.InstructionPropose(ctx, caller, a.TargetPath, a.Title, a.Diff)
		return toMCPResult(maybeCap(svc, caller, "instruction_propose", res)), nil, nil
	})
}

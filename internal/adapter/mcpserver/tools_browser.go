package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/app/tools"
)

type handoffArgs struct {
	Reason string `json:"reason" jsonschema:"concise explanation why human browser takeover is needed (e.g. login or 2FA)"`
	Origin string `json:"origin" jsonschema:"target site origin URL (e.g. https://portal.example.com)"`
}

func registerBrowserTools(mcpSrv *mcp.Server, svc *tools.Service, caller tools.Caller) {
	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "handoff",
		Description: "Hand over the browser session to the owner via loopback noVNC for login/2FA and park the run.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a handoffArgs) (*mcp.CallToolResult, any, error) {
		res := svc.Handoff(ctx, caller, a.Reason, a.Origin)
		return toMCPResult(maybeCap(svc, caller, "handoff", res)), nil, nil
	})
}

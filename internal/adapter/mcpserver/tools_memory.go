package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/app/tools"
)

type memorySearchArgs struct {
	Query string `json:"query" jsonschema:"search keywords for episodic memory"`
}

type memorySaveArgs struct {
	Content string `json:"content" jsonschema:"concise episodic observation to save"`
}

func registerMemoryTools(mcpSrv *mcp.Server, svc *tools.Service, caller tools.Caller) {
	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "memory_search",
		Description: "Search your episodic and shared team memories.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args memorySearchArgs) (*mcp.CallToolResult, any, error) {
		res := svc.MemorySearch(ctx, caller, args.Query)
		return toMCPResult(maybeCap(svc, caller, "memory_search", res)), nil, nil
	})

	mcp.AddTool(mcpSrv, &mcp.Tool{
		Name:        "memory_save",
		Description: "Save a durable episodic observation under your agent identity.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args memorySaveArgs) (*mcp.CallToolResult, any, error) {
		res := svc.MemorySave(ctx, caller, args.Content)
		return toMCPResult(maybeCap(svc, caller, "memory_save", res)), nil, nil
	})
}

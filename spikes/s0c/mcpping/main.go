package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pingArgs struct {
	Note string `json:"note,omitempty" jsonschema:"optional note"`
}

type pingRecord struct {
	Timestamp   string `json:"ts"`
	PID         int    `json:"pid"`
	PPID        int    `json:"ppid"`
	MaxRunToken string `json:"maxRunToken"`
	Cwd         string `json:"cwd"`
	Note        string `json:"note,omitempty"`
}

func main() {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "mcpping",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ping",
		Description: "Health and token probe tool for Max S0c spike. Always returns run token and PIDs.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args pingArgs) (*mcp.CallToolResult, any, error) {
		cwd, _ := os.Getwd()
		rec := pingRecord{
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
			PID:         os.Getpid(),
			PPID:        os.Getppid(),
			MaxRunToken: os.Getenv("MAX_RUN_TOKEN"),
			Cwd:         cwd,
			Note:        args.Note,
		}
		if logPath := os.Getenv("MCPPING_LOG"); logPath != "" {
			if b, err := json.Marshal(rec); err == nil {
				f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err == nil {
					_, _ = f.Write(append(b, '\n'))
					_ = f.Close()
				}
			}
		}
		reply := fmt.Sprintf("PONG maxRunToken=%s pid=%d ppid=%d", rec.MaxRunToken, rec.PID, rec.PPID)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: reply},
			},
		}, rec, nil
	})

	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "mcpping error: %v\n", err)
		os.Exit(1)
	}
}

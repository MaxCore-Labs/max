package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeMCPSpec struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
}

func captureMCPServer(sc *ACPScenario, rawParams json.RawMessage) {
	var p struct {
		MCPServers []fakeMCPSpec `json:"mcpServers"`
	}
	if json.Unmarshal(rawParams, &p) == nil && len(p.MCPServers) > 0 {
		cp := p.MCPServers[0]
		sc.capturedMCP = &cp
	}
}

func callMCPPing(spec *fakeMCPSpec) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	env := os.Environ()
	for _, kv := range spec.Env {
		env = append(env, kv.Name+"="+kv.Value)
	}
	cmd.Env = env

	client := mcp.NewClient(&mcp.Implementation{Name: "fakecli-acp", Version: "1.0.0"}, nil)
	sess, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return "", fmt.Errorf("mcp connect: %w", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		return "", fmt.Errorf("call ping: %w", err)
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

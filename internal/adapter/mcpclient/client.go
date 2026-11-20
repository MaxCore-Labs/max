package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/app"
)

type connConfig struct {
	URL     string            `json:"url"`
	Bearer  string            `json:"bearer"`
	Headers map[string]string `json:"headers"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     []string          `json:"env"`
}

// Client implements app.OutboundInvoker for HTTP JSON-RPC and stdio MCP targets.
type Client struct {
	httpClient *http.Client
}

var _ app.OutboundInvoker = (*Client)(nil)

// New constructs an outbound MCP Client.
func New() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 65 * time.Second},
	}
}

// Invoke calls tool on an external MCP connection using decrypted transport credentials.
func (c *Client) Invoke(ctx context.Context, kind string, decryptedConfig []byte, tool string, input json.RawMessage) (string, error) {
	var cfg connConfig
	if len(decryptedConfig) > 0 {
		if err := json.Unmarshal(decryptedConfig, &cfg); err != nil {
			return "", fmt.Errorf("invalid connection config: %w", err)
		}
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "http":
		return c.invokeHTTP(ctx, cfg, tool, input)
	case "stdio":
		return c.invokeStdio(ctx, cfg, tool, input)
	default:
		return "", fmt.Errorf("unsupported connection kind %q", kind)
	}
}

func (c *Client) invokeHTTP(ctx context.Context, cfg connConfig, tool string, input json.RawMessage) (string, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return "", errors.New("http connection requires url")
	}
	var args any = map[string]any{}
	if len(bytes.TrimSpace(input)) > 0 {
		_ = json.Unmarshal(input, &args)
	}
	rpcBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      tool,
			"arguments": args,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(rpcBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Bearer)
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("external MCP HTTP %d", resp.StatusCode)
	}
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil {
		if env.Error != nil {
			return "", errors.New(env.Error.Message)
		}
		if len(env.Result.Content) > 0 {
			var parts []string
			for _, part := range env.Result.Content {
				parts = append(parts, part.Text)
			}
			joined := strings.Join(parts, "\n")
			if env.Result.IsError {
				return "", errors.New(joined)
			}
			return joined, nil
		}
	}
	return string(raw), nil
}

func (c *Client) invokeStdio(ctx context.Context, cfg connConfig, tool string, input json.RawMessage) (string, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return "", errors.New("stdio connection requires command")
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = append([]string(nil), cfg.Env...)
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: "max-gateway", Version: "0.1.0"}, nil)
	sess, err := mc.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return "", fmt.Errorf("connect stdio mcp: %w", err)
	}
	defer func() { _ = sess.Close() }()

	var args map[string]any
	if len(bytes.TrimSpace(input)) > 0 {
		_ = json.Unmarshal(input, &args)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", err
	}
	var out []string
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			out = append(out, tc.Text)
		}
	}
	joined := strings.Join(out, "\n")
	if res.IsError {
		return "", errors.New(joined)
	}
	return joined, nil
}

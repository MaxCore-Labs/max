package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type permOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type permRequestParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Title      string `json:"title"`
	} `json:"toolCall"`
	Options []permOption `json:"options"`
}

type toolObservation struct {
	ToolCallID      string `json:"toolCallId"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	AskedPermission bool   `json:"askedPermission"`
	DecisionOutcome string `json:"decisionOutcome,omitempty"`
}

type acpClient struct {
	tag        string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	writeMu    sync.Mutex
	trace      *traceWriter
	nextID     atomic.Int64
	pendingMu  sync.Mutex
	pending    map[string]chan rpcEnvelope
	allowKinds map[string]bool

	mu         sync.Mutex
	toolByID   map[string]*toolObservation
	toolOrder  []string
	textChunks strings.Builder
	doneCh     chan struct{}
	ioWG       sync.WaitGroup
	closeOnce  sync.Once
}

func startACP(ctx context.Context, agyBin, homeDir, tag, permPolicy string, trace *traceWriter, extraArgs ...string) (*acpClient, error) {
	args := append([]string{"acp", "--include-default-customizations=false"}, extraArgs...)
	cmd := exec.CommandContext(ctx, agyBin, args...)
	cmd.Env = isolatedEnv(homeDir)
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agy acp: %w", err)
	}

	allowKinds := map[string]bool{}
	switch permPolicy {
	case "allow":
		allowKinds = map[string]bool{
			"read": true, "edit": true, "delete": true, "move": true,
			"search": true, "execute": true, "think": true, "fetch": true, "other": true,
		}
	case "readonly":
		allowKinds = map[string]bool{"read": true, "think": true}
	}

	c := &acpClient{
		tag:        tag,
		cmd:        cmd,
		stdin:      stdin,
		trace:      trace,
		pending:    make(map[string]chan rpcEnvelope),
		allowKinds: allowKinds,
		toolByID:   make(map[string]*toolObservation),
		doneCh:     make(chan struct{}),
	}

	c.ioWG.Add(2)
	go func() {
		defer c.ioWG.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			trace.write(tag, "stderr", nil, sc.Text())
		}
	}()
	go func() {
		defer c.ioWG.Done()
		c.readLoop(stdout)
	}()
	return c, nil
}

func (c *acpClient) readLoop(r io.Reader) {
	defer func() {
		close(c.doneCh)
		c.pendingMu.Lock()
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = map[string]chan rpcEnvelope{}
		c.pendingMu.Unlock()
	}()

	br := bufio.NewReaderSize(r, 256*1024)
	for {
		line, err := br.ReadBytes('\n')
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			raw := []byte(trimmed)
			c.trace.write(c.tag, "<-", raw, "")
			var env rpcEnvelope
			if json.Unmarshal(raw, &env) == nil {
				c.dispatch(env)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *acpClient) dispatch(env rpcEnvelope) {
	if env.Method != "" && len(env.ID) > 0 {
		if env.Method == "session/request_permission" {
			c.handlePermission(env)
			return
		}
		_ = c.sendRaw(map[string]any{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   map[string]any{"code": -32601, "message": "Method not supported by headless Max probe: " + env.Method},
		})
		return
	}

	if env.Method == "session/update" {
		var up struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				ToolCallID    string `json:"toolCallId"`
				Name          string `json:"name"`
				Kind          string `json:"kind"`
				Title         string `json:"title"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(env.Params, &up) == nil {
			c.mu.Lock()
			switch up.Update.SessionUpdate {
			case "agent_message_chunk":
				c.textChunks.WriteString(up.Update.Content.Text)
			case "tool_call", "tool_call_update":
				if up.Update.ToolCallID != "" {
					obs, exists := c.toolByID[up.Update.ToolCallID]
					if !exists {
						obs = &toolObservation{ToolCallID: up.Update.ToolCallID}
						c.toolByID[up.Update.ToolCallID] = obs
						c.toolOrder = append(c.toolOrder, up.Update.ToolCallID)
					}
					if up.Update.Name != "" {
						obs.Name = up.Update.Name
					}
					if up.Update.Kind != "" {
						obs.Kind = up.Update.Kind
					}
					if up.Update.Title != "" {
						obs.Title = up.Update.Title
					}
				}
			}
			c.mu.Unlock()
		}
		return
	}

	if len(env.ID) > 0 {
		key := strings.TrimSpace(string(env.ID))
		c.pendingMu.Lock()
		ch, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
		}
		c.pendingMu.Unlock()
		if ok {
			ch <- env
		}
	}
}

func (c *acpClient) handlePermission(env rpcEnvelope) {
	var p permRequestParams
	_ = json.Unmarshal(env.Params, &p)

	c.mu.Lock()
	obs, exists := c.toolByID[p.ToolCall.ToolCallID]
	if !exists {
		obs = &toolObservation{ToolCallID: p.ToolCall.ToolCallID}
		c.toolByID[p.ToolCall.ToolCallID] = obs
		c.toolOrder = append(c.toolOrder, p.ToolCall.ToolCallID)
	}
	if p.ToolCall.Name != "" {
		obs.Name = p.ToolCall.Name
	}
	if p.ToolCall.Kind != "" {
		obs.Kind = p.ToolCall.Kind
	}
	if p.ToolCall.Title != "" {
		obs.Title = p.ToolCall.Title
	}
	obs.AskedPermission = true

	// Default-deny allowlist + exact Kind matching (never allow_always/reject_always, never Options[0]).
	wantKind := "reject_once"
	if obs.Kind != "" && c.allowKinds[obs.Kind] {
		wantKind = "allow_once"
	}
	var chosen *permOption
	for i := range p.Options {
		if p.Options[i].Kind == wantKind {
			chosen = &p.Options[i]
			break
		}
	}

	var outcome map[string]any
	if chosen != nil {
		obs.DecisionOutcome = chosen.Kind + ":" + chosen.OptionID
		outcome = map[string]any{"outcome": "selected", "optionId": chosen.OptionID}
	} else {
		obs.DecisionOutcome = "cancelled"
		outcome = map[string]any{"outcome": "cancelled"}
		c.trace.write(c.tag, "meta", nil, fmt.Sprintf("no exact %s option for toolCallId=%s (kind=%q); cancelled", wantKind, p.ToolCall.ToolCallID, obs.Kind))
	}
	c.mu.Unlock()

	_ = c.sendRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      env.ID,
		"result":  map[string]any{"outcome": outcome},
	})
}

func (c *acpClient) sendRaw(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.trace.write(c.tag, "->", b, "")
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *acpClient) call(ctx context.Context, timeout time.Duration, method string, params any) (json.RawMessage, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id := c.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	ch := make(chan rpcEnvelope, 1)

	c.pendingMu.Lock()
	select {
	case <-c.doneCh:
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("call %s: acp process already exited", method)
	default:
		c.pending[key] = ch
	}
	c.pendingMu.Unlock()

	if err := c.sendRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case <-callCtx.Done():
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("call %s timeout/cancelled: %w", method, callCtx.Err())
	case <-c.doneCh:
		return nil, fmt.Errorf("call %s: EOF before response", method)
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("call %s: channel closed before response", method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc %s error (%d): %s", method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *acpClient) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.textChunks.String()
}

func (c *acpClient) resetText() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.textChunks.Reset()
}

func (c *acpClient) observations() []toolObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]toolObservation, 0, len(c.toolOrder))
	for _, id := range c.toolOrder {
		if obs, ok := c.toolByID[id]; ok {
			out = append(out, *obs)
		}
	}
	return out
}

func (c *acpClient) close() {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		select {
		case <-c.doneCh:
		case <-time.After(3 * time.Second):
			killProcessGroup(c.cmd)
		}
		// Ensure entire process group (including child MCP servers) is terminated,
		// drain both stdout & stderr goroutines completely, and only then call Wait().
		killProcessGroup(c.cmd)
		c.ioWG.Wait()
		_ = c.cmd.Wait()
	})
}

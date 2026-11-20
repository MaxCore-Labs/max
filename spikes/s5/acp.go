package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type usageMetrics struct {
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	CachedReadTokens  int64 `json:"cachedReadTokens"`
	CachedWriteTokens int64 `json:"cachedWriteTokens"`
	ThoughtTokens     int64 `json:"thoughtTokens"`
	TotalTokens       int64 `json:"totalTokens"`
}

type promptResponse struct {
	StopReason string       `json:"stopReason"`
	Usage      usageMetrics `json:"usage"`
}

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
	Kind     string `json:"kind"`
}

// selectPermissionOption grants exact `allow_once` only for `read`/`edit` and denies
// `execute` (and any unknown tool kind) via `reject_once`, never selecting `allow_always`.
func selectPermissionOption(toolKind string, options []permOption) (selectedID string, ok bool) {
	want := "reject_once"
	if toolKind == "read" || toolKind == "edit" {
		want = "allow_once"
	}
	for _, o := range options {
		if o.Kind == want || o.OptionID == want {
			return o.OptionID, true
		}
	}
	return "", false
}

type acpRunner struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	writeMu    sync.Mutex
	nextID     atomic.Int64
	pendingMu  sync.Mutex
	pending    map[string]chan rpcEnvelope
	doneCh     chan struct{}
	stderrDone chan struct{}
	stderrBuf  strings.Builder
	stderrMu   sync.Mutex
	eventsMu   sync.Mutex
	events     []json.RawMessage
}

func isolatedEnv(homeDir string) []string {
	appData := filepath.Join(homeDir, "AppData", "Roaming")
	localAppData := filepath.Join(homeDir, "AppData", "Local")
	xdgConfig := filepath.Join(homeDir, ".config")
	xdgData := filepath.Join(homeDir, ".local", "share")
	xdgState := filepath.Join(homeDir, ".local", "state")
	for _, d := range []string{appData, localAppData, xdgConfig, xdgData, xdgState} {
		_ = os.MkdirAll(d, 0o700)
	}

	keep := []string{
		"PATH", "SYSTEMDRIVE", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
		"TEMP", "TMP", "TZ", "LANG", "LC_ALL",
		"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR",
	}
	env := make([]string, 0, len(keep)+7)
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+xdgConfig,
		"XDG_DATA_HOME="+xdgData,
		"XDG_STATE_HOME="+xdgState,
		"USERPROFILE="+homeDir,
		"APPDATA="+appData,
		"LOCALAPPDATA="+localAppData,
	)
	return env
}

func startACPRunner(ctx context.Context, agyBin, homeDir string) (*acpRunner, error) {
	cmd := exec.CommandContext(ctx, agyBin, "acp", "--include-default-customizations=false")
	cmd.Env = isolatedEnv(homeDir)
	cmd.WaitDelay = 2 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	r := &acpRunner{
		cmd:        cmd,
		stdin:      stdin,
		pending:    make(map[string]chan rpcEnvelope),
		doneCh:     make(chan struct{}),
		stderrDone: make(chan struct{}),
	}

	go func() {
		defer close(r.stderrDone)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			r.stderrMu.Lock()
			r.stderrBuf.WriteString(sc.Text() + "\n")
			r.stderrMu.Unlock()
		}
	}()
	go r.readLoop(stdout)
	return r, nil
}

func (r *acpRunner) readLoop(rd io.Reader) {
	defer close(r.doneCh)
	br := bufio.NewReaderSize(rd, 256*1024)
	for {
		line, err := br.ReadBytes('\n')
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			var env rpcEnvelope
			if json.Unmarshal([]byte(trimmed), &env) == nil {
				r.dispatch(trimmed, env)
			}
		}
		if err != nil {
			r.pendingMu.Lock()
			for _, ch := range r.pending {
				close(ch)
			}
			r.pending = map[string]chan rpcEnvelope{}
			r.pendingMu.Unlock()
			return
		}
	}
}

func isStreamingChunkUpdate(params json.RawMessage) bool {
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	return p.Update.SessionUpdate == "agent_message_chunk" || p.Update.SessionUpdate == "agent_thought_chunk"
}

func (r *acpRunner) dispatch(rawLine string, env rpcEnvelope) {
	if env.Method != "" && len(env.ID) > 0 {
		if env.Method == "session/request_permission" {
			var p struct {
				ToolCall struct {
					Kind string `json:"kind"`
				} `json:"toolCall"`
				Options []permOption `json:"options"`
			}
			_ = json.Unmarshal(env.Params, &p)
			var outcome map[string]any
			if optID, ok := selectPermissionOption(p.ToolCall.Kind, p.Options); ok {
				outcome = map[string]any{"outcome": "selected", "optionId": optID}
			} else {
				outcome = map[string]any{"outcome": "cancelled"}
			}
			_ = r.send(map[string]any{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"result":  map[string]any{"outcome": outcome},
			})
			return
		}
		_ = r.send(map[string]any{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   map[string]any{"code": -32601, "message": "unsupported"},
		})
		return
	}
	if env.Method != "" && len(env.ID) == 0 {
		// Record all non-chunk server notifications so any quota/status alert is preserved in NDJSON.
		if !isStreamingChunkUpdate(env.Params) {
			r.eventsMu.Lock()
			if len(r.events) < 64 {
				r.events = append(r.events, json.RawMessage(rawLine))
			}
			r.eventsMu.Unlock()
		}
		return
	}
	if len(env.ID) > 0 && env.Method == "" {
		key := strings.TrimSpace(string(env.ID))
		r.pendingMu.Lock()
		ch, ok := r.pending[key]
		if ok {
			delete(r.pending, key)
		}
		r.pendingMu.Unlock()
		if ok {
			ch <- env
		}
	}
}

func (r *acpRunner) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_, err = r.stdin.Write(append(b, '\n'))
	return err
}

func (r *acpRunner) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := r.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	ch := make(chan rpcEnvelope, 1)
	r.pendingMu.Lock()
	r.pending[key] = ch
	r.pendingMu.Unlock()

	if err := r.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		r.pendingMu.Lock()
		delete(r.pending, key)
		r.pendingMu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		r.pendingMu.Lock()
		delete(r.pending, key)
		r.pendingMu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("eof on %s (stderr: %s)", method, r.stderrString())
		}
		if resp.Error != nil {
			rawData := string(resp.Error.Data)
			return nil, fmt.Errorf("rpc %s (%d): %s %s", method, resp.Error.Code, resp.Error.Message, rawData)
		}
		return resp.Result, nil
	}
}

func (r *acpRunner) stderrString() string {
	r.stderrMu.Lock()
	defer r.stderrMu.Unlock()
	return strings.TrimSpace(r.stderrBuf.String())
}

func (r *acpRunner) recordedEvents() []json.RawMessage {
	r.eventsMu.Lock()
	defer r.eventsMu.Unlock()
	out := make([]json.RawMessage, len(r.events))
	copy(out, r.events)
	return out
}

func (r *acpRunner) close() {
	_ = r.stdin.Close()
	select {
	case <-r.doneCh:
	case <-time.After(2 * time.Second):
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
	}
	_ = r.cmd.Wait()
	select {
	case <-r.stderrDone:
	case <-time.After(500 * time.Millisecond):
	}
}

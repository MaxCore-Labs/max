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

type acpRunner struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan rpcEnvelope
	doneCh    chan struct{}
	stderrBuf strings.Builder
	stderrMu  sync.Mutex
}

func isolatedEnv(homeDir string) []string {
	appData := filepath.Join(homeDir, "AppData", "Roaming")
	localAppData := filepath.Join(homeDir, "AppData", "Local")
	_ = os.MkdirAll(appData, 0o700)
	_ = os.MkdirAll(localAppData, 0o700)

	keep := []string{
		"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
		"TEMP", "TMP", "TZ", "LANG",
	}
	env := make([]string, 0, len(keep)+4)
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"HOME="+homeDir,
		"USERPROFILE="+homeDir,
		"APPDATA="+appData,
		"LOCALAPPDATA="+localAppData,
	)
	return env
}

func startACPRunner(ctx context.Context, agyBin, homeDir string) (*acpRunner, error) {
	cmd := exec.CommandContext(ctx, agyBin, "acp", "--include-default-customizations=false")
	cmd.Env = isolatedEnv(homeDir)

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
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[string]chan rpcEnvelope),
		doneCh:  make(chan struct{}),
	}

	go func() {
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
				r.dispatch(env)
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

func (r *acpRunner) dispatch(env rpcEnvelope) {
	if env.Method != "" && len(env.ID) > 0 {
		if env.Method == "session/request_permission" {
			var p struct {
				Options []struct {
					OptionID string `json:"optionId"`
					Kind     string `json:"kind"`
				} `json:"options"`
			}
			_ = json.Unmarshal(env.Params, &p)
			optID := "allow"
			for _, o := range p.Options {
				if strings.Contains(o.Kind, "allow") || strings.Contains(o.OptionID, "allow") {
					optID = o.OptionID
					break
				}
			}
			_ = r.send(map[string]any{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"result": map[string]any{
					"outcome": map[string]any{"outcome": "selected", "optionId": optID},
				},
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
		return nil, err
	}
	select {
	case <-ctx.Done():
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

func (r *acpRunner) close() {
	_ = r.stdin.Close()
	select {
	case <-r.doneCh:
	case <-time.After(3 * time.Second):
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
	}
	_ = r.cmd.Wait()
}

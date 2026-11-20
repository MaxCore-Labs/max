package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"max/internal/adapter/engine"
	"max/internal/app"
	"max/internal/domain"
)

func (d *Driver) runACP(ctx context.Context, spec app.RunSpec, perm app.PermissionFunc) (<-chan app.EngineEvent, error) {
	args := []string{"acp", "--include-default-customizations=false"}
	for _, add := range spec.AddDirs {
		if strings.TrimSpace(add) != "" {
			args = append(args, "--add-dir", add)
		}
	}

	stderrBuf := &boundedBuffer{cap: maxStderrCap}
	proc, stdin, linesCh, errCh, err := engine.Start(ctx, engine.ProcSpec{
		Bin:         d.bin,
		Args:        args,
		Env:         d.isolatedEnv(),
		Dir:         spec.Cwd,
		Stderr:      stderrBuf,
		IdleTimeout: d.idleTimeout,
		Grace:       defaultKillGrace,
	})
	if err != nil {
		return nil, err
	}

	out := make(chan app.EngineEvent, 32)
	go func() {
		defer close(out)
		defer func() {
			_ = stdin.Close()
			_ = engine.KillGroup(proc.PGID, defaultKillGrace)
			for range linesCh {
			}
			for range errCh {
			}
		}()

		d.driveACPSession(ctx, spec, perm, stdin, linesCh, errCh, stderrBuf, out)
	}()

	return out, nil
}

func (d *Driver) driveACPSession(
	ctx context.Context,
	spec app.RunSpec,
	perm app.PermissionFunc,
	stdin io.WriteCloser,
	linesCh <-chan []byte,
	errCh <-chan error,
	stderrBuf *boundedBuffer,
	out chan<- app.EngineEvent,
) {
	nextID := 0
	sendReq := func(method string, params any) (int, error) {
		nextID++
		b, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      nextID,
			"method":  method,
			"params":  params,
		})
		if err != nil {
			return 0, err
		}
		_, err = stdin.Write(append(b, '\n'))
		return nextID, err
	}
	sendRaw := func(payload any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(b, '\n'))
		return err
	}

	toolCache := make(map[string]cachedToolCall)
	var nonTextUpdates []string
	promptSent := false

	waitForResponse := func(wantID int) (rpcEnvelope, bool) {
		wantStr := strconv.Itoa(wantID)
		for {
			select {
			case <-ctx.Done():
				return rpcEnvelope{}, false
			case line, ok := <-linesCh:
				if !ok {
					procErr := <-errCh
					errText := ""
					if procErr != nil {
						errText = procErr.Error()
					}
					if hit, resetAt := d.matchesLimit(errText, stderrBuf.String()); hit {
						d.logLimitDiagnostics(spec.RunID, "eof", errText, nonTextUpdates, stderrBuf.String())
						out <- app.EngineEvent{Type: "limit", LimitResetAt: resetAt}
						return rpcEnvelope{}, false
					}
					out <- app.EngineEvent{Type: "error", ErrKind: "eof", ErrMessage: fmt.Sprintf("acp closed early: %v", procErr)}
					return rpcEnvelope{}, false
				}

				var env rpcEnvelope
				if err := json.Unmarshal(line, &env); err != nil {
					continue
				}
				if env.Method == "session/update" {
					if promptSent {
						d.handleACPUpdate(env.Params, toolCache, &nonTextUpdates, out)
					}
					continue
				}
				if env.Method == "session/request_permission" {
					d.handleACPPermission(ctx, spec, perm, env.ID, env.Params, toolCache, sendRaw)
					continue
				}
				if trimQuotes(string(env.ID)) == wantStr {
					return env, true
				}
			}
		}
	}

	// 1. initialize
	initID, err := sendReq("initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "max", "version": "0.1.0"},
	})
	if err != nil {
		out <- app.EngineEvent{Type: "error", ErrKind: "write", ErrMessage: err.Error()}
		return
	}
	if initResp, ok := waitForResponse(initID); !ok || initResp.Error != nil {
		if ok && initResp.Error != nil {
			d.emitRPCFailure(spec.RunID, initResp.Error, nonTextUpdates, stderrBuf.String(), out)
		}
		return
	}

	// 2. session/new or session/load (mcpServers[].env MUST be array of {name, value} objects per S0c).
	mcpEnv := []envVariable{{Name: "MAX_RUN_TOKEN", Value: spec.RunToken}}
	if d.mcpSock != "" {
		mcpEnv = append(mcpEnv, envVariable{Name: "MAX_MCP_SOCK", Value: d.mcpSock})
	}
	mcpServers := []mcpServerSpec{{
		Name:    "max",
		Command: d.maxBin,
		Args:    []string{"mcp"},
		Env:     mcpEnv,
	}}

	var sessID int
	if spec.SessionID == "" {
		newParams := map[string]any{
			"cwd":        spec.Cwd,
			"mcpServers": mcpServers,
		}
		if strings.TrimSpace(spec.SystemPrompt) != "" {
			newParams["_meta"] = map[string]any{
				"antigravity": map[string]any{
					"systemPrompt": map[string]any{"append": spec.SystemPrompt},
				},
			}
		}
		sessID, err = sendReq("session/new", newParams)
	} else {
		sessID, err = sendReq("session/load", map[string]any{
			"sessionId":  spec.SessionID,
			"cwd":        spec.Cwd,
			"mcpServers": mcpServers,
		})
	}
	if err != nil {
		out <- app.EngineEvent{Type: "error", ErrKind: "write", ErrMessage: err.Error()}
		return
	}
	sessResp, ok := waitForResponse(sessID)
	if !ok {
		return
	}
	if sessResp.Error != nil {
		d.emitRPCFailure(spec.RunID, sessResp.Error, nonTextUpdates, stderrBuf.String(), out)
		return
	}
	var sr struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(sessResp.Result, &sr)
	activeSessionID := sr.SessionID
	if activeSessionID == "" {
		activeSessionID = spec.SessionID
	}
	out <- app.EngineEvent{Type: "session", SessionID: activeSessionID}

	// 3. Optional session/set_mode("plan") for suggest autonomy.
	if spec.Autonomy == domain.AutonomySuggest {
		modeID, err := sendReq("session/set_mode", map[string]any{
			"sessionId": activeSessionID,
			"modeId":    "plan",
		})
		if err != nil {
			out <- app.EngineEvent{Type: "error", ErrKind: "write", ErrMessage: err.Error()}
			return
		}
		if modeResp, ok := waitForResponse(modeID); !ok || modeResp.Error != nil {
			if ok && modeResp.Error != nil {
				d.emitRPCFailure(spec.RunID, modeResp.Error, nonTextUpdates, stderrBuf.String(), out)
			}
			return
		}
	}

	// 4. session/prompt (enable session/update processing from this point on).
	promptSent = true
	promptID, err := sendReq("session/prompt", map[string]any{
		"sessionId": activeSessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": spec.Prompt},
		},
	})
	if err != nil {
		out <- app.EngineEvent{Type: "error", ErrKind: "write", ErrMessage: err.Error()}
		return
	}
	promptResp, ok := waitForResponse(promptID)
	if !ok {
		return
	}
	if promptResp.Error != nil {
		d.emitRPCFailure(spec.RunID, promptResp.Error, nonTextUpdates, stderrBuf.String(), out)
		return
	}

	var pr promptResult
	_ = json.Unmarshal(promptResp.Result, &pr)
	if pr.StopReason != "" && pr.StopReason != "end_turn" {
		d.logLimitDiagnostics(spec.RunID, pr.StopReason, string(promptResp.Result), nonTextUpdates, stderrBuf.String())
		if hit, resetAt := d.matchesLimit(pr.StopReason, string(promptResp.Result), stderrBuf.String()); hit {
			out <- app.EngineEvent{Type: "limit", LimitResetAt: resetAt}
			return
		}
	}

	out <- app.EngineEvent{
		Type:         "final",
		SessionID:    activeSessionID,
		InputTokens:  pr.Usage.InputTokens,
		OutputTokens: pr.Usage.OutputTokens,
	}
}

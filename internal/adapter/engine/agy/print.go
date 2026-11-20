package agy

import (
	"context"
	"encoding/json"
	"strings"

	"max/internal/adapter/engine"
	"max/internal/app"
	"max/internal/domain"
)

type printLine struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	ToolUseID string `json:"tool_use_id"`
	ToolName  string `json:"tool_name"`
	Error     string `json:"error"`
	Usage     struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (d *Driver) runPrint(ctx context.Context, spec app.RunSpec) (<-chan app.EngineEvent, error) {
	modeFlag := "accept-edits"
	if spec.Autonomy == domain.AutonomySuggest {
		modeFlag = "plan"
	}
	promptPayload := spec.Prompt
	if spec.SessionID == "" && strings.TrimSpace(spec.SystemPrompt) != "" {
		promptPayload = spec.SystemPrompt + "\n\n" + spec.Prompt
	}

	// --output-format stream-json MUST precede -p per S0c item 4.
	args := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"-p=" + promptPayload,
		"--mode", modeFlag,
	}
	if spec.Model != "" && spec.Model != "default" {
		args = append(args, "--model", spec.Model)
	}
	for _, add := range spec.AddDirs {
		if strings.TrimSpace(add) != "" {
			args = append(args, "--add-dir", add)
		}
	}
	if spec.JSONSchema != "" {
		args = append(args, "--json-schema", spec.JSONSchema)
	}
	if spec.SessionID != "" {
		args = append(args, "--conversation="+spec.SessionID)
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
	_ = stdin.Close()

	out := make(chan app.EngineEvent, 32)
	go func() {
		defer close(out)
		defer func() {
			_ = engine.KillGroup(proc.PGID, defaultKillGrace)
		}()

		var activeSession string
		sawFinal := false
		for raw := range linesCh {
			var pl printLine
			if err := json.Unmarshal(raw, &pl); err != nil {
				continue
			}
			switch pl.Type {
			case "session", "system":
				if pl.SessionID != "" {
					activeSession = pl.SessionID
					out <- app.EngineEvent{Type: "session", SessionID: pl.SessionID}
				}
			case "text_delta", "assistant":
				if pl.Text != "" {
					out <- app.EngineEvent{Type: "text_delta", Text: pl.Text}
				}
			case "tool_call":
				out <- app.EngineEvent{Type: "tool_call", ToolUseID: pl.ToolUseID, ToolName: pl.ToolName}
			case "tool_result":
				out <- app.EngineEvent{Type: "tool_result", ToolUseID: pl.ToolUseID, ToolName: pl.ToolName, ToolOK: pl.Error == ""}
			case "error":
				if hit, resetAt := d.matchesLimit(pl.Error, stderrBuf.String()); hit {
					d.logLimitDiagnostics(spec.RunID, "print_error", pl.Error, nil, stderrBuf.String())
					out <- app.EngineEvent{Type: "limit", LimitResetAt: resetAt}
					sawFinal = true
					continue
				}
				out <- app.EngineEvent{Type: "error", ErrKind: "print", ErrMessage: pl.Error}
			case "result", "final":
				sawFinal = true
				sid := pl.SessionID
				if sid == "" {
					sid = activeSession
				}
				out <- app.EngineEvent{
					Type:         "final",
					SessionID:    sid,
					InputTokens:  pl.Usage.InputTokens,
					OutputTokens: pl.Usage.OutputTokens,
				}
			}
		}

		procErr := <-errCh
		if !sawFinal && procErr != nil {
			if hit, resetAt := d.matchesLimit(procErr.Error(), stderrBuf.String()); hit {
				d.logLimitDiagnostics(spec.RunID, "print_exit", procErr.Error(), nil, stderrBuf.String())
				out <- app.EngineEvent{Type: "limit", LimitResetAt: resetAt}
				return
			}
			out <- app.EngineEvent{Type: "error", ErrKind: "exit", ErrMessage: procErr.Error()}
		}
	}()

	return out, nil
}

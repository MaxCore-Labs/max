package agy_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/engine/agy"
	"max/internal/app"
	"max/internal/domain"
)

func TestLimitEvent(t *testing.T) {
	limitMsgs := []string{
		"RESOURCE_EXHAUSTED: model capacity depleted until 2026-11-20T12:00:00Z",
		"Plan quota exceeded for current window",
		"upstream returned HTTP 429 Too Many Requests",
	}
	for _, msg := range limitMsgs {
		var logBuf bytes.Buffer
		evs, _ := runFakeACPScenario(t, map[string]any{
			"rpc_error_code":    -32001,
			"rpc_error_message": msg,
			"stderr_text":       "raw-stderr-diagnostic",
		}, app.RunSpec{RunID: "r-lim", Prompt: "hi", Autonomy: domain.AutonomyAuto}, nil, &logBuf)

		hasLimit := false
		for _, ev := range evs {
			if ev.Type == "limit" {
				hasLimit = true
			}
		}
		if !hasLimit {
			t.Fatalf("expected limit event for %q, got %+v", msg, evs)
		}
		if !strings.Contains(logBuf.String(), "raw-stderr-diagnostic") {
			t.Fatalf("expected verbatim diagnostic log containing stderr for %q, got:\n%s", msg, logBuf.String())
		}
	}

	// Bare "429" inside normal text or non-limit error must NOT false-positive as a limit event.
	evsNonLimit, _ := runFakeACPScenario(t, map[string]any{
		"session_id":      "sess-429-id",
		"response_chunks": []string{"Found 429 items in invoice #4290."},
		"input_tokens":    429,
		"output_tokens":   42,
	}, app.RunSpec{RunID: "r-ok", Prompt: "count", Autonomy: domain.AutonomyAuto}, nil, nil)

	for _, ev := range evsNonLimit {
		if ev.Type == "limit" {
			t.Fatalf("false-positive limit event on bare '429': %+v", ev)
		}
	}
}

func TestEnvIsolation(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "should-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "should-not-leak")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	t.Setenv("RANDOM_PARENT_VAR", "leak")

	_, rec := runFakeACPScenario(t, map[string]any{
		"session_id": "sess-env",
	}, app.RunSpec{RunID: "r-env", Prompt: "hi", Autonomy: domain.AutonomyAuto}, nil, nil)

	var sawHome bool
	for _, kv := range rec.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "HOME":
			sawHome = true
			if !strings.Contains(v, "agent-home") {
				t.Fatalf("HOME = %q, want isolated agent-home directory", v)
			}
		case "GEMINI_API_KEY", "ANTHROPIC_API_KEY", "DBUS_SESSION_BUS_ADDRESS", "RANDOM_PARENT_VAR":
			t.Fatalf("forbidden parent env %q leaked into agy child process", kv)
		}
	}
	if !sawHome {
		t.Fatal("expected isolated HOME variable in child env")
	}
}

func TestHealthForeignMCP(t *testing.T) {
	bin := buildAgyFakeCLI(t)
	home := t.TempDir()
	mcpCfgPath := filepath.Join(home, ".gemini", "antigravity", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(mcpCfgPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(mcpCfgPath, []byte(`{
		"mcpServers": {
			"max": {"command": "max", "args": ["mcp"]},
			"github_personal": {"command": "npx", "args": ["gh-mcp"]}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write mcp_config.json: %v", err)
	}

	drv := agy.New(bin, home, agy.ModeACP, slog.Default())
	h := drv.Health(context.Background())
	if !h.Installed {
		t.Fatal("expected Installed=true")
	}
	if len(h.ForeignMCP) != 1 || h.ForeignMCP[0] != "github_personal" {
		t.Fatalf("ForeignMCP = %v, want [github_personal]", h.ForeignMCP)
	}
}

func TestCapsByMode(t *testing.T) {
	acpCaps := agy.New("agy", t.TempDir(), agy.ModeACP, slog.Default()).Caps()
	if !acpCaps.PerRunMCP || acpCaps.PreToolGate || acpCaps.ToolAllowlist || !acpCaps.Resume {
		t.Fatalf("unexpected ModeACP Caps: %+v", acpCaps)
	}

	printCaps := agy.New("agy", t.TempDir(), agy.ModePrint, slog.Default()).Caps()
	if printCaps.PerRunMCP || printCaps.PreToolGate || !printCaps.Resume {
		t.Fatalf("unexpected ModePrint Caps: %+v", printCaps)
	}
}

func TestPrintMode(t *testing.T) {
	bin := buildAgyFakeCLI(t)
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "print-stream.ndjson")
	lines := strings.Join([]string{
		`{"type":"session","session_id":"sess-print-1"}`,
		`{"type":"text_delta","text":"Xin "}`,
		`{"type":"text_delta","text":"chào!"}`,
		`{"type":"result","session_id":"sess-print-1","usage":{"input_tokens":1500,"output_tokens":250}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(scriptPath, []byte(lines), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	drv := agy.New(bin, filepath.Join(tmp, "home"), agy.ModePrint, slog.Default()).
		WithExtraEnv("FAKECLI_SCRIPT=" + scriptPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	evCh, err := drv.Run(ctx, app.RunSpec{
		RunID:    "run-print",
		Cwd:      tmp,
		Prompt:   "Báo cáo ngắn",
		Autonomy: domain.AutonomyAuto,
	}, nil)
	if err != nil {
		t.Fatalf("PrintMode Run: %v", err)
	}

	var events []app.EngineEvent
	for ev := range evCh {
		events = append(events, ev)
	}
	if len(events) != 4 {
		t.Fatalf("PrintMode events = %+v, want [session, text_delta, text_delta, final]", events)
	}
	if events[0].Type != "session" || events[0].SessionID != "sess-print-1" {
		t.Fatalf("events[0] = %+v", events[0])
	}
	if events[1].Text+events[2].Text != "Xin chào!" {
		t.Fatalf("text = %q", events[1].Text+events[2].Text)
	}
	if events[3].Type != "final" || events[3].InputTokens != 1500 || events[3].OutputTokens != 250 {
		t.Fatalf("events[3] = %+v", events[3])
	}
}

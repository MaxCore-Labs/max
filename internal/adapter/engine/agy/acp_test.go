package agy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"max/internal/adapter/engine/agy"
	"max/internal/app"
	"max/internal/domain"
)

var (
	agyFakeOnce sync.Once
	agyFakeBin  string
)

func buildAgyFakeCLI(t *testing.T) string {
	t.Helper()
	agyFakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "max-agy-fakecli-*")
		if err != nil {
			return
		}
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		out := filepath.Join(dir, "fakecli"+ext)
		cmd := exec.Command("go", "build", "-o", out, "max/internal/testutil/fakecli")
		if err := cmd.Run(); err == nil {
			agyFakeBin = out
		}
	})
	if agyFakeBin == "" {
		t.Fatal("failed to build fakecli")
	}
	return agyFakeBin
}

type acpRecorded struct {
	Args         []string          `json:"args"`
	Env          []string          `json:"env"`
	Requests     []json.RawMessage `json:"requests"`
	PermResponse json.RawMessage   `json:"perm_response"`
}

func runFakeACPScenario(
	t *testing.T,
	scenario map[string]any,
	spec app.RunSpec,
	perm app.PermissionFunc,
	logBuf *bytes.Buffer,
) ([]app.EngineEvent, acpRecorded) {
	t.Helper()
	bin := buildAgyFakeCLI(t)
	tmp := t.TempDir()
	recPath := filepath.Join(tmp, "recorded.json")
	scPath := filepath.Join(tmp, "scenario.json")
	scenario["record_path"] = recPath
	b, _ := json.Marshal(scenario)
	if err := os.WriteFile(scPath, b, 0o600); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	homeDir := filepath.Join(tmp, "agent-home")
	if logBuf == nil {
		logBuf = &bytes.Buffer{}
	}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	drv := agy.New(bin, homeDir, agy.ModeACP, logger).
		WithExtraEnv("FAKECLI_ACP_SCENARIO="+scPath).
		WithMaxBin("/usr/local/bin/max", "")

	if spec.Cwd == "" {
		spec.Cwd = filepath.Join(tmp, "vault")
		_ = os.MkdirAll(spec.Cwd, 0o755)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	evCh, err := drv.Run(ctx, spec, perm)
	if err != nil {
		t.Fatalf("drv.Run: %v", err)
	}
	var events []app.EngineEvent
	for ev := range evCh {
		events = append(events, ev)
	}

	var rec acpRecorded
	if rb, err := os.ReadFile(recPath); err == nil {
		_ = json.Unmarshal(rb, &rec)
	}
	return events, rec
}

func methodNames(reqs []json.RawMessage) []string {
	var out []string
	for _, r := range reqs {
		var m struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(r, &m) == nil && m.Method != "" {
			out = append(out, m.Method)
		}
	}
	return out
}

func TestACPHappyPath(t *testing.T) {
	events, rec := runFakeACPScenario(t, map[string]any{
		"session_id":      "sess-happy-1",
		"response_chunks": []string{"Xin ", "chào!"},
		"input_tokens":    1500,
		"output_tokens":   250,
	}, app.RunSpec{
		RunID:     "run-1",
		AgentID:   "ag-1",
		AgentSlug: "chief",
		Prompt:    "Báo cáo ngắn",
		RunToken:  "mrt_happy_tok",
		Autonomy:  domain.AutonomyAuto,
	}, nil, nil)

	methods := methodNames(rec.Requests)
	wantMethods := []string{"initialize", "session/new", "session/prompt"}
	if strings.Join(methods, ",") != strings.Join(wantMethods, ",") {
		t.Fatalf("ACP methods = %v, want %v", methods, wantMethods)
	}

	rawNew := string(rec.Requests[1])
	if !strings.Contains(rawNew, `"cwd"`) || !strings.Contains(rawNew, `"name":"MAX_RUN_TOKEN"`) || !strings.Contains(rawNew, `"value":"mrt_happy_tok"`) {
		t.Fatalf("session/new missing cwd or MAX_RUN_TOKEN EnvVariable array:\n%s", rawNew)
	}

	if len(events) != 4 {
		t.Fatalf("events = %+v, want [session, text_delta, text_delta, final]", events)
	}
	if events[0].Type != "session" || events[0].SessionID != "sess-happy-1" {
		t.Fatalf("events[0] = %+v", events[0])
	}
	if events[1].Text+events[2].Text != "Xin chào!" {
		t.Fatalf("text = %q", events[1].Text+events[2].Text)
	}
	if events[3].Type != "final" || events[3].InputTokens != 1500 || events[3].OutputTokens != 250 {
		t.Fatalf("final = %+v", events[3])
	}
}

func TestACPResume(t *testing.T) {
	events, rec := runFakeACPScenario(t, map[string]any{
		"session_id":            "sess-resume-9",
		"replay_chunks_on_load": []string{"OLD_HISTORY_CHUNK_MUST_BE_IGNORED"},
		"response_chunks":       []string{"FRESH_REPLY"},
		"input_tokens":          500,
		"output_tokens":         50,
	}, app.RunSpec{
		RunID:     "run-2",
		SessionID: "sess-resume-9",
		Prompt:    "Tiếp tục",
		RunToken:  "mrt_new_run_tok",
		Autonomy:  domain.AutonomyAuto,
	}, nil, nil)

	methods := methodNames(rec.Requests)
	if strings.Join(methods, ",") != "initialize,session/load,session/prompt" {
		t.Fatalf("ACP resume methods = %v, want [initialize session/load session/prompt]", methods)
	}
	rawLoad := string(rec.Requests[1])
	if !strings.Contains(rawLoad, `"value":"mrt_new_run_tok"`) {
		t.Fatalf("session/load missing refreshed MAX_RUN_TOKEN:\n%s", rawLoad)
	}
	for _, ev := range events {
		if strings.Contains(ev.Text, "OLD_HISTORY_CHUNK") {
			t.Fatalf("driver failed to ignore replayed session/update before session/prompt: %+v", ev)
		}
	}
}

func TestACPSuggestSetsPlanMode(t *testing.T) {
	_, rec := runFakeACPScenario(t, map[string]any{
		"session_id":      "sess-plan-1",
		"response_chunks": []string{"Kế hoạch"},
	}, app.RunSpec{
		RunID:    "run-plan",
		Prompt:   "Đề xuất",
		RunToken: "mrt_plan",
		Autonomy: domain.AutonomySuggest,
	}, nil, nil)

	methods := methodNames(rec.Requests)
	if strings.Join(methods, ",") != "initialize,session/new,session/set_mode,session/prompt" {
		t.Fatalf("suggest mode methods = %v, want [initialize session/new session/set_mode session/prompt]", methods)
	}
	if !strings.Contains(string(rec.Requests[2]), `"modeId":"plan"`) {
		t.Fatalf("session/set_mode missing modeId='plan': %s", string(rec.Requests[2]))
	}
}

func TestACPPermissionRoundTrip(t *testing.T) {
	stdOpts := []map[string]string{
		{"optionId": "opt-allow", "name": "Allow", "kind": "allow_once"},
		{"optionId": "opt-deny", "name": "Deny", "kind": "reject_once"},
	}

	// Case 1: Permission request omits kind -> looked up via toolCallId cache -> Allow selects allow_once.
	var permCalled bool
	_, recAllow := runFakeACPScenario(t, map[string]any{
		"send_tool_call": map[string]any{
			"tool_call_id": "call_777",
			"name":         "run_command",
			"kind":         "execute",
			"title":        "ls -la",
			"raw_input":    map[string]any{"CommandLine": "ls -la"},
		},
		"permission_request": map[string]any{
			"tool_call_id": "call_777",
			// kind intentionally omitted to test toolCallId cache lookup!
			"options": stdOpts,
		},
	}, app.RunSpec{RunID: "r1", Prompt: "go", RunToken: "tok1", Autonomy: domain.AutonomyAuto},
		func(_ context.Context, ask app.ToolAsk) domain.Decision {
			permCalled = true
			if ask.ToolName != "run_command" {
				t.Errorf("ask.ToolName = %q, want run_command from cache", ask.ToolName)
			}
			return domain.Decision{Verdict: domain.Allow}
		}, nil)
	if !permCalled {
		t.Fatal("expected PermissionFunc to be called")
	}
	if !strings.Contains(string(recAllow.PermResponse), `"optionId":"opt-allow"`) {
		t.Fatalf("expected allow_once option selected, got %s", string(recAllow.PermResponse))
	}

	// Case 2: Deny or unknown kind selects reject_once.
	_, recUnknown := runFakeACPScenario(t, map[string]any{
		"permission_request": map[string]any{
			"tool_call_id": "call_unknown_not_cached",
			"kind":         "weird_unknown_kind",
			"options":      stdOpts,
		},
	}, app.RunSpec{RunID: "r2", Prompt: "go", RunToken: "tok2", Autonomy: domain.AutonomyAuto},
		func(_ context.Context, _ app.ToolAsk) domain.Decision {
			return domain.Decision{Verdict: domain.Allow}
		}, nil)
	if !strings.Contains(string(recUnknown.PermResponse), `"optionId":"opt-deny"`) {
		t.Fatalf("expected reject_once for unknown kind, got %s", string(recUnknown.PermResponse))
	}

	// Case 3: No matching option -> returns outcome: "cancelled".
	_, recNoOpt := runFakeACPScenario(t, map[string]any{
		"permission_request": map[string]any{
			"tool_call_id": "call_no_opt",
			"name":         "run_command",
			"kind":         "execute",
			"options": []map[string]string{
				{"optionId": "always", "name": "Always", "kind": "allow_always"},
			},
		},
	}, app.RunSpec{RunID: "r3", Prompt: "go", RunToken: "tok3", Autonomy: domain.AutonomyAuto},
		func(_ context.Context, _ app.ToolAsk) domain.Decision {
			return domain.Decision{Verdict: domain.Deny, Reason: "no shell"}
		}, nil)
	if !strings.Contains(string(recNoOpt.PermResponse), `"outcome":"cancelled"`) {
		t.Fatalf("expected outcome='cancelled' when reject_once option absent, got %s", string(recNoOpt.PermResponse))
	}
}

func TestACPSystemPromptAppended(t *testing.T) {
	_, rec := runFakeACPScenario(t, map[string]any{
		"session_id": "sess-sys",
	}, app.RunSpec{
		RunID:        "run-sys",
		SystemPrompt: "KERNEL_APPEND_MARKER_99",
		Prompt:       "Hi",
		RunToken:     "tok",
		Autonomy:     domain.AutonomyAuto,
	}, nil, nil)

	if !strings.Contains(string(rec.Requests[1]), "KERNEL_APPEND_MARKER_99") {
		t.Fatalf("session/new missing _meta.antigravity.systemPrompt.append:\n%s", string(rec.Requests[1]))
	}
}

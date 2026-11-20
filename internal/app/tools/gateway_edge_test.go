package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/grants"
	"max/internal/domain"
)

func TestTimeoutBecomesAmbiguous(t *testing.T) {
	env := setupGatewayEnv(t, 80*time.Millisecond)
	ctx := context.Background()
	_, _ = env.grants.Create(ctx, grants.NewGrant{
		AgentID:     "ag-sales",
		Connection:  "mailer",
		Tool:        "send_email",
		Constraints: domain.Constraints{Equals: map[string]string{"to": "slow@acme.com"}},
	})
	env.target.delay = 250 * time.Millisecond

	res := env.gw.Call(ctx, env.caller, "mailer", "send_email", json.RawMessage(`{"to":"slow@acme.com","body":"ping"}`), "", false)
	if !res.IsError || !strings.Contains(res.Text, "ambiguous") {
		t.Fatalf("expected ambiguous error on timeout, got: %+v", res)
	}

	// Wait past target's 250ms delay to prove late response never overwrites ambiguous status.
	time.Sleep(220 * time.Millisecond)
	tcs, err := env.st.Read().ListToolCallsByRun(ctx, env.caller.RunID)
	if err != nil || len(tcs) != 1 || tcs[0].Status != "ambiguous" {
		t.Fatalf("tool_calls = %+v (err=%v), want status=ambiguous", tcs, err)
	}

	inbox, _ := env.st.Read().ListInboxItems(ctx, 10)
	foundAsk := false
	for _, it := range inbox {
		if strings.Contains(strings.ToLower(it.Title+" "+it.Body), "đã gửi chưa") {
			foundAsk = true
		}
	}
	if !foundAsk {
		t.Fatalf("expected Inbox item asking 'đã gửi chưa?', got %+v", inbox)
	}
}

func TestResolveAmbiguous(t *testing.T) {
	env := setupGatewayEnv(t, 60*time.Millisecond)
	ctx := context.Background()
	_, _ = env.grants.Create(ctx, grants.NewGrant{
		AgentID:     "ag-sales",
		Connection:  "mailer",
		Tool:        "send_email",
		Constraints: domain.Constraints{OneOf: map[string][]string{"to": {"amb1@acme.com", "amb2@acme.com"}}},
	})
	env.target.delay = 200 * time.Millisecond

	_ = env.gw.Call(ctx, env.caller, "mailer", "send_email", json.RawMessage(`{"to":"amb1@acme.com"}`), "", false)
	_ = env.gw.Call(ctx, env.caller, "mailer", "send_email", json.RawMessage(`{"to":"amb2@acme.com"}`), "", false)

	tcs, _ := env.st.Read().ListToolCallsByRun(ctx, env.caller.RunID)
	if len(tcs) != 2 {
		t.Fatalf("expected 2 ambiguous tool_calls, got %d", len(tcs))
	}

	// Now target is healthy and fast again.
	env.target.mu.Lock()
	env.target.delay = 0
	env.target.mu.Unlock()
	callsBefore := atomic.LoadInt32(&env.target.calls)

	// 1. sent=true -> marks done without calling target again.
	if err := env.gw.ResolveAmbiguous(ctx, tcs[0].RunID, tcs[0].ToolUseID, true); err != nil {
		t.Fatalf("ResolveAmbiguous(sent=true): %v", err)
	}
	if atomic.LoadInt32(&env.target.calls) != callsBefore {
		t.Fatal("sent=true must NOT re-call target")
	}

	// 2. sent=false -> re-executes exact input once and marks done.
	if err := env.gw.ResolveAmbiguous(ctx, tcs[1].RunID, tcs[1].ToolUseID, false); err != nil {
		t.Fatalf("ResolveAmbiguous(sent=false): %v", err)
	}
	if atomic.LoadInt32(&env.target.calls) != callsBefore+1 {
		t.Fatalf("sent=false calls = %d, want %d", env.target.calls, callsBefore+1)
	}
	after, _ := env.st.Read().ListToolCallsByRun(ctx, env.caller.RunID)
	for _, tc := range after {
		if tc.Status != "done" {
			t.Fatalf("expected resolved status=done, got %+v", tc)
		}
	}
}

func TestCancelledTaskBlocksExecution(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	ctx := context.Background()
	_ = env.gw.Call(ctx, env.caller, "mailer", "send_email", json.RawMessage(`{"to":"cancel@acme.com"}`), "", false)

	aps, _ := env.st.Read().ListApprovalsPage(ctx, "pending", 10, nil, "")
	_ = env.st.WithTx(ctx, func(tx app.Tx) error {
		return tx.UpdateTaskStatus(ctx, "tk-gw", domain.TaskCancelled, nil, 2000)
	})

	if err := env.ap.Decide(ctx, aps[0].ID, true, "dec-cancelled"); err != nil {
		t.Fatalf("Decide after cancel: %v", err)
	}
	if atomic.LoadInt32(&env.target.calls) != 0 {
		t.Fatalf("cancelled task must block outbound execution, got %d calls", env.target.calls)
	}
}

func TestSecretsNeverReturned(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	ctx := context.Background()
	env.target.resp = "sent using sk-gateway-topsecret-value-999 and Bearer tok_secret_abc123"
	res := env.gw.Call(ctx, env.caller, "mailer", "list_inbox", json.RawMessage(`{}`), "", false)
	if strings.Contains(res.Text, "sk-gateway-topsecret-value-999") || strings.Contains(res.Text, "tok_secret_abc123") {
		t.Fatalf("secret leaked in gateway Result: %q", res.Text)
	}
	evs, _ := env.st.Read().ListRunEvents(ctx, env.caller.RunID)
	for _, ev := range evs {
		if strings.Contains(ev.DataJSON, "sk-gateway-topsecret-value-999") {
			t.Fatalf("secret leaked in run_events: %+v", ev)
		}
	}
}

func TestNoGatewayForSuggest(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	sug := env.caller
	sug.Policy.Autonomy = domain.AutonomySuggest
	res := env.gw.Call(context.Background(), sug, "mailer", "send_email", json.RawMessage(`{"to":"a@b.com"}`), "", false)
	if !res.IsError || atomic.LoadInt32(&env.target.calls) != 0 {
		t.Fatalf("suggest agent must be denied on class E gateway call: res=%+v calls=%d", res, env.target.calls)
	}
}

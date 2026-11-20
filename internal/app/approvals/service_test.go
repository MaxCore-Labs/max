package approvals_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/approvals"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

func setupApprovalsFixture(
	t *testing.T,
	execFn approvals.Executor,
) (st app.Store, clk *fixedClock, caller tools.Caller, svc *approvals.Service) {
	t.Helper()
	ctx := context.Background()
	st = testdb.New(t)
	clk = &fixedClock{now: time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)}
	actorSvc := actor.NewService(st, clk, app.UUIDv7Gen{})

	agentID := "agent-ops"
	convID := "conv-ops"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: agentID, Slug: "ops", Name: "Ops", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		return tx.InsertConversation(ctx, app.ConversationRecord{
			ID: convID, AgentID: agentID, Sessions: `{}`,
		})
	}); err != nil {
		t.Fatalf("seed approvals fixture: %v", err)
	}

	caller = tools.Caller{
		RunID:          "run-ap-1",
		AgentID:        agentID,
		ConversationID: convID,
		Policy: domain.AgentPolicy{
			Scope:    domain.AgentScope{Slug: "ops", TeamSlug: "core"},
			Autonomy: domain.AutonomyAuto,
		},
		Origin: domain.OriginOwner,
	}
	svc = approvals.NewService(st, actorSvc, execFn, clk, app.UUIDv7Gen{})
	return st, clk, caller, svc
}

func TestAskCreatesPendingAndParks(t *testing.T) {
	ctx := context.Background()
	st, clk, caller, svc := setupApprovalsFixture(t, nil)

	id, already, err := svc.Ask(ctx, approvals.Request{
		Caller:   caller,
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"a@b.com","body":"hello"}`),
		Reason:   "external action requires approval",
		ByMax:    true,
	})
	if err != nil || already || id == "" {
		t.Fatalf("Ask = (%q, %v, %v), want new pending id", id, already, err)
	}

	ap, ok, err := st.Read().GetApproval(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetApproval: ok=%v err=%v", ok, err)
	}
	if ap.Status != "pending" {
		t.Fatalf("Status = %q, want pending", ap.Status)
	}
	wantExp := clk.now.Add(24 * time.Hour).UnixMilli()
	if ap.ExpiresAt != wantExp {
		t.Fatalf("ExpiresAt = %d, want %d", ap.ExpiresAt, wantExp)
	}

	conv, _, _ := st.Read().GetConversation(ctx, caller.ConversationID)
	if conv.WaitingFor == nil || !strings.Contains(*conv.WaitingFor, `"approval"`) {
		t.Fatalf("waiting_for = %v, want kind='approval'", conv.WaitingFor)
	}

	outbox, err := st.Read().ListOutboxPending(ctx, 10)
	if err != nil || len(outbox) == 0 || outbox[0].Topic != "inbox" {
		t.Fatalf("outbox = %+v (err=%v), want topic='inbox'", outbox, err)
	}
}

func TestAskSameInputTwice(t *testing.T) {
	ctx := context.Background()
	_, _, caller, svc := setupApprovalsFixture(t, nil)

	req := approvals.Request{
		Caller:   caller,
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"a@b.com","body":"same"}`),
		Reason:   "external",
		ByMax:    true,
	}
	id1, already1, err := svc.Ask(ctx, req)
	if err != nil || already1 {
		t.Fatalf("first Ask = (%q, %v, %v)", id1, already1, err)
	}
	id2, already2, err := svc.Ask(ctx, req)
	if err != nil || !already2 || id2 != id1 {
		t.Fatalf("second Ask = (%q, %v, %v), want (%q, true, nil)", id2, already2, err, id1)
	}
}

func TestDecideAllowByMaxExecutesOnce(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	var gotInput string

	st, _, caller, svc := setupApprovalsFixture(t, func(_ context.Context, _ tools.Caller, tool string, input json.RawMessage) tools.Result {
		calls.Add(1)
		gotInput = string(input)
		return tools.Result{Text: "sent message_id=msg-99 via " + tool}
	})

	rawIn := `{"to":"client@example.com","subject":"Invoice"}`
	id, _, err := svc.Ask(ctx, approvals.Request{
		Caller:   caller,
		ToolName: "gmail.send",
		Input:    json.RawMessage(rawIn),
		Reason:   "external",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if err := svc.Decide(ctx, id, true, "idem-dec-1"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Executor calls = %d, want 1", calls.Load())
	}
	if gotInput != rawIn {
		t.Fatalf("Executor input = %q, want verbatim %q", gotInput, rawIn)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, caller.AgentID)
	if err != nil || len(queued) != 1 || queued[0].Kind != domain.WakeApprovalResult {
		t.Fatalf("queued wakeups = %+v (err=%v), want 1 approval_result", queued, err)
	}
	if !strings.Contains(queued[0].Payload, "msg-99") {
		t.Fatalf("wakeup payload missing execution result: %s", queued[0].Payload)
	}
}

func TestDecideIdempotent(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	_, _, caller, svc := setupApprovalsFixture(t, func(_ context.Context, _ tools.Caller, _ string, _ json.RawMessage) tools.Result {
		calls.Add(1)
		return tools.Result{Text: "ok"}
	})

	id, _, err := svc.Ask(ctx, approvals.Request{
		Caller:   caller,
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"x@y.com"}`),
		Reason:   "external",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if err := svc.Decide(ctx, id, true, "same-key"); err != nil {
		t.Fatalf("first Decide: %v", err)
	}
	if err := svc.Decide(ctx, id, true, "same-key"); err != nil {
		t.Fatalf("second Decide with same key failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Executor calls = %d, want 1", calls.Load())
	}

	errDiff := svc.Decide(ctx, id, false, "different-key")
	if errDiff == nil || !strings.Contains(strings.ToLower(errDiff.Error()), "đã quyết định") {
		t.Fatalf("expected 'đã quyết định' error on conflicting decisionKey, got %v", errDiff)
	}
}

func TestDecideDeny(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	st, _, caller, svc := setupApprovalsFixture(t, func(_ context.Context, _ tools.Caller, _ string, _ json.RawMessage) tools.Result {
		calls.Add(1)
		return tools.Result{Text: "should not run"}
	})

	id, _, err := svc.Ask(ctx, approvals.Request{
		Caller:   caller,
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"deny@y.com"}`),
		Reason:   "requires owner confirmation",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if err := svc.Decide(ctx, id, false, "deny-key"); err != nil {
		t.Fatalf("Decide deny: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("Executor called %d times on deny, want 0", calls.Load())
	}

	queued, _ := st.Read().ListQueuedWakeups(ctx, caller.AgentID)
	if len(queued) != 1 || queued[0].Kind != domain.WakeApprovalResult || !strings.Contains(queued[0].Payload, "denied") {
		t.Fatalf("unexpected deny wakeup: %+v", queued)
	}
}

func TestConsumeOnce(t *testing.T) {
	ctx := context.Background()
	st, _, caller, svc := setupApprovalsFixture(t, nil)

	raw := json.RawMessage(`{"cmd":"git status"}`)
	hash := approvals.HashInput(raw)
	id, _, err := svc.Ask(ctx, approvals.Request{
		Caller:   caller,
		ToolName: "Bash",
		Input:    raw,
		Reason:   "shell command",
		ByMax:    false,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if err := svc.Decide(ctx, id, true, "allow-bash"); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	// Wrong hash returns false without consuming.
	var wrongOK, firstOK, secondOK bool
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		var cerr error
		wrongOK, cerr = svc.ConsumeOnce(ctx, tx, caller.AgentID, "Bash", "wrong-hash")
		return cerr
	}); err != nil || wrongOK {
		t.Fatalf("ConsumeOnce wrong hash = (%v, %v), want (false, nil)", wrongOK, err)
	}

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		var cerr error
		firstOK, cerr = svc.ConsumeOnce(ctx, tx, caller.AgentID, "Bash", hash)
		return cerr
	}); err != nil || !firstOK {
		t.Fatalf("first ConsumeOnce = (%v, %v), want (true, nil)", firstOK, err)
	}

	ap, _, _ := st.Read().GetApproval(ctx, id)
	if ap.Status != "consumed" {
		t.Fatalf("Status = %q, want consumed", ap.Status)
	}

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		var cerr error
		secondOK, cerr = svc.ConsumeOnce(ctx, tx, caller.AgentID, "Bash", hash)
		return cerr
	}); err != nil || secondOK {
		t.Fatalf("second ConsumeOnce = (%v, %v), want (false, nil)", secondOK, err)
	}
}

func TestExpire(t *testing.T) {
	ctx := context.Background()
	st, clk, caller, svc := setupApprovalsFixture(t, nil)

	id, _, err := svc.Ask(ctx, approvals.Request{
		Caller:   caller,
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"late@example.com"}`),
		Reason:   "external",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	n, err := svc.ExpireDue(ctx, clk.now.Add(25*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("ExpireDue = (%d, %v), want (1, nil)", n, err)
	}

	ap, _, _ := st.Read().GetApproval(ctx, id)
	if ap.Status != "expired" {
		t.Fatalf("Status = %q, want expired", ap.Status)
	}
	queued, _ := st.Read().ListQueuedWakeups(ctx, caller.AgentID)
	if len(queued) != 1 || !strings.Contains(queued[0].Payload, "không ai duyệt") {
		t.Fatalf("expected expired wakeup mentioning 'không ai duyệt', got %+v", queued)
	}
}

func TestPreviewRedacted(t *testing.T) {
	ctx := context.Background()
	st, _, caller, svc := setupApprovalsFixture(t, nil)

	longInput := `{"auth":"Bearer abcSecret999","pad":"` + strings.Repeat("x", 900) + `"}`
	id, _, err := svc.Ask(ctx, approvals.Request{
		Caller:   caller,
		ToolName: "webhook.post",
		Input:    json.RawMessage(longInput),
		Reason:   "external",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	ap, _, _ := st.Read().GetApproval(ctx, id)
	if strings.Contains(ap.Preview, "abcSecret999") {
		t.Fatalf("preview leaked Bearer token: %q", ap.Preview)
	}
	if len(ap.Preview) > 600 {
		t.Fatalf("len(preview) = %d, want <= 600", len(ap.Preview))
	}
}

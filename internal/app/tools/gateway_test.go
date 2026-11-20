package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/approvals"
	"max/internal/app/connections"
	"max/internal/app/grants"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type fakeOutboundTarget struct {
	mu      sync.Mutex
	calls   int32
	delay   time.Duration
	blockCh chan struct{}
	inputs  []string
	resp    string
}

func (f *fakeOutboundTarget) Invoke(ctx context.Context, _ string, _ []byte, tool string, input json.RawMessage) (string, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.inputs = append(f.inputs, tool+":"+string(input))
	delay := f.delay
	blockCh := f.blockCh
	resp := f.resp
	f.mu.Unlock()

	if blockCh != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-blockCh:
		}
	}
	if delay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
	if resp == "" {
		resp = `{"ok":true}`
	}
	return resp, nil
}

type gwEnv struct {
	st      app.Store
	target  *fakeOutboundTarget
	conns   *connections.Service
	grants  *grants.Service
	ap      *approvals.Service
	gw      *tools.Gateway
	caller  tools.Caller
}

func setupGatewayEnv(t *testing.T, timeout time.Duration) gwEnv {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	dataDir := t.TempDir()

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "sales", Name: "Sales", MaxRunning: 2, Status: "active"})
		_ = tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-sales", Slug: "sales-01", Name: "Sales", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		})
		tkID := "tk-gw"
		_ = tx.InsertTask(ctx, app.TaskRecord{
			ID: tkID, RootID: tkID, RootOrigin: domain.OriginOwner,
			Title: "Outreach", Description: "Send follow-up", Status: "in_progress",
			AssigneeAgentID: ptrStr("ag-sales"), CreatedAt: 1000, UpdatedAt: 1000,
		})
		_, err := tx.EnsureTaskConversation(ctx, "ag-sales", tkID, "conv-gw")
		return err
	})

	connSvc, err := connections.NewService(st, dataDir, app.SystemClock{})
	if err != nil {
		t.Fatalf("connections.NewService: %v", err)
	}
	_ = connSvc.Upsert(ctx, connections.UpsertInput{
		Name:   "mailer",
		Kind:   "mock",
		Config: []byte(`{"api_secret":"sk-gateway-topsecret-value-999"}`),
		Tools: []connections.ConnTool{
			{Name: "list_inbox", Write: false},
			{Name: "send_email", Write: true, TargetField: "to"},
		},
		Enabled: true,
	})

	target := &fakeOutboundTarget{resp: "message queued id=101"}
	grantSvc := grants.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	actorSvc := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})

	var gw *tools.Gateway
	apSvc := approvals.NewService(st, actorSvc, func(ctx context.Context, c tools.Caller, toolName string, input json.RawMessage) tools.Result {
		return gw.ExecuteApproved(ctx, c, toolName, input)
	}, app.SystemClock{}, app.UUIDv7Gen{})

	gw = tools.NewGateway(tools.GatewayDeps{
		Store:       st,
		Connections: connSvc,
		Grants:      grantSvc,
		Approvals:   apSvc,
		Actors:      actorSvc,
		Invoker:     target,
		Timeout:     timeout,
		Clock:       app.SystemClock{},
		IDs:         app.UUIDv7Gen{},
	})

	tkID := "tk-gw"
	return gwEnv{
		st:     st,
		target: target,
		conns:  connSvc,
		grants: grantSvc,
		ap:     apSvc,
		gw:     gw,
		caller: tools.Caller{
			RunID:          "run-gw-1",
			AgentID:        "ag-sales",
			ConversationID: "conv-gw",
			TaskID:         &tkID,
			Policy:         domain.AgentPolicy{Scope: domain.AgentScope{Slug: "sales-01"}, Autonomy: domain.AutonomyAuto},
			Origin:         domain.OriginOwner,
		},
	}
}

func ptrStr(s string) *string { return &s }

func TestReadToolNoApproval(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	res := env.gw.Call(context.Background(), env.caller, "mailer", "list_inbox", json.RawMessage(`{"limit":5}`), "", false)
	if res.IsError {
		t.Fatalf("read tool error: %s", res.Text)
	}
	if atomic.LoadInt32(&env.target.calls) != 1 {
		t.Fatalf("target calls = %d, want 1", env.target.calls)
	}
}

func TestWriteWithoutGrantAsks(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	res := env.gw.Call(context.Background(), env.caller, "mailer", "send_email", json.RawMessage(`{"to":"a@b.com","body":"hi"}`), "", false)
	if atomic.LoadInt32(&env.target.calls) != 0 {
		t.Fatalf("write without grant must NOT call target, got %d calls", env.target.calls)
	}
	if !strings.Contains(res.Text, "Cần chủ nhân duyệt") {
		t.Fatalf("expected approval prompt, got: %s", res.Text)
	}
	aps, _ := env.st.Read().ListApprovalsPage(context.Background(), "pending", 10, nil, "")
	if len(aps) != 1 || !aps[0].ByMax {
		t.Fatalf("pending approvals = %+v, want 1 with ByMax=true", aps)
	}
}

func TestApprovedExecutesExactInputOnce(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	ctx := context.Background()
	input := json.RawMessage(`{"to":"ceo@acme.com","body":"Exact approved payload"}`)
	_ = env.gw.Call(ctx, env.caller, "mailer", "send_email", input, "", false)

	aps, _ := env.st.Read().ListApprovalsPage(ctx, "pending", 10, nil, "")
	if len(aps) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(aps))
	}
	if err := env.ap.Decide(ctx, aps[0].ID, true, "dec-key-1"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	// Clicking decide a second time with same decisionKey is idempotent.
	if err := env.ap.Decide(ctx, aps[0].ID, true, "dec-key-1"); err != nil {
		t.Fatalf("idempotent Decide: %v", err)
	}
	if atomic.LoadInt32(&env.target.calls) != 1 {
		t.Fatalf("target calls = %d, want 1", env.target.calls)
	}
}

func TestKeyNormalization(t *testing.T) {
	k1 := tools.IdempotencyKey("conv-1", "mailer.send_email", json.RawMessage(`{ "b": 2, "a": 1 }`), "")
	k2 := tools.IdempotencyKey("conv-1", "mailer.send_email", json.RawMessage(`{"a":1,"b":2}`), "")
	k3 := tools.IdempotencyKey("conv-1", "mailer.send_email", json.RawMessage(`{"a":1,"b":2}`), "retry-2")
	if k1 != k2 {
		t.Fatalf("expected normalized JSON keys to match: %s != %s", k1, k2)
	}
	if k1 == k3 {
		t.Fatalf("expected different repeatToken to produce distinct key: %s", k1)
	}
}

func TestSameKeyDoneReturnsCached(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	ctx := context.Background()
	_, _ = env.grants.Create(ctx, grants.NewGrant{
		AgentID:     "ag-sales",
		Connection:  "mailer",
		Tool:        "send_email",
		Constraints: domain.Constraints{Equals: map[string]string{"to": "vip@acme.com"}},
	})

	in := json.RawMessage(`{"to":"vip@acme.com","body":"Hello"}`)
	r1 := env.gw.Call(ctx, env.caller, "mailer", "send_email", in, "", false)
	r2 := env.gw.Call(ctx, env.caller, "mailer", "send_email", in, "", false)
	if r1.IsError || r2.IsError || r1.Text != r2.Text {
		t.Fatalf("r1=%+v r2=%+v", r1, r2)
	}
	if atomic.LoadInt32(&env.target.calls) != 1 {
		t.Fatalf("target calls = %d, want 1 (cached on second call)", env.target.calls)
	}
}

func TestSameKeyStartedReturnsInProgress(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	ctx := context.Background()
	_, _ = env.grants.Create(ctx, grants.NewGrant{
		AgentID:     "ag-sales",
		Connection:  "mailer",
		Tool:        "send_email",
		Constraints: domain.Constraints{Equals: map[string]string{"to": "vip@acme.com"}},
	})
	env.target.blockCh = make(chan struct{})

	in := json.RawMessage(`{"to":"vip@acme.com","body":"In flight"}`)
	done1 := make(chan tools.Result, 1)
	go func() {
		done1 <- env.gw.Call(ctx, env.caller, "mailer", "send_email", in, "", false)
	}()
	time.Sleep(40 * time.Millisecond)

	r2 := env.gw.Call(ctx, env.caller, "mailer", "send_email", in, "", false)
	if !strings.Contains(r2.Text, "đang thực hiện") {
		t.Fatalf("expected 'đang thực hiện' while started, got: %s", r2.Text)
	}
	close(env.target.blockCh)
	<-done1
	if atomic.LoadInt32(&env.target.calls) != 1 {
		t.Fatalf("target calls = %d, want 1", env.target.calls)
	}
}

func TestSameTargetDifferentBodyWarns(t *testing.T) {
	env := setupGatewayEnv(t, 5*time.Second)
	ctx := context.Background()
	_, _ = env.grants.Create(ctx, grants.NewGrant{
		AgentID:     "ag-sales",
		Connection:  "mailer",
		Tool:        "send_email",
		Constraints: domain.Constraints{Equals: map[string]string{"to": "bob@acme.com"}},
	})

	_ = env.gw.Call(ctx, env.caller, "mailer", "send_email", json.RawMessage(`{"to":"bob@acme.com","body":"First"}`), "", false)
	warn := env.gw.Call(ctx, env.caller, "mailer", "send_email", json.RawMessage(`{"to":"bob@acme.com","body":"Reworded"}`), "", false)
	if !warn.IsError || !strings.Contains(warn.Text, "confirm_repeat") {
		t.Fatalf("expected repeat target warning, got: %+v", warn)
	}
	if atomic.LoadInt32(&env.target.calls) != 1 {
		t.Fatalf("target calls = %d, want 1 before confirm_repeat", env.target.calls)
	}

	ok := env.gw.Call(ctx, env.caller, "mailer", "send_email", json.RawMessage(`{"to":"bob@acme.com","body":"Reworded"}`), "", true)
	if ok.IsError || atomic.LoadInt32(&env.target.calls) != 2 {
		t.Fatalf("with confirmRepeat=true: res=%+v calls=%d", ok, env.target.calls)
	}
}

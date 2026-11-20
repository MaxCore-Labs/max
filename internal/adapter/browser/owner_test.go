package browser_test

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/browser"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	runapp "max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

func TestOwnerProfileOnlyLevel1(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	v := vaultgit.New(t.TempDir())
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	syncSvc := agentsync.NewService(st, v, app.SystemClock{}, app.UUIDv7Gen{})

	// Level 3 engine (agy) with browser: owner and allow_ungated_engine: false -> refused!
	docL3 := "---\nname: Owner Browser L3\nteam: core\nengines: [agy]\nbrowser: owner\n---\nInstructions.\n"
	_ = v.Write(ctx, "agents/owner-l3/AGENT.md", []byte(docL3))
	_, probs, err := syncSvc.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	foundRefusal := false
	for _, p := range probs {
		if p.Slug == "owner-l3" {
			foundRefusal = true
		}
	}
	if !foundRefusal {
		t.Fatal("expected Level-3 engine with browser: owner to be refused without allow_ungated_engine")
	}

	// Level 1 engine (claude) with browser: owner -> allowed!
	docL1 := "---\nname: Owner Browser L1\nteam: core\nengines: [claude]\nbrowser: owner\n---\nInstructions.\n"
	_ = v.Write(ctx, "agents/owner-l1/AGENT.md", []byte(docL1))
	_, probs2, _ := syncSvc.SyncAll(ctx)
	for _, p := range probs2 {
		if p.Slug == "owner-l1" {
			t.Fatalf("expected Level-1 engine to allow browser: owner, got problem: %+v", p)
		}
	}

	// Also verify browser.ValidateOwnerEngine helper directly.
	if err := browser.ValidateOwnerEngine(2, false); err == nil {
		t.Fatal("expected ValidateOwnerEngine(level=2, unlocked=false) to fail")
	}
	if err := browser.ValidateOwnerEngine(1, false); err != nil {
		t.Fatalf("ValidateOwnerEngine(level=1) failed: %v", err)
	}
	if err := browser.ValidateOwnerEngine(3, true); err != nil {
		t.Fatalf("ValidateOwnerEngine(level=3, unlocked=true) failed: %v", err)
	}
}

func TestTakeoverBindsLoopback(t *testing.T) {
	tk := browser.NewTakeover(browser.TakeoverConfig{})
	rawURL, err := tk.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = tk.Stop(context.Background()) }()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse takeover URL %q: %v", rawURL, err)
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", u.Host, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("takeover listener bound to %q, MUST bind strictly to 127.0.0.1", host)
	}
}

func TestTakeoverAutoStops(t *testing.T) {
	clk := newManualClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	tk := browser.NewTakeover(browser.TakeoverConfig{
		Clock:       clk,
		IdleTimeout: 15 * time.Minute,
	})
	if _, err := tk.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !tk.Running() {
		t.Fatal("expected Takeover running right after Start")
	}

	clk.Advance(14 * time.Minute)
	tk.CheckIdle(context.Background())
	if !tk.Running() {
		t.Fatal("expected Takeover still running at 14 minutes")
	}

	clk.Advance(2 * time.Minute)
	tk.CheckIdle(context.Background())
	if tk.Running() {
		t.Fatal("expected Takeover to auto-stop after 15 minutes with no client connections")
	}
}

func TestHandoffParksRun(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	actors := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"})
		_ = tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "ops-01", Name: "Ops", TeamID: "t1",
			EnginesJSON: `["claude"]`, EngineMask: 1, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserOwner, ConfigRev: "r1", Status: domain.AgentActive,
		})
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{
			AgentID: "a1", Kind: domain.WakeMessage, Priority: domain.PrioInteractive, Payload: json.RawMessage(`{"x":1}`),
		})
		return err
	})
	claimed, _ := actors.ClaimBatch(ctx, actor.Capacity{
		Free:  map[domain.EngineName]int{domain.EngineClaude: 1},
		Admit: func(domain.EngineName, domain.Priority) domain.Admission { return domain.AdmitNow },
	}, time.Now())

	tk := browser.NewTakeover(browser.TakeoverConfig{})
	defer func() { _ = tk.Stop(ctx) }()
	ownerGuard := browser.NewOwnerGuard(st, tk, app.SystemClock{}, app.UUIDv7Gen{})

	msg, err := ownerGuard.Handoff(ctx, "a1", claimed[0].RunID, claimed[0].ConversationID, "Cần đăng nhập 2FA", "https://portal.example.com")
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if !strings.Contains(msg, "127.0.0.1") {
		t.Fatalf("expected takeover loopback link in handoff message, got: %s", msg)
	}

	// Finalizing a succeeded turn when waiting_for.kind='owner' parks the run!
	eng := fakeengine.New(domain.EngineClaude,
		app.EngineEvent{Type: "text_delta", Text: "Đã chuyển quyền điều khiển trình duyệt cho chủ nhân."},
		app.EngineEvent{Type: "final", InputTokens: 10, OutputTokens: 10},
	)
	runSvc := runapp.NewService(runapp.Deps{
		Store:   st,
		Engines: map[domain.EngineName]app.Engine{domain.EngineClaude: eng},
	})
	out, err := runSvc.Execute(ctx, claimed[0])
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	runRec, _, _ := st.Read().GetRun(ctx, claimed[0].RunID)
	if runRec.Status != domain.RunParked {
		t.Fatalf("run status = %v (out=%+v), want parked", runRec.Status, out)
	}
	conv, _, _ := st.Read().GetConversation(ctx, claimed[0].ConversationID)
	if conv.WaitingFor == nil || !strings.Contains(*conv.WaitingFor, `"kind":"owner"`) {
		t.Fatalf("conversation.waiting_for = %v, want kind='owner'", conv.WaitingFor)
	}
	inbox, _ := st.Read().ListInboxItems(ctx, 10)
	if len(inbox) == 0 || !strings.Contains(inbox[0].Body, "127.0.0.1") {
		t.Fatalf("expected Inbox item with takeover link, got %+v", inbox)
	}
}

func TestNoPasswordTyping(t *testing.T) {
	dec := browser.CheckOwnerAction("browser_type", json.RawMessage(`{"target":"input[type=password]","text":"hunter2"}`))
	if dec.Verdict != domain.Deny || !strings.Contains(strings.ToLower(dec.Reason), "handoff") {
		t.Fatalf("password field typing = %+v, want Deny mentioning handoff", dec)
	}

	dec2 := browser.CheckOwnerAction("browser_fill_form", json.RawMessage(`{"fields":[{"name":"password","type":"password","value":"secret"}]}`))
	if dec2.Verdict != domain.Deny || !strings.Contains(strings.ToLower(dec2.Reason), "handoff") {
		t.Fatalf("password form fill = %+v, want Deny mentioning handoff", dec2)
	}
}

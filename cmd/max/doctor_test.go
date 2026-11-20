package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/limits"
	"max/internal/domain"
	"max/internal/platform"
)

func TestDoctorReports(t *testing.T) {
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	cfg := platform.Config{DataDir: dataDir, VaultDir: vaultDir, Listen: "127.0.0.1:8787", PublicURL: "http://127.0.0.1:8787"}

	var outBad, errBad bytes.Buffer
	badEng := &scriptedChatEngine{vaultDir: vaultDir, loggedIn: true, foreignMCP: []string{"rogue-mcp-server"}}
	codeBad := runDoctorWithEngines(context.Background(), cfg, map[domain.EngineName]app.Engine{domain.EngineAgy: badEng}, &outBad, &errBad)
	if codeBad != 1 {
		t.Fatalf("doctor with ForeignMCP exit = %d, want 1", codeBad)
	}
	combined := outBad.String() + errBad.String()
	if !strings.Contains(combined, "rogue-mcp-server") {
		t.Fatalf("doctor output missing foreign MCP server name: %s", combined)
	}

	var outOK, errOK bytes.Buffer
	goodEng := &scriptedChatEngine{vaultDir: vaultDir, loggedIn: true}
	codeOK := runDoctorWithEngines(context.Background(), cfg, map[domain.EngineName]app.Engine{domain.EngineAgy: goodEng}, &outOK, &errOK)
	if codeOK != 0 {
		t.Fatalf("doctor healthy exit = %d, want 0 (out=%s err=%s)", codeOK, outOK.String(), errOK.String())
	}
}

func TestBinaryChangeDisablesEngine(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	binPath := filepath.Join(t.TempDir(), "fakecli-bin")
	if err := os.WriteFile(binPath, []byte("binary-v1-contents"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	st, err := sqlite.Open(ctx, filepath.Join(dataDir, "max.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = st.Close() }()

	bus := platform.NewBus()
	noticeCh, unsub := bus.Subscribe([]string{"status"}, 8)
	defer unsub()

	guard := newBinaryGuard(st, bus, app.SystemClock{}, app.UUIDv7Gen{})
	slots := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 2}, nil)
	actors := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "chief-of-staff", Name: "CoS", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{
			AgentID: "a1", Kind: domain.WakeMessage, Priority: domain.PrioInteractive, Payload: json.RawMessage(`{"m":1}`),
		})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := guard.Confirm(ctx, domain.EngineAgy, binPath, "1.0.0"); err != nil {
		t.Fatalf("initial confirm: %v", err)
	}
	ok, err := guard.Verify(ctx, domain.EngineAgy, binPath)
	if err != nil || !ok {
		t.Fatalf("verify unchanged = %v, err=%v, want true", ok, err)
	}

	if err := os.WriteFile(binPath, []byte("binary-v2-contents"), 0o755); err != nil {
		t.Fatalf("tamper bin: %v", err)
	}
	ok2, err := guard.Verify(ctx, domain.EngineAgy, binPath)
	if err != nil || ok2 {
		t.Fatalf("verify after tamper = %v (err=%v), want false", ok2, err)
	}
	slots.SetHealthAvailable(domain.EngineAgy, ok2)
	claimedWhileTampered, err := actors.ClaimBatch(ctx, actor.Capacity{Free: slots.Free()}, time.Now())
	if err != nil || len(claimedWhileTampered) != 0 {
		t.Fatalf("claimed while engine binary tampered = %d (err=%v), want 0", len(claimedWhileTampered), err)
	}

	select {
	case ev := <-noticeCh:
		if ev.Type != "system.notice" {
			t.Fatalf("event type = %q, want system.notice", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("expected system.notice event on binary hash mismatch")
	}
	_ = st.Close()

	t.Setenv("MAX_DATA_DIR", dataDir)
	t.Setenv("MAX_AGY_BIN", binPath)
	var outBuf, errBuf bytes.Buffer
	if code := run([]string{"engine", "confirm", "agy"}, &outBuf, &errBuf); code != 0 {
		t.Fatalf("max engine confirm exit = %d, stderr = %s", code, errBuf.String())
	}

	st3, err := sqlite.Open(ctx, filepath.Join(dataDir, "max.sqlite3"))
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer func() { _ = st3.Close() }()
	guard2 := newBinaryGuard(st3, bus, app.SystemClock{}, app.UUIDv7Gen{})
	ok3, err := guard2.Verify(ctx, domain.EngineAgy, binPath)
	if err != nil || !ok3 {
		t.Fatalf("verify after max engine confirm = %v (err=%v), want true", ok3, err)
	}
	slots.SetHealthAvailable(domain.EngineAgy, ok3)
	actors3 := actor.NewService(st3, app.SystemClock{}, app.UUIDv7Gen{})
	claimedAfterConfirm, err := actors3.ClaimBatch(ctx, actor.Capacity{Free: slots.Free()}, time.Now())
	if err != nil || len(claimedAfterConfirm) != 1 {
		t.Fatalf("claimed after confirm = %d (err=%v), want 1", len(claimedAfterConfirm), err)
	}
}

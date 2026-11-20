package limits_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/limits"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type mutClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func TestSlotsNeverExceed(t *testing.T) {
	s := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 3}, nil)

	var active atomic.Int32
	var peak atomic.Int32
	var wg sync.WaitGroup

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if s.Acquire(domain.EngineAgy) {
					cur := active.Add(1)
					for {
						old := peak.Load()
						if cur <= old || peak.CompareAndSwap(old, cur) {
							break
						}
					}
					time.Sleep(time.Millisecond)
					active.Add(-1)
					s.Release(domain.EngineAgy)
				}
			}
		}()
	}
	wg.Wait()

	if peak.Load() > 3 {
		t.Fatalf("peak concurrent slots = %d, must never exceed 3", peak.Load())
	}
	if peak.Load() == 0 {
		t.Fatal("expected at least 1 slot acquired")
	}
}

func TestInteractiveReservation(t *testing.T) {
	s := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 3}, nil)

	if !s.AcquirePriority(domain.EngineAgy, domain.PrioTask) {
		t.Fatal("1st background slot should succeed")
	}
	if !s.AcquirePriority(domain.EngineAgy, domain.PrioTask) {
		t.Fatal("2nd background slot should succeed")
	}
	if s.AcquirePriority(domain.EngineAgy, domain.PrioTask) {
		t.Fatal("3rd background slot must be denied (1 slot reserved for interactive)")
	}
	if !s.AcquirePriority(domain.EngineAgy, domain.PrioInteractive) {
		t.Fatal("3rd slot must succeed for PrioInteractive")
	}
}

func TestMemoryGuard(t *testing.T) {
	lowMem := func() uint64 { return 512 * 1024 * 1024 } // 512 MiB < 1 GiB
	s := limits.NewSlots(map[domain.EngineName]int{
		domain.EngineAgy:    3,
		domain.EngineClaude: 2,
	}, lowMem)

	free := s.Free()
	for eng, n := range free {
		if n != 0 {
			t.Fatalf("Free()[%s] = %d when free RAM < 1 GiB, want 0", eng, n)
		}
	}
}

func seedQuotaAgent(t *testing.T, st app.Store) domain.ID {
	t.Helper()
	ctx := context.Background()
	agentID := "ag-quota"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: agentID, Slug: "chief", Name: "Chief", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		})
	}); err != nil {
		t.Fatalf("seedQuotaAgent: %v", err)
	}
	return agentID
}

func TestRecordRollsWindows(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID := seedQuotaAgent(t, st)
	t1 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	t2 := t1.Add(6 * time.Hour)
	clk := &mutClock{now: t1}

	q := limits.NewQuota(st, clk, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7, TZ: "UTC"})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return q.Record(ctx, tx, domain.EngineAgy, agentID, 300, 100, t1)
	}); err != nil {
		t.Fatalf("Record window 1: %v", err)
	}

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return q.Record(ctx, tx, domain.EngineAgy, agentID, 500, 150, t2)
	}); err != nil {
		t.Fatalf("Record window 2: %v", err)
	}

	rows, err := st.Read().CountQuotaLedgerRows(ctx, domain.EngineAgy, "short")
	if err != nil || rows != 2 {
		t.Fatalf("short ledger rows = %d (err=%v), want 2", rows, err)
	}

	v2, err := q.View(ctx, domain.EngineAgy, t2)
	if err != nil {
		t.Fatalf("View(t2): %v", err)
	}
	if v2.UsedShort != 650 {
		t.Fatalf("UsedShort = %d, want 650 (only current short window)", v2.UsedShort)
	}
	if v2.UsedWeek != 1050 {
		t.Fatalf("UsedWeek = %d, want 1050", v2.UsedWeek)
	}
}

func TestOnLimitLearns(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID := seedQuotaAgent(t, st)
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	clk := &mutClock{now: now}

	q := limits.NewQuota(st, clk, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7, TZ: "UTC"})
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return q.Record(ctx, tx, domain.EngineAgy, agentID, 700, 100, now)
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	resetAt := now.Add(40 * time.Minute)
	if err := q.OnLimit(ctx, domain.EngineAgy, &resetAt, now); err != nil {
		t.Fatalf("OnLimit: %v", err)
	}

	v, err := q.View(ctx, domain.EngineAgy, now)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.EstShort == nil || *v.EstShort != 800 {
		t.Fatalf("EstShort = %v, want 800", v.EstShort)
	}
	if v.LimitedUntil == nil || !v.LimitedUntil.Equal(resetAt) {
		t.Fatalf("LimitedUntil = %v, want %v", v.LimitedUntil, resetAt)
	}
	if got := q.Admit(domain.EngineAgy, domain.PrioInteractive); got != domain.DeferLimited {
		t.Fatalf("Admit while limited = %v, want DeferLimited", got)
	}
}

func TestAdmitAfterReset(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID := seedQuotaAgent(t, st)
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	clk := &mutClock{now: now}

	q := limits.NewQuota(st, clk, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7, TZ: "UTC"})
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return q.Record(ctx, tx, domain.EngineAgy, agentID, 700, 100, now)
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	resetAt := now.Add(15 * time.Minute)
	if err := q.OnLimit(ctx, domain.EngineAgy, &resetAt, now); err != nil {
		t.Fatalf("OnLimit: %v", err)
	}

	// Advance clock into the next 5-hour window past resetAt -> usage is 0, learned EstShort=800 stays!
	after := now.Add(6 * time.Hour)
	clk.Set(after)

	if got := q.Admit(domain.EngineAgy, domain.PrioTask); got != domain.AdmitNow {
		t.Fatalf("Admit after reset = %v, want AdmitNow", got)
	}
	v, err := q.View(ctx, domain.EngineAgy, after)
	if err != nil {
		t.Fatalf("View after reset: %v", err)
	}
	if v.EstShort == nil || *v.EstShort != 800 {
		t.Fatalf("learned EstShort lost after reset: %v, want 800", v.EstShort)
	}
}

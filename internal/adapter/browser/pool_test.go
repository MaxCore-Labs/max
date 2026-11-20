package browser_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/adapter/browser"
	"max/internal/app"
	"max/internal/app/actor"
	runapp "max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
	ch  []chan struct{}
}

func newManualClock(t0 time.Time) *manualClock {
	return &manualClock{now: t0}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	subs := c.ch
	c.ch = nil
	c.mu.Unlock()
	for _, s := range subs {
		close(s)
	}
}

func (c *manualClock) WaitTick() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := make(chan struct{})
	c.ch = append(c.ch, s)
	return s
}

func TestSeatsNeverExceed(t *testing.T) {
	clk := newManualClock(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	pool := browser.NewPool(browser.Config{
		Seats:      1,
		CDPHostURL: "http://127.0.0.1:9222",
		Clock:      clk,
		ClockWait:  clk.WaitTick,
	})

	ep1, err := pool.Acquire(context.Background(), "run-1", domain.PrioTask, 20*time.Second)
	if err != nil || ep1 == "" {
		t.Fatalf("run-1 Acquire: ep=%q err=%v", ep1, err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, rid := range []domain.ID{"run-2", "run-3"} {
		wg.Add(1)
		go func(idx int, id domain.ID) {
			defer wg.Done()
			_, errs[idx] = pool.Acquire(context.Background(), id, domain.PrioTask, 20*time.Second)
		}(i, rid)
	}

	time.Sleep(30 * time.Millisecond)
	if pool.ActiveSeats() != 1 {
		t.Fatalf("ActiveSeats = %d, want 1", pool.ActiveSeats())
	}
	clk.Advance(20 * time.Second)
	wg.Wait()

	for i, e := range errs {
		if !errors.Is(e, browser.ErrBusy) {
			t.Fatalf("waiter %d err = %v, want ErrBusy", i+1, e)
		}
	}
}

func TestInteractiveFirst(t *testing.T) {
	clk := newManualClock(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	pool := browser.NewPool(browser.Config{
		Seats:      1,
		CDPHostURL: "http://127.0.0.1:9222",
		Clock:      clk,
		ClockWait:  clk.WaitTick,
	})

	if _, err := pool.Acquire(context.Background(), "run-holder", domain.PrioTask, time.Second); err != nil {
		t.Fatalf("holder Acquire: %v", err)
	}

	order := make(chan domain.ID, 2)
	go func() {
		if _, err := pool.Acquire(context.Background(), "run-bg", domain.PrioTask, 20*time.Second); err == nil {
			order <- "run-bg"
			_ = pool.Release(context.Background(), "run-bg")
		}
	}()
	time.Sleep(25 * time.Millisecond)
	go func() {
		if _, err := pool.Acquire(context.Background(), "run-interactive", domain.PrioInteractive, 20*time.Second); err == nil {
			order <- "run-interactive"
			_ = pool.Release(context.Background(), "run-interactive")
		}
	}()
	time.Sleep(25 * time.Millisecond)

	_ = pool.Release(context.Background(), "run-holder")

	first := <-order
	second := <-order
	if first != "run-interactive" || second != "run-bg" {
		t.Fatalf("acquire order = [%s, %s], want [run-interactive, run-bg]", first, second)
	}
}

func TestLeaseMax10Min(t *testing.T) {
	clk := newManualClock(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	pool := browser.NewPool(browser.Config{
		Seats:      1,
		CDPHostURL: "http://127.0.0.1:9222",
		Clock:      clk,
		ClockWait:  clk.WaitTick,
	})

	if _, err := pool.Acquire(context.Background(), "run-long", domain.PrioTask, time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := pool.ValidateLease("run-long"); err != nil {
		t.Fatalf("ValidateLease before 10m: %v", err)
	}

	clk.Advance(10*time.Minute + time.Second)
	pool.ReapExpired(context.Background())

	if err := pool.ValidateLease("run-long"); err == nil {
		t.Fatal("expected lease to be revoked after 10 minutes")
	}
	if pool.ActiveSeats() != 0 {
		t.Fatalf("ActiveSeats after expiry = %d, want 0", pool.ActiveSeats())
	}
}

func TestReleaseOnRunEnd(t *testing.T) {
	var closedSeed atomic.Value
	cdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/close") {
			closedSeed.Store(r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/fingerprint/anon/devtools/browser/1"}`))
	}))
	defer cdp.Close()

	pool := browser.NewPool(browser.Config{
		Seats:      1,
		CDPHostURL: cdp.URL,
	})
	st := testdb.New(t)
	ctx := context.Background()
	actors := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"})
		_ = tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "researcher", Name: "Res", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserAnon, ConfigRev: "r1", Status: domain.AgentActive,
		})
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{
			AgentID: "a1", Kind: domain.WakeMessage, Priority: domain.PrioTask, Payload: []byte(`{"x":1}`),
		})
		return err
	})
	claimed, _ := actors.ClaimBatch(ctx, actor.Capacity{
		Free:  map[domain.EngineName]int{domain.EngineAgy: 1},
		Admit: func(domain.EngineName, domain.Priority) domain.Admission { return domain.AdmitNow },
	}, time.Now())

	ep, err := pool.Acquire(ctx, claimed[0].RunID, domain.PrioTask, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	spec := pool.MCPSpec(ep)
	outDir := ""
	for i := 0; i < len(spec.Args)-1; i++ {
		if spec.Args[i] == "--output-dir" {
			outDir = spec.Args[i+1]
		}
	}

	eng := fakeengine.New(domain.EngineAgy, app.EngineEvent{Type: "error", ErrKind: "boom", ErrMessage: "simulated crash"})
	runSvc := runapp.NewService(runapp.Deps{
		Store:   st,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		OnRunEnd: func(runID domain.ID) {
			_ = pool.Release(context.Background(), runID)
		},
	})
	_, _ = runSvc.Execute(ctx, claimed[0])

	if pool.ActiveSeats() != 0 {
		t.Fatalf("ActiveSeats after run end = %d, want 0", pool.ActiveSeats())
	}
	if closedSeed.Load() != "/fingerprint/anon/close" {
		t.Fatalf("closedSeed = %v, want /fingerprint/anon/close", closedSeed.Load())
	}
	if outDir != "" {
		if _, err := os.Stat(outDir); !os.IsNotExist(err) {
			t.Fatalf("expected isolated temp output dir %q removed on Release", outDir)
		}
	}
}

func TestBrowserLocaleFromServerSetting(t *testing.T) {
	pool := browser.NewPool(browser.Config{
		Seats:         1,
		CDPHostURL:    "http://127.0.0.1:9222",
		BrowserTZ:     "America/New_York",
		BrowserLocale: "en-US",
		OwnerTZ:       "Asia/Ho_Chi_Minh",
	})
	spec := pool.MCPSpec("ws://127.0.0.1:9222/fingerprint/anon/devtools/browser/1")
	joined := strings.Join(spec.Env, "\n")
	if !strings.Contains(joined, "TZ=America/New_York") {
		t.Fatalf("expected TZ=America/New_York from MAX_BROWSER_TZ, got env:\n%s", joined)
	}
	if strings.Contains(joined, "Asia/Ho_Chi_Minh") {
		t.Fatalf("browser env must NOT inherit owner MAX_TZ Asia/Ho_Chi_Minh:\n%s", joined)
	}
	if !strings.Contains(joined, "LANG=en-US") && !strings.Contains(joined, "MAX_BROWSER_LOCALE=en-US") {
		t.Fatalf("expected MAX_BROWSER_LOCALE=en-US in browser env:\n%s", joined)
	}
}

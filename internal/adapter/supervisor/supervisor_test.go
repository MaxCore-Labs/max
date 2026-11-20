package supervisor_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/adapter/supervisor"
	"max/internal/platform"
)

func TestHelperChildProc(t *testing.T) {
	mode := os.Getenv("MAX_SUP_HELPER_MODE")
	if mode == "" {
		return
	}
	countFile := os.Getenv("MAX_SUP_COUNT_FILE")
	var runNum int
	if countFile != "" {
		raw, _ := os.ReadFile(countFile)
		n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		runNum = n + 1
		_ = os.WriteFile(countFile, []byte(strconv.Itoa(runNum)), 0o600)
	}

	switch mode {
	case "crash3":
		if runNum <= 3 {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
	case "crash4":
		if runNum <= 4 {
			os.Exit(3)
		}
		time.Sleep(30 * time.Second)
	case "tree":
		exe, _ := os.Executable()
		sub := exec.Command(exe, "-test.run=^TestHelperChildProc$")
		sub.Env = append(os.Environ(), "MAX_SUP_HELPER_MODE=leaf", "MAX_SUP_LEAF_PID_FILE="+os.Getenv("MAX_SUP_LEAF_PID_FILE"))
		_ = sub.Start()
		if pidFile := os.Getenv("MAX_SUP_ROOT_PID_FILE"); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		_ = sub.Wait()
	case "leaf":
		if pidFile := os.Getenv("MAX_SUP_LEAF_PID_FILE"); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		time.Sleep(30 * time.Second)
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func helperChild(name, mode string, extraEnv ...string) supervisor.Child {
	exe, _ := os.Executable()
	env := append(os.Environ(), "MAX_SUP_HELPER_MODE="+mode)
	env = append(env, extraEnv...)
	return supervisor.Child{
		Name: name,
		Bin:  exe,
		Args: []string{"-test.run=^TestHelperChildProc$"},
		Env:  env,
	}
}

func waitUntil(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestRestartsOnExit(t *testing.T) {
	// Also verify pure BackoffDelay progression caps at 60s.
	for attempt, want := range []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		60 * time.Second,
		60 * time.Second,
	} {
		got := supervisor.BackoffDelay(attempt+1, time.Second, 60*time.Second)
		if got != want {
			t.Fatalf("BackoffDelay(%d) = %v, want %v", attempt+1, got, want)
		}
	}

	var mu sync.Mutex
	var recordedSleeps []time.Duration
	sup := supervisor.New(supervisor.Options{
		BaseBackoff: 10 * time.Second,
		MaxBackoff:  60 * time.Second,
		Sleep: func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			recordedSleeps = append(recordedSleeps, d)
			mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
				return nil
			}
		},
	})

	countFile := filepath.Join(t.TempDir(), "count.txt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sup.Start(ctx, helperChild("worker", "crash3", "MAX_SUP_COUNT_FILE="+countFile)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitUntil(t, 5*time.Second, func() bool {
		st := sup.Status()["worker"]
		return st.Restarts == 3 && st.Running
	}, "expected 3 restarts and running=true on 4th start")

	mu.Lock()
	sleeps := append([]time.Duration(nil), recordedSleeps...)
	mu.Unlock()
	wantSleeps := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second}
	if fmt.Sprint(sleeps) != fmt.Sprint(wantSleeps) {
		t.Fatalf("recorded backoff sleeps = %v, want %v", sleeps, wantSleeps)
	}
	// Verify cap at 60s on subsequent crashes:
	if capped := supervisor.BackoffDelay(4, 10*time.Second, 60*time.Second); capped != 60*time.Second {
		t.Fatalf("4th backoff = %v, want 60s cap", capped)
	}
}

func TestNoticeAfter3PerHour(t *testing.T) {
	bus := platform.NewBus()
	evCh, unsub := bus.Subscribe([]string{"system"}, 16)
	defer unsub()

	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	sup := supervisor.New(supervisor.Options{
		Bus:         bus,
		Now:         func() time.Time { return now },
		BaseBackoff: time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			return ctx.Err()
		},
	})

	countFile := filepath.Join(t.TempDir(), "count4.txt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sup.Start(ctx, helperChild("flaky", "crash4", "MAX_SUP_COUNT_FILE="+countFile)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitUntil(t, 5*time.Second, func() bool {
		return sup.Status()["flaky"].Restarts >= 4 && sup.Status()["flaky"].Running
	}, "expected 4 restarts")

	time.Sleep(80 * time.Millisecond)
	var notices []platform.Event
	for {
		select {
		case ev := <-evCh:
			if ev.Type == "system.notice" {
				notices = append(notices, ev)
			}
		default:
			goto doneDrain
		}
	}
doneDrain:
	if len(notices) != 1 {
		t.Fatalf("got %d system.notice events, want exactly 1", len(notices))
	}
}

func TestStopOnShutdown(t *testing.T) {
	dir := t.TempDir()
	rootPIDFile := filepath.Join(dir, "root.pid")
	leafPIDFile := filepath.Join(dir, "leaf.pid")

	sup := supervisor.New(supervisor.Options{KillGrace: 200 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())

	if err := sup.Start(ctx, helperChild(
		"tree",
		"tree",
		"MAX_SUP_ROOT_PID_FILE="+rootPIDFile,
		"MAX_SUP_LEAF_PID_FILE="+leafPIDFile,
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var rootPID, leafPID int
	waitUntil(t, 5*time.Second, func() bool {
		b1, e1 := os.ReadFile(rootPIDFile)
		b2, e2 := os.ReadFile(leafPIDFile)
		if e1 != nil || e2 != nil {
			return false
		}
		rootPID, _ = strconv.Atoi(strings.TrimSpace(string(b1)))
		leafPID, _ = strconv.Atoi(strings.TrimSpace(string(b2)))
		return rootPID > 0 && leafPID > 0 && supervisor.ProcessAlive(leafPID)
	}, "expected root and leaf child processes to be running")

	cancel()

	waitUntil(t, 5*time.Second, func() bool {
		return !supervisor.ProcessAlive(rootPID) && !supervisor.ProcessAlive(leafPID) && !sup.Status()["tree"].Running
	}, "expected entire process group (root + leaf) to be terminated on context cancel")
}

func TestUnhealthyNotCrashing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sup := supervisor.New(supervisor.Options{
		HealthInterval: 25 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := helperChild("degraded", "sleep")
	ch.HealthURL = srv.URL + "/healthz"
	if err := sup.Start(ctx, ch); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitUntil(t, 3*time.Second, func() bool {
		return hits.Load() >= 3
	}, "expected health probe to poll HTTP 500 endpoint")

	st := sup.Status()["degraded"]
	if !st.Running {
		t.Fatalf("Running = false, want true (HTTP 500 must not kill process)")
	}
	if st.Healthy {
		t.Fatalf("Healthy = true, want false on HTTP 500")
	}
	if st.Restarts != 0 {
		t.Fatalf("Restarts = %d, want 0 (must not enter infinite restart loop)", st.Restarts)
	}
}

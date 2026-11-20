package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"max/internal/adapter/engine"
)

var (
	fakeCLIOnce sync.Once
	fakeCLIBin  string
	fakeCLIErr  error
)

func buildFakeCLI(t *testing.T) string {
	t.Helper()
	fakeCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "max-fakecli-*")
		if err != nil {
			fakeCLIErr = err
			return
		}
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		out := filepath.Join(dir, "fakecli"+ext)
		cmd := exec.Command("go", "build", "-o", out, "max/internal/testutil/fakecli")
		if b, err := cmd.CombinedOutput(); err != nil {
			fakeCLIErr = os.ErrInvalid
			t.Logf("build fakecli failed: %v\n%s", err, string(b))
			return
		}
		fakeCLIBin = out
	})
	if fakeCLIErr != nil || fakeCLIBin == "" {
		t.Fatalf("failed to build fakecli: %v", fakeCLIErr)
	}
	return fakeCLIBin
}

func writeScript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writeScript: %v", err)
	}
	return p
}

func baseEnv(script string, extra ...string) []string {
	env := []string{"FAKECLI_SCRIPT=" + script}
	if runtime.GOOS == "windows" {
		if sr := os.Getenv("SystemRoot"); sr != "" {
			env = append(env, "SYSTEMROOT="+sr)
		}
	}
	return append(env, extra...)
}

func TestStartStreamsLines(t *testing.T) {
	bin := buildFakeCLI(t)
	script := writeScript(t, `{"n":1}`, `{"n":2}`, `{"n":3}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, stdin, linesCh, errCh, err := engine.Start(ctx, engine.ProcSpec{
		Bin:   bin,
		Env:   baseEnv(script),
		Grace: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = stdin.Close()

	var got []string
	for line := range linesCh {
		got = append(got, string(line))
	}
	if err := <-errCh; err != nil {
		t.Fatalf("errCh = %v, want nil", err)
	}
	if len(got) != 3 || got[0] != `{"n":1}` || got[1] != `{"n":2}` || got[2] != `{"n":3}` {
		t.Fatalf("got lines = %v", got)
	}
}

func TestHugeLine(t *testing.T) {
	bin := buildFakeCLI(t)
	const size = 8 * 1024 * 1024
	script := writeScript(t, "huge "+strconv.Itoa(size))

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, stdin, linesCh, errCh, err := engine.Start(ctx, engine.ProcSpec{
		Bin:   bin,
		Env:   baseEnv(script),
		Grace: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = stdin.Close()

	var gotLen int
	count := 0
	for line := range linesCh {
		count++
		gotLen = len(line)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("errCh = %v, want nil", err)
	}
	if count != 1 || gotLen != size {
		t.Fatalf("count=%d len=%d, want 1 and %d", count, gotLen, size)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	var delta uint64
	if after.HeapInuse > before.HeapInuse {
		delta = after.HeapInuse - before.HeapInuse
	}
	if delta > 64*1024*1024 {
		t.Fatalf("heap delta = %d bytes (> 64 MB)", delta)
	}
}

func TestPartialLineOnKill(t *testing.T) {
	bin := buildFakeCLI(t)
	script := writeScript(t, `partial {"incomplete":true`, "hang")

	ctx, cancel := context.WithCancel(context.Background())
	_, stdin, linesCh, errCh, err := engine.Start(ctx, engine.ProcSpec{
		Bin:   bin,
		Env:   baseEnv(script),
		Grace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = stdin.Close()

	time.Sleep(120 * time.Millisecond)
	cancel()

	var lines []string
	for line := range linesCh {
		lines = append(lines, string(line))
	}
	if len(lines) != 0 {
		t.Fatalf("expected partial line to be dropped, got %v", lines)
	}
	procErr := <-errCh
	if procErr == nil || !strings.Contains(procErr.Error(), "killed") {
		t.Fatalf("errCh = %v, want error containing 'killed'", procErr)
	}
}

func TestIdleTimeout(t *testing.T) {
	bin := buildFakeCLI(t)
	script := writeScript(t, "hang")

	start := time.Now()
	_, stdin, linesCh, errCh, err := engine.Start(context.Background(), engine.ProcSpec{
		Bin:         bin,
		Env:         baseEnv(script),
		IdleTimeout: 200 * time.Millisecond,
		Grace:       100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = stdin.Close()

	for range linesCh {
	}
	procErr := <-errCh
	elapsed := time.Since(start)
	if procErr == nil {
		t.Fatal("expected idle timeout error, got nil")
	}
	if elapsed >= time.Second {
		t.Fatalf("IdleTimeout took %v, want < 1s", elapsed)
	}
}

func TestKillGroupKillsChildren(t *testing.T) {
	bin := buildFakeCLI(t)
	script := writeScript(t, "spawn_child", "hang")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, stdin, linesCh, errCh, err := engine.Start(ctx, engine.ProcSpec{
		Bin:   bin,
		Env:   baseEnv(script),
		Grace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = stdin.Close()

	var childPID int
	select {
	case line := <-linesCh:
		s := strings.TrimPrefix(string(line), "child_pid=")
		childPID, err = strconv.Atoi(s)
		if err != nil || childPID <= 0 {
			t.Fatalf("bad child_pid line %q: %v", string(line), err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for child_pid")
	}

	if err := engine.KillGroup(proc.PGID, 100*time.Millisecond); err != nil {
		t.Fatalf("KillGroup: %v", err)
	}
	for range linesCh {
	}
	for range errCh {
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !engine.ProcessAlive(proc.PID) && !engine.ProcessAlive(childPID) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("processes still alive after KillGroup: parent=%v child=%v",
		engine.ProcessAlive(proc.PID), engine.ProcessAlive(childPID))
}

func TestEnvIsExact(t *testing.T) {
	bin := buildFakeCLI(t)
	t.Setenv("MAX_PARENT_SECRET_SHOULD_NOT_LEAK", "super-secret")
	script := writeScript(t, "dump_env")
	wantEnv := baseEnv(script, "ONLY_KEY=only_val")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, stdin, linesCh, errCh, err := engine.Start(ctx, engine.ProcSpec{
		Bin:   bin,
		Env:   wantEnv,
		Grace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = stdin.Close()

	var raw string
	for line := range linesCh {
		raw = string(line)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("errCh: %v", err)
	}
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal env %q: %v", raw, err)
	}
	wantMap := make(map[string]string, len(wantEnv))
	for _, kv := range wantEnv {
		k, v, _ := strings.Cut(kv, "=")
		wantMap[strings.ToUpper(k)] = v
	}
	if len(got) != len(wantMap) {
		t.Fatalf("child env count = %d (%v), want exact %d (%v)", len(got), got, len(wantMap), wantEnv)
	}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if wantV, ok := wantMap[strings.ToUpper(k)]; !ok || wantV != v {
			t.Fatalf("unexpected or mismatched child env %q (want %v)", kv, wantEnv)
		}
	}
}

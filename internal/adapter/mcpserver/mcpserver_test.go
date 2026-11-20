package mcpserver_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/adapter/engine"
	"max/internal/adapter/mcpserver"
	"max/internal/app"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

var (
	mcpFakeCLIOnce sync.Once
	mcpFakeCLIBin  string
)

func buildMCPFakeCLI(t *testing.T) string {
	t.Helper()
	mcpFakeCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "max-mcp-cli-*")
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
			mcpFakeCLIBin = out
		}
	})
	if mcpFakeCLIBin == "" {
		t.Fatal("failed to build fakecli")
	}
	return mcpFakeCLIBin
}

func sampleCaller(runID, slug string) tools.Caller {
	return tools.Caller{
		RunID:          runID,
		AgentID:        "ag-" + slug,
		ConversationID: "cv-" + slug,
		Policy: domain.AgentPolicy{
			Scope:    domain.AgentScope{Slug: slug, TeamSlug: "core"},
			Autonomy: domain.AutonomyAuto,
		},
		Origin: domain.OriginOwner,
	}
}

func TestResolveByToken(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	toolSvc := tools.NewService(st, t.TempDir(), app.SystemClock{}, app.UUIDv7Gen{})
	srv := mcpserver.NewServer(toolSvc)
	mcpserver.SetDefaultServer(srv)

	cA := sampleCaller("run-active", "chief")
	srv.RegisterRun(cA, "tok-active", 0)

	got, err := mcpserver.ResolveCaller(ctx, "tok-active", 0)
	if err != nil || got.RunID != "run-active" || got.Policy.Scope.Slug != "chief" {
		t.Fatalf("ResolveCaller active = (%+v, %v)", got, err)
	}

	srv.UnregisterRun("run-active")
	if _, err := mcpserver.ResolveCaller(ctx, "tok-active", 0); err == nil {
		t.Fatal("expected error for unregistered/ended run token")
	}
}

func spawnParentAndChild(t *testing.T) (proc *engine.Proc, childPID int, cleanup func()) {
	t.Helper()
	bin := buildMCPFakeCLI(t)
	scriptPath := filepath.Join(t.TempDir(), "tree.txt")
	if err := os.WriteFile(scriptPath, []byte("spawn_child\nhang\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	env := []string{"FAKECLI_SCRIPT=" + scriptPath}
	if runtime.GOOS == "windows" {
		if sr := os.Getenv("SystemRoot"); sr != "" {
			env = append(env, "SYSTEMROOT="+sr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	p, stdin, linesCh, errCh, err := engine.Start(ctx, engine.ProcSpec{
		Bin:   bin,
		Env:   env,
		Grace: 100 * time.Millisecond,
	})
	if err != nil {
		cancel()
		t.Fatalf("engine.Start: %v", err)
	}
	_ = stdin.Close()

	line := <-linesCh
	s := strings.TrimPrefix(string(line), "child_pid=")
	cpid, err := strconv.Atoi(s)
	if err != nil || cpid <= 0 {
		cancel()
		t.Fatalf("parse child_pid %q: %v", string(line), err)
	}
	return p, cpid, func() {
		cancel()
		_ = engine.KillGroup(p.PGID, 100*time.Millisecond)
		for range linesCh {
		}
		for range errCh {
		}
	}
}

func TestResolveByProcessTree(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	toolSvc := tools.NewService(st, t.TempDir(), app.SystemClock{}, app.UUIDv7Gen{})
	srv := mcpserver.NewServer(toolSvc)
	mcpserver.SetDefaultServer(srv)

	parentProc, childPID, cleanup := spawnParentAndChild(t)
	defer cleanup()

	cParent := sampleCaller("run-tree-1", "researcher")
	srv.RegisterRun(cParent, "", parentProc.PID)
	defer srv.UnregisterRun("run-tree-1")

	got, err := mcpserver.ResolveCaller(ctx, "", childPID)
	if err != nil || got.RunID != "run-tree-1" {
		t.Fatalf("ResolveCaller by childPID %d (parent %d) = (%+v, %v), want run-tree-1",
			childPID, parentProc.PID, got, err)
	}

	if _, err := mcpserver.ResolveCaller(ctx, "", 99999999); err == nil {
		t.Fatal("expected error for unrelated PID")
	}
}

func TestResolveRejectsCrossRunToken(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	toolSvc := tools.NewService(st, t.TempDir(), app.SystemClock{}, app.UUIDv7Gen{})
	srv := mcpserver.NewServer(toolSvc)
	mcpserver.SetDefaultServer(srv)

	procA, childPIDOfA, cleanupA := spawnParentAndChild(t)
	defer cleanupA()
	procB, _, cleanupB := spawnParentAndChild(t)
	defer cleanupB()

	srv.RegisterRun(sampleCaller("run-A", "writer-a"), "tok-A", procA.PID)
	srv.RegisterRun(sampleCaller("run-B", "writer-b"), "tok-B", procB.PID)
	defer srv.UnregisterRun("run-A")
	defer srv.UnregisterRun("run-B")

	// Token of run B presented from a process inside run A's tree must be rejected!
	if _, err := mcpserver.ResolveCaller(ctx, "tok-B", childPIDOfA); err == nil {
		t.Fatal("expected cross-run token theft (tok-B from run-A child PID) to be rejected")
	}
}

func TestUnixSocketStdioBridgePing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st := testdb.New(t)
	toolSvc := tools.NewService(st, t.TempDir(), app.SystemClock{}, app.UUIDv7Gen{})
	srv := mcpserver.NewServer(toolSvc)
	srv.RegisterRun(sampleCaller("run-sock-1", "chief-of-staff"), "tok-sock-1", os.Getpid())
	defer srv.UnregisterRun("run-sock-1")

	sock := filepath.Join(os.TempDir(), fmt.Sprintf("mx-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(sock) })

	if err := srv.StartUnix(ctx, sock); err != nil {
		t.Fatalf("StartUnix: %v", err)
	}

	clientToBridgeR, clientToBridgeW := io.Pipe()
	bridgeToClientR, bridgeToClientW := io.Pipe()

	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- mcpserver.RunBridgeIO(ctx, sock, "tok-sock-1", clientToBridgeR, bridgeToClientW)
		_ = bridgeToClientW.Close()
	}()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	sess, err := mcpClient.Connect(ctx, &mcp.IOTransport{Reader: bridgeToClientR, Writer: clientToBridgeW}, nil)
	if err != nil {
		t.Fatalf("mcpClient.Connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatalf("CallTool(ping): %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty ping content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(tc.Text, "chief-of-staff") {
		t.Fatalf("unexpected ping response: %+v", res.Content[0])
	}
	_ = clientToBridgeW.Close()
	for range bridgeDone {
		break
	}
}

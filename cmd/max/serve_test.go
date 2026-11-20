package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/platform"
)

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

type scriptedChatEngine struct {
	vaultDir   string
	foreignMCP []string
	loggedIn   bool
}

func (e *scriptedChatEngine) Name() domain.EngineName { return domain.EngineAgy }
func (e *scriptedChatEngine) Caps() app.EngineCaps {
	return app.EngineCaps{PerRunMCP: true, Resume: true}
}
func (e *scriptedChatEngine) Health(context.Context) app.EngineHealth {
	return app.EngineHealth{
		Installed:  true,
		LoggedIn:   e.loggedIn,
		Version:    "1.0.0-fake",
		ForeignMCP: e.foreignMCP,
	}
}
func (e *scriptedChatEngine) Run(_ context.Context, spec app.RunSpec, perm app.PermissionFunc) (<-chan app.EngineEvent, error) {
	ch := make(chan app.EngineEvent, 8)
	go func() {
		defer close(ch)
		noteRel := filepath.Join("agents", spec.AgentSlug, "notes", "NOTES.md")
		noteFull := filepath.Join(e.vaultDir, noteRel)
		if perm != nil {
			dec := perm(context.Background(), app.ToolAsk{
				RunToken: spec.RunToken,
				ToolName: "write_file",
				Path:     noteFull,
			})
			if dec.Verdict == domain.Allow {
				_ = os.MkdirAll(filepath.Dir(noteFull), 0o755)
				_ = os.WriteFile(noteFull, []byte("# Ghi chú\nĐã xử lý tin nhắn.\n"), 0o644)
			}
		}
		ch <- app.EngineEvent{Type: "session", SessionID: "sess-e2e-1"}
		ch <- app.EngineEvent{Type: "text_delta", Text: "Xin chào chủ nhân, "}
		ch <- app.EngineEvent{Type: "text_delta", Text: "tôi đã ghi nhận!"}
		ch <- app.EngineEvent{Type: "final", InputTokens: 50, OutputTokens: 20}
	}()
	return ch, nil
}

func TestServeEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	listenAddr := freeLoopbackAddr(t)
	pubURL := "http://" + listenAddr
	adminPass := "e2e-admin-password-123"

	eng := &scriptedChatEngine{vaultDir: vaultDir, loggedIn: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan int, 1)
	go func() {
		code := runServeWithOptions(ctx, ServeOptions{
			Config: platform.Config{
				DataDir:       dataDir,
				VaultDir:      vaultDir,
				Listen:        listenAddr,
				PublicURL:     pubURL,
				AdminPassword: adminPass,
				Slots:         map[domain.EngineName]int{domain.EngineAgy: 2},
			},
			Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		}, &bytes.Buffer{}, &bytes.Buffer{})
		doneCh <- code
	}()

	// Wait for healthz
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(pubURL + "/api/v1/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 1. Login
	loginReq, _ := http.NewRequest(http.MethodPost, pubURL+"/api/v1/auth/login", strings.NewReader(`{"password":"`+adminPass+`"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp, err := client.Do(loginReq)
	if err != nil || loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: err=%v resp=%v", err, loginResp)
	}
	cookie := loginResp.Cookies()[0]
	_ = loginResp.Body.Close()

	// 2. Create thread
	thReq, _ := http.NewRequest(http.MethodPost, pubURL+"/api/v1/threads", strings.NewReader(`{"title":"E2E"}`))
	thReq.Header.Set("Content-Type", "application/json")
	thReq.Header.Set("Idempotency-Key", "idem-th-1")
	thReq.AddCookie(cookie)
	thResp, err := client.Do(thReq)
	if err != nil || thResp.StatusCode != http.StatusCreated {
		t.Fatalf("create thread failed: err=%v resp=%v", err, thResp)
	}
	var th struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(thResp.Body).Decode(&th)
	_ = thResp.Body.Close()

	// 3. Subscribe SSE to thread:<id>
	sseCtx, sseCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer sseCancel()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, pubURL+"/api/v1/events?topics=thread:"+th.ID, nil)
	sseReq.AddCookie(cookie)
	sseClient := &http.Client{}
	sseResp, err := sseClient.Do(sseReq)
	if err != nil || sseResp.StatusCode != http.StatusOK {
		t.Fatalf("SSE connect failed: err=%v resp=%v", err, sseResp)
	}
	defer func() { _ = sseResp.Body.Close() }()

	sawRunText := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(sseResp.Body)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "event: run.text") {
				select {
				case sawRunText <- true:
				default:
				}
				return
			}
		}
	}()

	// 4. Send message
	msgReq, _ := http.NewRequest(http.MethodPost, pubURL+"/api/v1/threads/"+th.ID+"/messages", strings.NewReader(`{"content":"Báo cáo nhanh"}`))
	msgReq.Header.Set("Content-Type", "application/json")
	msgReq.Header.Set("Idempotency-Key", "idem-msg-1")
	msgReq.AddCookie(cookie)
	msgResp, err := client.Do(msgReq)
	if err != nil || msgResp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message failed: err=%v resp=%v", err, msgResp)
	}
	_ = msgResp.Body.Close()

	select {
	case <-sawRunText:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run.text SSE event")
	}

	// 5. Verify assistant message saved and vault commit created
	var assistantSaved bool
	for range 40 {
		snapReq, _ := http.NewRequest(http.MethodGet, pubURL+"/api/v1/threads/"+th.ID, nil)
		snapReq.AddCookie(cookie)
		snapResp, err := client.Do(snapReq)
		if err == nil && snapResp.StatusCode == http.StatusOK {
			var snap struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(snapResp.Body).Decode(&snap)
			_ = snapResp.Body.Close()
			for _, m := range snap.Messages {
				if m.Role == "assistant" && strings.Contains(m.Content, "tôi đã ghi nhận") {
					assistantSaved = true
				}
			}
			if assistantSaved {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !assistantSaved {
		t.Fatal("assistant message was not persisted to messages table")
	}

	gitCmd := exec.Command("git", "-C", vaultDir, "rev-list", "--count", "HEAD")
	cntOut, err := gitCmd.Output()
	if err != nil || strings.TrimSpace(string(cntOut)) == "1" {
		t.Fatalf("vault commit count = %q (err=%v), want >= 2 after run commit", string(cntOut), err)
	}

	// 6. Graceful shutdown returns exit code 0
	cancel()
	select {
	case code := <-doneCh:
		if code != 0 {
			t.Fatalf("serve exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down within 10s")
	}
}

func TestServeRecoversOnBoot(t *testing.T) {
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	dbPath := filepath.Join(dataDir, "max.sqlite3")

	st, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	ctx := context.Background()
	actors := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	err = st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "chief-of-staff", Name: "CoS", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "init", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{
			AgentID: "a1", Kind: domain.WakeMessage, Priority: domain.PrioInteractive, Payload: json.RawMessage(`{"x":1}`),
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	claimed, err := actors.ClaimBatch(ctx, actor.Capacity{
		Free:  map[domain.EngineName]int{domain.EngineAgy: 1},
		Admit: func(domain.EngineName, domain.Priority) domain.Admission { return domain.AdmitNow },
	}, time.Now())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim crashed run: %v", err)
	}
	crashedRunID := claimed[0].RunID
	_ = st.Close()

	listenAddr := freeLoopbackAddr(t)
	eng := &scriptedChatEngine{vaultDir: vaultDir, loggedIn: true}
	srvCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan int, 1)
	go func() {
		doneCh <- runServeWithOptions(srvCtx, ServeOptions{
			Config: platform.Config{
				DataDir:       dataDir,
				VaultDir:      vaultDir,
				Listen:        listenAddr,
				PublicURL:     "http://" + listenAddr,
				AdminPassword: "boot-recover-password",
				Slots:         map[domain.EngineName]int{domain.EngineAgy: 2},
			},
			Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		}, &bytes.Buffer{}, &bytes.Buffer{})
	}()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, hErr := http.Get("http://" + listenAddr + "/healthz")
		if hErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(600 * time.Millisecond)
	cancel()
	<-doneCh

	st2, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer func() { _ = st2.Close() }()

	oldRun, ok, _ := st2.Read().GetRun(ctx, crashedRunID)
	if !ok || oldRun.Status != domain.RunInterrupted {
		t.Fatalf("crashed run status = %v (ok=%v), want interrupted", oldRun.Status, ok)
	}
	runs, _ := st2.Read().ListRunsPage(ctx, "a1", "succeeded", 10, nil, "")
	if len(runs) == 0 {
		t.Fatal("expected re-queued wakeup to execute and succeed after recovery")
	}
}

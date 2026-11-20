package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsQuotaLimitSignalNoFalsePositiveOn429Substring(t *testing.T) {
	if isQuotaLimitSignal("unexpected stopReason=\"max_tokens\" totalTokens=42915 session=abc429def", "", "max_tokens") {
		t.Fatal("bare 429 inside token count or session ID must not match quota signal")
	}
	if !isQuotaLimitSignal("rpc session/prompt (-32000): RESOURCE_EXHAUSTED: quota exceeded", "", "") {
		t.Fatal("expected RESOURCE_EXHAUSTED to match")
	}
	if !isQuotaLimitSignal("", "Error: HTTP 429 Too Many Requests", "end_turn") {
		t.Fatal("expected stderr HTTP 429 to match")
	}
}

func TestSelectPermissionOptionStrict(t *testing.T) {
	opts := []permOption{
		{OptionID: "allow_always", Kind: "allow_always"},
		{OptionID: "allow_once", Kind: "allow_once"},
		{OptionID: "reject_once", Kind: "reject_once"},
	}
	if got, ok := selectPermissionOption("read", opts); !ok || got != "allow_once" {
		t.Fatalf("read expected allow_once, got %q (%v)", got, ok)
	}
	if got, ok := selectPermissionOption("edit", opts); !ok || got != "allow_once" {
		t.Fatalf("edit expected allow_once, got %q (%v)", got, ok)
	}
	if got, ok := selectPermissionOption("execute", opts); !ok || got != "reject_once" {
		t.Fatalf("execute expected reject_once, got %q (%v)", got, ok)
	}
	if _, ok := selectPermissionOption("read", []permOption{{OptionID: "allow_always", Kind: "allow_always"}}); ok {
		t.Fatal("must never fall back to allow_always when allow_once is absent")
	}
}

// TestHelperFakeAgy simulates `agy acp` when invoked by TestExecuteSampleTaskCapturesStderrAndNotifications.
func TestHelperFakeAgy(t *testing.T) {
	if os.Getenv("S5_FAKE_AGY_HELPER") != "1" {
		return
	}
	// Spawn an orphan child holding stderr open for 15s to verify close() does not hang.
	var sleeper *exec.Cmd
	if runtime.GOOS == "windows" {
		sleeper = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 15")
	} else {
		sleeper = exec.Command("sleep", "15")
	}
	sleeper.Stderr = os.Stderr
	_ = sleeper.Start()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":1}}`+"\n", req.ID)
		case "session/new":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"sess-fake-1"}}`+"\n", req.ID)
		case "session/prompt":
			fmt.Fprintln(os.Stderr, "WARN upstream returned HTTP 429 Too Many Requests (reset in 2h)")
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"quota_warning","detail":"exhausted"}}}`)
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"request failed"}}`+"\n", req.ID)
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func TestExecuteSampleTaskCapturesStderrAndNotificationsWithoutHanging(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	tmp := t.TempDir()
	var wrapperPath string
	if runtime.GOOS == "windows" {
		wrapperPath = filepath.Join(tmp, "fake-agy.cmd")
		body := fmt.Sprintf("@echo off\r\nset S5_FAKE_AGY_HELPER=1\r\n\"%s\" -test.run=^TestHelperFakeAgy$ -- %%*\r\n", selfExe)
		if err := os.WriteFile(wrapperPath, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	} else {
		wrapperPath = filepath.Join(tmp, "fake-agy")
		body := fmt.Sprintf("#!/bin/sh\nexport S5_FAKE_AGY_HELPER=1\nexec \"%s\" -test.run='^TestHelperFakeAgy$' -- \"$@\"\n", selfExe)
		if err := os.WriteFile(wrapperPath, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	t0 := time.Now()
	m := executeSampleTask(
		context.Background(),
		wrapperPath,
		filepath.Join(tmp, "home"),
		filepath.Join(tmp, "work"),
		"unit-test", 1, 1,
	)
	elapsed := time.Since(t0)
	if elapsed > 6*time.Second {
		t.Fatalf("close() hung waiting on grandchild stderr: took %v", elapsed)
	}
	if !m.LimitHit {
		t.Fatalf("expected LimitHit=true from stderr HTTP 429, got false (stderr=%q err=%q)", m.StderrText, m.ErrorText)
	}
	if !strings.Contains(m.StderrText, "HTTP 429 Too Many Requests") {
		t.Fatalf("expected StderrText to be preserved in returned measurement, got %q", m.StderrText)
	}
	if len(m.Notifications) != 1 || !strings.Contains(string(m.Notifications[0]), "quota_warning") {
		t.Fatalf("expected 1 non-chunk notification preserved, got %d (%v)", len(m.Notifications), m.Notifications)
	}
}

package telegram_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/adapter/telegram"
	"max/internal/app"
	"max/internal/platform"
)

type manualClk struct{ now time.Time }

func (c *manualClk) Now() time.Time { return c.now }

func TestTelegramDisabledWithoutConfig(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := telegram.New(telegram.Config{
		APIBase:   srv.URL,
		BotToken:  "",
		OwnerIDs:  nil,
		PublicURL: "https://max.tailnet.ts.net",
	})
	if n.Enabled() {
		t.Fatal("expected Enabled()=false when BotToken/OwnerIDs missing")
	}
	if err := n.Notify(context.Background(), app.Notification{Kind: "approval", Title: "Test"}); err != nil {
		t.Fatalf("Notify when disabled: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("HTTP hits = %d, want 0 when disabled", hits.Load())
	}
}

func TestTelegramNoApprovalButtons(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := telegram.New(telegram.Config{
		APIBase:   srv.URL,
		BotToken:  "bot-token-test",
		OwnerIDs:  []int64{10001},
		PublicURL: "https://max.example.ts.net",
	})
	err := n.Notify(context.Background(), app.Notification{
		ID:      "ap-123",
		Topic:   "inbox",
		Kind:    "approval",
		Title:   "Duyệt gọi tool send_email",
		Body:    "Gửi tới ceo@acme.com",
		Urgency: "high",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("bodies = %d, want 1", len(bodies))
	}
	payload := bodies[0]
	if strings.Contains(payload, "reply_markup") || strings.Contains(payload, "inline_keyboard") || strings.Contains(payload, "callback_data") {
		t.Fatalf("Telegram payload MUST NOT include approval decision buttons: %s", payload)
	}
	if !strings.Contains(payload, "https://max.example.ts.net/inbox") {
		t.Fatalf("expected plain Web UI link in Telegram message, got: %s", payload)
	}
}

func TestTelegramOwnerOnly(t *testing.T) {
	var logBuf bytes.Buffer
	clk := &manualClk{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	n := telegram.New(telegram.Config{
		BotToken:  "bot-token-test",
		OwnerIDs:  []int64{77777},
		PublicURL: "http://127.0.0.1:8787",
		Clock:     clk,
		Logger:    platform.NewLogger(&logBuf, "info"),
	})

	// 5 messages from unknown user IDs within the same minute -> ignored AND <= 1 log line!
	for i := range 5 {
		accepted := n.HandleIncomingUpdate(telegram.Update{
			FromUserID: int64(90000 + i),
			Text:       "/approve ap-1",
		})
		if accepted {
			t.Fatal("expected unknown Telegram user ID to be ignored")
		}
	}

	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	nonEmpty := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
	}
	if nonEmpty > 1 {
		t.Fatalf("logged %d lines within 1 minute for unknown senders, want <= 1:\n%s", nonEmpty, logBuf.String())
	}
}

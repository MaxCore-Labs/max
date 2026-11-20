package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"max/internal/app"
	"max/internal/domain"
)

// ClassifyBrowserTool maps Playwright MCP tool names to R (S1 allowlist) or Ba.
func ClassifyBrowserTool(name string) domain.ToolClass {
	return domain.ClassifyBrowserTool(name)
}

// SanitizeBrowserCall hard-blocks arbitrary code execution tools and strips workspace-writing filename args (S1).
func SanitizeBrowserCall(name string, raw json.RawMessage) (json.RawMessage, error) {
	switch name {
	case "browser_evaluate", "browser_run_code_unsafe":
		return nil, fmt.Errorf("tool %s bị chặn cứng vì lý do bảo mật", name)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" {
		return json.RawMessage(`{}`), nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return raw, nil
	}
	delete(m, "filename")
	if rawURL, ok := m["url"].(string); ok && strings.TrimSpace(rawURL) != "" {
		if allowed, reason := AllowedURL(rawURL); !allowed {
			return nil, fmt.Errorf("URL bị từ chối: %s", reason)
		}
	}
	clean, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return clean, nil
}

// BlockTracker counts consecutive anti-bot / HTTP block events per (run, origin) and escalates after 2 blocks.
type BlockTracker struct {
	store  app.Store
	clock  app.Clock
	ids    app.IDGen
	mu     sync.Mutex
	counts map[string]int
}

// NewBlockTracker constructs a BlockTracker backed by store for owner Inbox alerts.
func NewBlockTracker(store app.Store, clock app.Clock, ids app.IDGen) *BlockTracker {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &BlockTracker{
		store:  store,
		clock:  clock,
		ids:    ids,
		counts: make(map[string]int),
	}
}

// RecordOriginBlock increments the block counter for rawURL's origin in runID; stops and alerts Inbox on 2nd block.
func (b *BlockTracker) RecordOriginBlock(ctx context.Context, agentID, runID domain.ID, rawURL string) (stop bool, message string, err error) {
	origin := extractOrigin(rawURL)
	if origin == "" {
		return false, "", errors.New("invalid origin URL")
	}
	key := string(runID) + "|" + origin
	b.mu.Lock()
	b.counts[key]++
	n := b.counts[key]
	b.mu.Unlock()

	if n < 2 {
		return false, fmt.Sprintf("trang %s bị chặn lần %d/2", origin, n), nil
	}

	nowMs := b.clock.Now().UnixMilli()
	refKind := "run"
	refID := runID
	agID := agentID
	dedupe := fmt.Sprintf("browser-blocked:%s:%s", runID, origin)
	err = b.store.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:        b.ids.New(),
			Kind:      "error",
			Title:     fmt.Sprintf("Trang %s chặn trình duyệt 2 lần", origin),
			Body:      fmt.Sprintf("Run %s bị chặn 2 lần liên tiếp tại %s; đã dừng tự động.", runID, origin),
			RefKind:   &refKind,
			RefID:     &refID,
			AgentID:   &agID,
			Urgency:   "normal",
			DedupeKey: &dedupe,
			CreatedAt: nowMs,
		})
	})
	return true, fmt.Sprintf("dừng, báo chủ nhân: %s đã chặn 2 lần liên tiếp", origin), err
}

func extractOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

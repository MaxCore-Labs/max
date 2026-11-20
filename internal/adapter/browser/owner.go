package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

// ValidateOwnerEngine enforces that browser: owner requires a Level-1 engine unless allow_ungated_engine is set (Spec 8.4.5 & P4.02).
func ValidateOwnerEngine(level int, allowUngated bool) error {
	if level > 1 && !allowUngated {
		return fmt.Errorf("browser: owner requires a Level-1 engine (got level %d without allow_ungated_engine)", level)
	}
	return nil
}

// CheckOwnerAction inspects a browser tool call on the owner profile and denies automated password typing in favor of handoff.
func CheckOwnerAction(toolName string, input json.RawMessage) domain.Decision {
	if domain.IsOwnerPasswordAction(toolName, input) {
		return domain.Decision{
			Verdict: domain.Deny,
			Reason:  "không gõ mật khẩu tự động trên profile owner; hãy gọi tool handoff(reason, origin) để chủ nhân tiếp quản",
		}
	}
	return domain.Decision{
		Verdict: domain.Ask,
		Reason:  "thao tác trên profile trình duyệt của chủ nhân cần duyệt hoặc grant khớp",
	}
}

// OwnerGuard coordinates human browser takeover handoff and Inbox alerts for owner browser profiles.
type OwnerGuard struct {
	store    app.Store
	takeover *Takeover
	clock    app.Clock
	ids      app.IDGen
}

// NewOwnerGuard constructs an OwnerGuard implementing app.BrowserHandoff.
func NewOwnerGuard(store app.Store, takeover *Takeover, clock app.Clock, ids app.IDGen) *OwnerGuard {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &OwnerGuard{
		store:    store,
		takeover: takeover,
		clock:    clock,
		ids:      ids,
	}
}

// Handoff starts the loopback takeover listener, sets conversation.waiting_for={'kind':'owner',...} so the run parks,
// and posts a high-urgency owner Inbox item containing the loopback takeover URL.
func (g *OwnerGuard) Handoff(ctx context.Context, agentID, runID, convID domain.ID, reason, origin string) (string, error) {
	if g.takeover == nil {
		return "", fmt.Errorf("takeover service unavailable")
	}
	tkURL, err := g.takeover.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("start takeover: %w", err)
	}

	cleanReason := platform.Redact(strings.TrimSpace(reason))
	if cleanReason == "" {
		cleanReason = "Cần chủ nhân tiếp quản phiên trình duyệt"
	}
	cleanOrigin := strings.TrimSpace(origin)

	nowMs := g.clock.Now().UnixMilli()
	itemID := g.ids.New()
	refKind := "run"
	refID := runID
	cID := convID
	agID := agentID

	wfPayload, _ := json.Marshal(map[string]string{
		"kind":     "owner",
		"subkind":  "takeover",
		"inbox_id": itemID,
		"origin":   cleanOrigin,
		"url":      tkURL,
	})
	wfStr := string(wfPayload)

	body := fmt.Sprintf("%s\nOrigin: %s\nLink tiếp quản loopback: %s", cleanReason, cleanOrigin, tkURL)
	err = g.store.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.SetConversationWaitingFor(ctx, convID, &wfStr); err != nil {
			return err
		}
		if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:             itemID,
			Kind:           "takeover",
			Title:          fmt.Sprintf("Tiếp quản trình duyệt: %s", cleanReason),
			Body:           body,
			RefKind:        &refKind,
			RefID:          &refID,
			ConversationID: &cID,
			AgentID:        &agID,
			Urgency:        "high",
			CreatedAt:      nowMs,
		}); err != nil {
			return err
		}
		outPayload, _ := json.Marshal(map[string]string{"inbox_id": itemID, "kind": "takeover", "url": tkURL})
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   string(outPayload),
			Urgency:       "high",
			NextAttemptAt: nowMs,
		})
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Đã mở phiên tiếp quản cho chủ nhân tại %s (origin=%s). Hãy kết thúc lượt ngay để chờ chủ nhân hoàn tất.", tkURL, cleanOrigin), nil
}

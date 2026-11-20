package approvals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/platform"
)

const (
	approvalTTL     = 24 * time.Hour
	maxPreviewBytes = 600
)

// ErrAlreadyDecided is returned when Decide is called on a resolved approval with a different decisionKey.
var ErrAlreadyDecided = errors.New("yêu cầu duyệt đã quyết định (already decided)")

// Request describes a tool call requiring owner approval before execution.
type Request struct {
	Caller   tools.Caller
	ToolName string
	Input    json.RawMessage
	Preview  string
	Reason   string
	ByMax    bool
}

// Executor executes an approved Max/gateway tool call outside DB transactions when ByMax is true.
type Executor func(ctx context.Context, c tools.Caller, tool string, input json.RawMessage) tools.Result

// Service manages stop-and-resume approvals, idempotent owner decisions, and 24-hour expiry (Spec 8.6).
type Service struct {
	store    app.Store
	actors   *actor.Service
	executor Executor
	clock    app.Clock
	ids      app.IDGen
}

// NewService constructs an approvals Service.
func NewService(store app.Store, actors *actor.Service, executor Executor, clock app.Clock, ids app.IDGen) *Service {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Service{
		store:    store,
		actors:   actors,
		executor: executor,
		clock:    clock,
		ids:      ids,
	}
}

// HashInput computes a canonical SHA-256 hex digest of raw JSON tool arguments.
func HashInput(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = "{}"
	}
	var canonical []byte
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		if b, merr := json.Marshal(v); merr == nil {
			canonical = b
		}
	}
	if len(canonical) == 0 {
		canonical = []byte(trimmed)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// Ask creates (or deduplicates) a pending approval, marks conversations.waiting_for='approval', and enqueues Inbox/outbox items.
func (s *Service) Ask(ctx context.Context, r Request) (approvalID domain.ID, alreadyPending bool, err error) {
	now := s.clock.Now()
	nowMs := now.UnixMilli()
	expMs := now.Add(approvalTTL).UnixMilli()

	rawInput := strings.TrimSpace(string(r.Input))
	if rawInput == "" {
		rawInput = "{}"
	}
	inputHash := HashInput(json.RawMessage(rawInput))

	rawPreview := r.Preview
	if strings.TrimSpace(rawPreview) == "" {
		rawPreview = rawInput
	}
	preview := truncateUTF8(platform.Redact(rawPreview), maxPreviewBytes)
	reason := strings.TrimSpace(r.Reason)
	if reason == "" {
		reason = "owner approval required"
	}

	err = s.store.WithTx(ctx, func(tx app.Tx) error {
		existing, found, ferr := tx.FindPendingApproval(ctx, r.Caller.AgentID, r.ToolName, inputHash)
		if ferr != nil {
			return ferr
		}
		if found {
			approvalID = existing.ID
			alreadyPending = true
			return nil
		}

		newID := s.ids.New()
		approvalID = newID
		if err := tx.InsertApproval(ctx, app.ApprovalRecord{
			ID:             newID,
			RunID:          r.Caller.RunID,
			AgentID:        r.Caller.AgentID,
			ConversationID: r.Caller.ConversationID,
			ToolName:       r.ToolName,
			Input:          rawInput,
			InputHash:      inputHash,
			Preview:        preview,
			Reason:         reason,
			ByMax:          r.ByMax,
			Status:         "pending",
			RequestedAt:    nowMs,
			ExpiresAt:      expMs,
		}); err != nil {
			return err
		}

		wfBytes, _ := json.Marshal(map[string]string{"kind": "approval", "approval_id": newID})
		wf := string(wfBytes)
		if err := tx.SetConversationWaitingFor(ctx, r.Caller.ConversationID, &wf); err != nil {
			return err
		}

		refKind := "approval"
		refID := newID
		convID := r.Caller.ConversationID
		agID := r.Caller.AgentID
		dedupe := "approval:" + newID
		if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:             s.ids.New(),
			Kind:           "approval",
			Title:          fmt.Sprintf("Duyệt gọi tool %s", r.ToolName),
			Body:           preview,
			RefKind:        &refKind,
			RefID:          &refID,
			ConversationID: &convID,
			AgentID:        &agID,
			Urgency:        "high",
			DedupeKey:      &dedupe,
			CreatedAt:      nowMs,
		}); err != nil {
			return err
		}

		outPayload, _ := json.Marshal(map[string]string{"approval_id": newID, "kind": "approval"})
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   string(outPayload),
			DedupeKey:     &dedupe,
			Urgency:       "high",
			NextAttemptAt: nowMs,
		})
	})
	return approvalID, alreadyPending, err
}

// Decide applies an owner allow/deny decision idempotently, executes ByMax tools at most once outside SQLite Tx, and wakes the agent.
func (s *Service) Decide(ctx context.Context, approvalID domain.ID, allow bool, decisionKey string) error {
	nowMs := s.clock.Now().UnixMilli()
	var (
		ap         app.ApprovalRecord
		shouldExec bool
		idempotent bool
		decKeyPtr  *string
	)
	if strings.TrimSpace(decisionKey) != "" {
		k := strings.TrimSpace(decisionKey)
		decKeyPtr = &k
	}

	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		foundAp, ok, err := tx.GetApproval(ctx, approvalID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("approval %q not found", approvalID)
		}
		ap = foundAp

		if ap.Status != "pending" {
			if decKeyPtr != nil && ap.DecisionKey != nil && *ap.DecisionKey == *decKeyPtr {
				idempotent = true
				return nil
			}
			return ErrAlreadyDecided
		}

		decidedAt := nowMs
		if !allow {
			if err := tx.UpdateApprovalDecision(ctx, approvalID, "denied", decKeyPtr, &decidedAt); err != nil {
				return err
			}
			if err := tx.SetConversationWaitingFor(ctx, ap.ConversationID, nil); err != nil {
				return err
			}
			if err := tx.ResolveInboxByRef(ctx, "approval", approvalID, nowMs); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{
				"approval_id": approvalID,
				"tool":        ap.ToolName,
				"status":      "denied",
				"reason":      ap.Reason,
			})
			convID := ap.ConversationID
			_, _, err = s.actors.Wake(ctx, tx, actor.WakeInput{
				AgentID:        ap.AgentID,
				Kind:           domain.WakeApprovalResult,
				Priority:       domain.PrioTask,
				ConversationID: &convID,
				Payload:        payload,
			})
			return err
		}

		if err := tx.UpdateApprovalDecision(ctx, approvalID, "allowed", decKeyPtr, &decidedAt); err != nil {
			return err
		}
		if err := tx.SetConversationWaitingFor(ctx, ap.ConversationID, nil); err != nil {
			return err
		}
		if err := tx.ResolveInboxByRef(ctx, "approval", approvalID, nowMs); err != nil {
			return err
		}

		if ap.ByMax {
			shouldExec = true
			return nil
		}

		payload, _ := json.Marshal(map[string]any{
			"approval_id": approvalID,
			"tool":        ap.ToolName,
			"status":      "allowed",
		})
		convID := ap.ConversationID
		_, _, err = s.actors.Wake(ctx, tx, actor.WakeInput{
			AgentID:        ap.AgentID,
			Kind:           domain.WakeApprovalResult,
			Priority:       domain.PrioTask,
			ConversationID: &convID,
			Payload:        payload,
		})
		return err
	})
	if err != nil || idempotent || !shouldExec {
		return err
	}

	var execRes tools.Result
	if s.executor != nil {
		caller := tools.Caller{
			RunID:          ap.RunID,
			AgentID:        ap.AgentID,
			ConversationID: ap.ConversationID,
		}
		execRes = s.executor(ctx, caller, ap.ToolName, json.RawMessage(ap.Input))
	}

	return s.store.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.UpdateApprovalDecision(ctx, approvalID, "consumed", nil, nil); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"approval_id": approvalID,
			"tool":        ap.ToolName,
			"status":      "allowed",
			"executed":    true,
			"result":      platform.Redact(execRes.Text),
			"is_error":    execRes.IsError,
		})
		convID := ap.ConversationID
		_, _, err := s.actors.Wake(ctx, tx, actor.WakeInput{
			AgentID:        ap.AgentID,
			Kind:           domain.WakeApprovalResult,
			Priority:       domain.PrioTask,
			ConversationID: &convID,
			Payload:        payload,
		})
		return err
	})
}

// ConsumeOnce atomically consumes one allowed non-ByMax approval matching (agentID, tool, inputHash).
func (s *Service) ConsumeOnce(ctx context.Context, tx app.Tx, agentID domain.ID, tool, inputHash string) (bool, error) {
	return tx.ConsumeAllowedApproval(ctx, agentID, tool, inputHash, s.clock.Now().UnixMilli())
}

// ExpireDue marks pending approvals past expires_at as expired and wakes their conversations.
func (s *Service) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	nowMs := now.UnixMilli()
	var count int
	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		due, err := tx.ListExpiredPendingApprovals(ctx, nowMs)
		if err != nil {
			return err
		}
		for _, ap := range due {
			decidedAt := nowMs
			if err := tx.UpdateApprovalDecision(ctx, ap.ID, "expired", nil, &decidedAt); err != nil {
				return err
			}
			if err := tx.SetConversationWaitingFor(ctx, ap.ConversationID, nil); err != nil {
				return err
			}
			if err := tx.ResolveInboxByRef(ctx, "approval", ap.ID, nowMs); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{
				"approval_id": ap.ID,
				"tool":        ap.ToolName,
				"status":      "expired",
				"reason":      "quá 24 giờ không ai duyệt",
			})
			convID := ap.ConversationID
			if _, _, err := s.actors.Wake(ctx, tx, actor.WakeInput{
				AgentID:        ap.AgentID,
				Kind:           domain.WakeApprovalResult,
				Priority:       domain.PrioTask,
				ConversationID: &convID,
				Payload:        payload,
			}); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		rl := utf8.RuneLen(r)
		if b.Len()+rl > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

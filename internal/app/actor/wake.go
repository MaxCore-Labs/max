package actor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"max/internal/app"
	"max/internal/domain"
)

const mailboxHardCap = 200

// ErrMailboxFull is returned when an agent mailbox already has 200 queued wakeups and a new coalescible wakeup cannot merge.
var ErrMailboxFull = errors.New("mailbox full")

// WakeInput carries reference metadata for waking an agent inside an active transaction (Spec 8.2.1).
type WakeInput struct {
	AgentID        domain.ID
	Kind           domain.WakeKind
	Priority       domain.Priority
	ConversationID *domain.ID
	Payload        json.RawMessage
	IdempotencyKey *string
	NotBefore      time.Time
	CauseRunID     *domain.ID
	Hops           int
}

// Wakeup is the drained wakeup representation handed to a claimed run.
type Wakeup = app.Wakeup

// Service manages durable agent mailboxes, fair claiming, and crash recovery.
type Service struct {
	store     app.Store
	clock     app.Clock
	ids       app.IDGen
	notify    chan struct{}
	lastStamp atomic.Int64
}

// NewService constructs an actor Service.
func NewService(store app.Store, clock app.Clock, ids app.IDGen) *Service {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Service{
		store:  store,
		clock:  clock,
		ids:    ids,
		notify: make(chan struct{}, 1),
	}
}

// Signal returns a channel notified whenever Wake inserts or coalesces work.
func (s *Service) Signal() <-chan struct{} {
	return s.notify
}

func (s *Service) ping() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Wake inserts or coalesces a wakeup inside tx and recomputes the agent's derived ready state.
func (s *Service) Wake(ctx context.Context, tx app.Tx, in WakeInput) (id domain.ID, coalesced bool, err error) {
	if strings.TrimSpace(in.AgentID) == "" {
		return "", false, errors.New("wake requires non-empty agent_id")
	}
	now := s.clock.Now()
	nowMs := now.UnixMilli()

	notBeforeMs := int64(0)
	if !in.NotBefore.IsZero() && in.NotBefore.After(now) {
		notBeforeMs = in.NotBefore.UnixMilli()
	}

	payloadStr := strings.TrimSpace(string(in.Payload))
	if payloadStr == "" {
		payloadStr = "{}"
	}

	if in.IdempotencyKey != nil && strings.TrimSpace(*in.IdempotencyKey) != "" {
		existingID, found, err := tx.FindWakeupByIdemKey(ctx, *in.IdempotencyKey)
		if err != nil {
			return "", false, err
		}
		if found {
			return existingID, true, nil
		}
	}

	if in.Kind.Coalescible() {
		existingID, merged, err := tx.CoalesceQueuedWakeup(
			ctx, in.AgentID, in.ConversationID, in.Kind, in.Priority, payloadStr, notBeforeMs,
		)
		if err != nil {
			return "", false, err
		}
		if merged {
			if err := tx.RecomputeAgentReady(ctx, in.AgentID, nowMs); err != nil {
				return "", false, fmt.Errorf("recompute ready after coalesce: %w", err)
			}
			s.ping()
			return existingID, true, nil
		}

		queuedCount, err := tx.CountQueuedWakeups(ctx, in.AgentID)
		if err != nil {
			return "", false, err
		}
		if queuedCount >= mailboxHardCap {
			return "", false, ErrMailboxFull
		}
	}

	newID := s.ids.New()
	rec := app.WakeupRecord{
		ID:             newID,
		AgentID:        in.AgentID,
		ConversationID: in.ConversationID,
		Kind:           in.Kind,
		Priority:       in.Priority,
		Payload:        payloadStr,
		IdempotencyKey: in.IdempotencyKey,
		Status:         "queued",
		CauseRunID:     in.CauseRunID,
		Hops:           in.Hops,
		NotBefore:      notBeforeMs,
		CreatedAt:      nowMs,
	}
	if err := tx.InsertWakeupRaw(ctx, rec); err != nil {
		return "", false, err
	}
	if err := tx.RecomputeAgentReady(ctx, in.AgentID, nowMs); err != nil {
		return "", false, fmt.Errorf("recompute ready after wake: %w", err)
	}
	s.ping()
	return newID, false, nil
}

// RecomputeReady recalculates agents.{ready, ready_priority, ready_since} inside tx at now.
func (s *Service) RecomputeReady(ctx context.Context, tx app.Tx, agentID domain.ID, now time.Time) error {
	return tx.RecomputeAgentReady(ctx, agentID, now.UnixMilli())
}

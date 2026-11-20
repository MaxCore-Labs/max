package scheduler

import (
	"context"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/approvals"
)

const (
	wakeupRetention = 30 * 24 * time.Hour
	defaultInterval = 30 * time.Second
)

// Scheduler executes periodic internal housekeeping ticks (due wakeups, approval expiry, counter reconciliation, retention).
type Scheduler struct {
	store     app.Store
	actors    *actor.Service
	approvals *approvals.Service
	notify    func()

	mu          sync.Mutex
	maxObserved time.Time
}

// New constructs an internal Scheduler.
func New(store app.Store, actors *actor.Service, approvalsSvc *approvals.Service, notify func()) *Scheduler {
	return &Scheduler{
		store:     store,
		actors:    actors,
		approvals: approvalsSvc,
		notify:    notify,
	}
}

// Run executes Tick every 30 seconds until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context, clock app.Clock) error {
	if clock == nil {
		clock = app.SystemClock{}
	}
	ticker := time.NewTicker(defaultInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_ = s.Tick(ctx, clock.Now())
		}
	}
}

// Tick performs one idempotent housekeeping pass safe under both forward and backward system clock jumps.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	clockWentBackward := !s.maxObserved.IsZero() && now.Before(s.maxObserved)
	if now.After(s.maxObserved) {
		s.maxObserved = now
	}
	s.mu.Unlock()

	nowMs := now.UnixMilli()
	if nowMs < 0 {
		nowMs = 0
	}

	var promoted int
	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		var pErr error
		promoted, pErr = tx.PromoteDueWakeups(ctx, nowMs)
		if pErr != nil {
			return pErr
		}
		if err := tx.ReconcileTeamRunning(ctx); err != nil {
			return err
		}
		// Only prune retention when clock did not jump backwards so backwards jumps cannot miscompute cutoffs.
		if !clockWentBackward {
			cutoffMs := now.Add(-wakeupRetention).UnixMilli()
			if _, err := tx.PruneTerminalWakeupsBefore(ctx, cutoffMs, 1000); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	var expired int
	if s.approvals != nil && !clockWentBackward {
		expired, err = s.approvals.ExpireDue(ctx, now)
		if err != nil {
			return err
		}
	}

	if (promoted > 0 || expired > 0) && s.notify != nil {
		s.notify()
	}
	return nil
}

package limits

import (
	"context"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/domain"
)

const hoursPerWeek = 24 * 7

// QuotaConfig configures rolling window durations, background share, initial ceilings, and timezone.
type QuotaConfig struct {
	ShortHours      int
	BackgroundShare float64
	InitEstShort    *int64
	InitEstWeek     *int64
	TZ              string
}

// Quota manages rolling subscription usage ledgers and learned rate-limit ceilings per engine (Spec 8.8.2).
type Quota struct {
	store app.Store
	clock app.Clock

	mu  sync.RWMutex
	cfg QuotaConfig
	loc *time.Location
}

// NewQuota constructs a Quota manager.
func NewQuota(store app.Store, clock app.Clock, cfg QuotaConfig) *Quota {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if cfg.ShortHours <= 0 {
		cfg.ShortHours = 5
	}
	if cfg.BackgroundShare <= 0 || cfg.BackgroundShare > 1 {
		cfg.BackgroundShare = 0.7
	}
	loc := time.UTC
	if cfg.TZ != "" {
		if l, err := time.LoadLocation(cfg.TZ); err == nil {
			loc = l
		}
	}
	return &Quota{
		store: store,
		clock: clock,
		cfg:   cfg,
		loc:   loc,
	}
}

// UpdateSettings updates quota window parameters at runtime (used by updateQuotaSettings).
func (q *Quota) UpdateSettings(shortHours int, bgShare float64, estShort, estWeek *int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if shortHours > 0 {
		q.cfg.ShortHours = shortHours
	}
	if bgShare > 0 && bgShare <= 1 {
		q.cfg.BackgroundShare = bgShare
	}
	if estShort != nil {
		q.cfg.InitEstShort = estShort
	}
	if estWeek != nil {
		q.cfg.InitEstWeek = estWeek
	}
}

// ConfigSnapshot returns a copy of current QuotaConfig.
func (q *Quota) ConfigSnapshot() QuotaConfig {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.cfg
}

// View reads current short/week window usage and learned ceilings for engine e at now.
func (q *Quota) View(ctx context.Context, e domain.EngineName, now time.Time) (domain.QuotaView, error) {
	q.mu.RLock()
	shortHours := q.cfg.ShortHours
	initShort := q.cfg.InitEstShort
	initWeek := q.cfg.InitEstWeek
	q.mu.RUnlock()

	shortStart := domain.WindowStart(now, shortHours).UnixMilli()
	weekStart := domain.WindowStart(now, hoursPerWeek).UnixMilli()

	usedShort, _, err := q.store.Read().GetQuotaWindowTokens(ctx, e, "short", shortStart)
	if err != nil {
		return domain.QuotaView{}, err
	}
	usedWeek, _, err := q.store.Read().GetQuotaWindowTokens(ctx, e, "week", weekStart)
	if err != nil {
		return domain.QuotaView{}, err
	}

	view := domain.QuotaView{
		UsedShort: usedShort,
		UsedWeek:  usedWeek,
		EstShort:  initShort,
		EstWeek:   initWeek,
	}

	qs, ok, err := q.store.Read().GetQuotaState(ctx, e)
	if err != nil {
		return domain.QuotaView{}, err
	}
	if ok {
		if qs.EstShortTokens != nil {
			view.EstShort = qs.EstShortTokens
		}
		if qs.EstWeekTokens != nil {
			view.EstWeek = qs.EstWeekTokens
		}
		if qs.LimitedUntil != nil {
			until := time.UnixMilli(*qs.LimitedUntil).UTC()
			view.LimitedUntil = &until
		}
	}
	return view, nil
}

// Record increments rolling short/week quota_ledger windows and usage_daily by (in + out) tokens inside tx.
func (q *Quota) Record(ctx context.Context, tx app.Tx, e domain.EngineName, agentID domain.ID, in, out int64, now time.Time) error {
	q.mu.RLock()
	shortHours := q.cfg.ShortHours
	loc := q.loc
	q.mu.RUnlock()

	total := max(0, in) + max(0, out)
	shortStart := domain.WindowStart(now, shortHours).UnixMilli()
	weekStart := domain.WindowStart(now, hoursPerWeek).UnixMilli()

	if err := tx.AddQuotaLedgerUsage(ctx, e, "short", shortStart, total); err != nil {
		return err
	}
	if err := tx.AddQuotaLedgerUsage(ctx, e, "week", weekStart, total); err != nil {
		return err
	}

	teamID := "unknown"
	if ag, ok, err := tx.GetAgent(ctx, agentID); err == nil && ok {
		teamID = ag.TeamID
	}
	day := now.In(loc).Format("2006-01-02")
	return tx.AddDailyUsage(ctx, day, agentID, teamID, e, total)
}

// OnLimit learns EstShort from current window usage and sets LimitedUntil for engine e.
func (q *Quota) OnLimit(ctx context.Context, e domain.EngineName, resetAt *time.Time, now time.Time) error {
	cur, err := q.View(ctx, e, now)
	if err != nil {
		return err
	}
	learned := domain.LearnLimit(cur, resetAt, now)
	nowMs := now.UnixMilli()

	return q.store.WithTx(ctx, func(tx app.Tx) error {
		prev, ok, err := tx.GetQuotaState(ctx, e)
		if err != nil {
			return err
		}
		attempts := 0
		if ok {
			attempts = prev.ProbeAttempts
		}
		var untilMs *int64
		if resetAt != nil && resetAt.After(now) {
			u := resetAt.UnixMilli()
			untilMs = &u
		} else {
			u := now.Add(domain.NextProbe(attempts)).UnixMilli()
			untilMs = &u
		}
		return tx.SaveLearnedQuotaState(ctx, app.QuotaStateRecord{
			Engine:         e,
			EstShortTokens: learned.EstShort,
			EstWeekTokens:  learned.EstWeek,
			LimitedUntil:   untilMs,
			LastLimitAt:    &nowMs,
			ProbeAttempts:  attempts + 1,
		})
	})
}

// Admit evaluates subscription admission for priority p on engine e (usable directly as actor.Capacity.Admit).
func (q *Quota) Admit(e domain.EngineName, p domain.Priority) domain.Admission {
	now := q.clock.Now()
	q.mu.RLock()
	bgShare := q.cfg.BackgroundShare
	q.mu.RUnlock()

	view, err := q.View(context.Background(), e, now)
	if err != nil {
		return domain.AdmitNow
	}
	return domain.Admit(view, p, bgShare, now)
}

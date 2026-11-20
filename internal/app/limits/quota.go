package limits

import (
	"context"
	"fmt"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/domain"
)

const (
	hoursPerWeek        = 24 * 7
	maxAgentRunsPerHour = 12
)

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
	ids   app.IDGen

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
		ids:   app.UUIDv7Gen{},
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
	var ag app.AgentRecord
	var hasAg bool
	if got, ok, err := tx.GetAgent(ctx, agentID); err == nil && ok {
		ag = got
		hasAg = true
		teamID = got.TeamID
	}
	day := now.In(loc).Format("2006-01-02")
	if err := tx.AddDailyUsage(ctx, day, agentID, teamID, e, total); err != nil {
		return err
	}
	if hasAg && ag.QuotaTokensDay != nil && *ag.QuotaTokensDay > 0 {
		used, err := tx.GetAgentDailyTokens(ctx, day, agentID)
		if err == nil && used >= *ag.QuotaTokensDay {
			if err := tx.SetAgentStatus(ctx, agentID, domain.AgentQuotaPaused); err != nil {
				return err
			}
			nowMs := now.UnixMilli()
			refKind := "agent"
			refID := agentID
			dedupe := fmt.Sprintf("quota_paused:%s:%s", agentID, day)
			if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
				ID:        q.ids.New(),
				Kind:      "notification",
				Title:     fmt.Sprintf("Agent %s chạm trần token ngày", ag.Slug),
				Body:      fmt.Sprintf("Đã dùng %d / %d token trong ngày %s; tạm dừng tới ngày mới.", used, *ag.QuotaTokensDay, day),
				RefKind:   &refKind,
				RefID:     &refID,
				AgentID:   &refID,
				Urgency:   "normal",
				DedupeKey: &dedupe,
				CreatedAt: nowMs,
			}); err != nil {
				return err
			}
		}
	}
	return nil
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

// AgentAllowed evaluates read-only per-agent hourly run rate and per-agent/per-team daily token caps.
func (q *Quota) AgentAllowed(ctx context.Context, agentID, teamID domain.ID, now time.Time) (bool, string) {
	q.mu.RLock()
	loc := q.loc
	q.mu.RUnlock()

	rd := q.store.Read()
	day := now.In(loc).Format("2006-01-02")

	ag, ok, err := rd.GetAgent(ctx, agentID)
	if err != nil || !ok {
		return false, "agent_not_found"
	}
	if ag.Status == domain.AgentQuotaPaused {
		if ag.QuotaTokensDay != nil && *ag.QuotaTokensDay > 0 {
			used, _ := rd.GetAgentDailyTokens(ctx, day, agentID)
			if used >= *ag.QuotaTokensDay {
				return false, "agent_daily_token_cap"
			}
		}
		return false, "agent_quota_paused"
	}
	if ag.Status != domain.AgentActive {
		return false, "agent_inactive"
	}

	if ag.QuotaTokensDay != nil && *ag.QuotaTokensDay > 0 {
		used, err := rd.GetAgentDailyTokens(ctx, day, agentID)
		if err == nil && used >= *ag.QuotaTokensDay {
			return false, "agent_daily_token_cap"
		}
	}

	if teamID == "" {
		teamID = ag.TeamID
	}
	if teamID != "" {
		if tm, tok, terr := rd.GetTeam(ctx, teamID); terr == nil && tok && tm.QuotaTokensDay != nil && *tm.QuotaTokensDay > 0 {
			teamUsed, uerr := rd.GetTeamDailyTokens(ctx, day, teamID)
			if uerr == nil && teamUsed >= *tm.QuotaTokensDay {
				return false, "team_daily_token_cap"
			}
		}
	}

	runsLastHour, err := rd.CountAgentRunsSince(ctx, agentID, now.Add(-time.Hour).UnixMilli())
	if err == nil && runsLastHour >= maxAgentRunsPerHour {
		return false, "runs_per_hour_cap"
	}
	return true, ""
}

// RefreshDailyQuotas reactivates quota_paused agents once a new calendar day begins in MAX_TZ.
func (q *Quota) RefreshDailyQuotas(ctx context.Context, now time.Time) error {
	q.mu.RLock()
	loc := q.loc
	q.mu.RUnlock()

	day := now.In(loc).Format("2006-01-02")
	paused, err := q.store.Read().ListQuotaPausedAgents(ctx)
	if err != nil || len(paused) == 0 {
		return err
	}

	nowMs := now.UnixMilli()
	return q.store.WithTx(ctx, func(tx app.Tx) error {
		for _, ag := range paused {
			if ag.QuotaTokensDay != nil && *ag.QuotaTokensDay > 0 {
				used, err := tx.GetAgentDailyTokens(ctx, day, ag.ID)
				if err != nil || used >= *ag.QuotaTokensDay {
					continue
				}
			}
			if err := tx.SetAgentStatus(ctx, ag.ID, domain.AgentActive); err != nil {
				return err
			}
			if err := tx.RecomputeAgentReady(ctx, ag.ID, nowMs); err != nil {
				return err
			}
		}
		return nil
	})
}

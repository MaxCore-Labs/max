package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/platform"
)

const (
	maxOutboxAttempts  = 10
	defaultPollEvery   = 2 * time.Second
	baseRetryBackoff   = 5 * time.Second
	maxRetryBackoffDur = 30 * time.Minute
)

// Config configures the transactional Outbox Dispatcher.
type Config struct {
	Store      app.Store
	Notifier   app.Notifier
	Clock      app.Clock
	TZ         string
	QuietHours string // e.g. "22:00-07:00"
	PublicURL  string
	Interval   time.Duration
	Logger     *slog.Logger
}

// Dispatcher drains pending rows from the SQLite outbox with quiet-hour deferral and exponential retry backoff.
type Dispatcher struct {
	cfg Config
	loc *time.Location
}

// NewDispatcher constructs an Outbox Dispatcher.
func NewDispatcher(cfg Config) *Dispatcher {
	if cfg.Clock == nil {
		cfg.Clock = app.SystemClock{}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultPollEvery
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	loc := time.UTC
	if cfg.TZ != "" {
		if l, err := time.LoadLocation(cfg.TZ); err == nil {
			loc = l
		}
	}
	return &Dispatcher{cfg: cfg, loc: loc}
}

// Run polls and dispatches due outbox rows until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	_ = d.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_ = d.Tick(ctx)
		}
	}
}

// Tick executes one pass over due pending outbox items.
func (d *Dispatcher) Tick(ctx context.Context) error {
	rows, err := d.cfg.Store.Read().ListOutboxPending(ctx, 50)
	if err != nil {
		return err
	}
	now := d.cfg.Clock.Now()
	nowMs := now.UnixMilli()

	for _, rec := range rows {
		if rec.Topic == "memory.remember" || rec.NextAttemptAt > nowMs {
			continue
		}
		n := d.toNotification(rec)
		if !isBypassQuietHours(n) {
			if inQuiet, wakeAt := checkQuietHours(now.In(d.loc), d.cfg.QuietHours); inQuiet {
				_ = d.cfg.Store.WithTx(ctx, func(tx app.Tx) error {
					return tx.DeferOutboxUntil(ctx, rec.ID, wakeAt.UnixMilli())
				})
				continue
			}
		}

		var sendErr error
		if d.cfg.Notifier != nil {
			sendErr = d.cfg.Notifier.Notify(ctx, n)
		}
		if sendErr == nil {
			if err := d.cfg.Store.WithTx(ctx, func(tx app.Tx) error {
				return tx.MarkOutboxSent(ctx, rec.ID, nowMs)
			}); err != nil {
				return err
			}
			continue
		}

		nextAttempts := rec.Attempts + 1
		backoff := retryDelay(nextAttempts)
		nextAt := now.Add(backoff).UnixMilli()
		var dropAt *int64
		if nextAttempts >= maxOutboxAttempts {
			dropAt = &nowMs
			d.cfg.Logger.Error("outbox max attempts reached; dropped notification",
				"outbox_id", rec.ID,
				"topic", rec.Topic,
				"attempts", nextAttempts,
				"err", platform.Redact(sendErr.Error()),
			)
		}
		if err := d.cfg.Store.WithTx(ctx, func(tx app.Tx) error {
			return tx.RecordOutboxFailure(ctx, rec.ID, nextAttempts, nextAt, dropAt)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) toNotification(rec app.OutboxRecord) app.Notification {
	var m map[string]string
	_ = json.Unmarshal([]byte(rec.PayloadJSON), &m)
	kind := m["kind"]
	if kind == "" {
		kind = rec.Topic
	}
	title := m["title"]
	if title == "" {
		title = fmt.Sprintf("Thông báo %s", kind)
	}
	link := ""
	if strings.TrimSpace(d.cfg.PublicURL) != "" {
		link = strings.TrimRight(d.cfg.PublicURL, "/") + "/inbox"
	}
	return app.Notification{
		ID:      strconv.FormatInt(rec.ID, 10),
		Topic:   rec.Topic,
		Kind:    kind,
		Title:   platform.Redact(title),
		Body:    platform.Redact(m["body"]),
		Urgency: rec.Urgency,
		LinkURL: link,
	}
}

func isBypassQuietHours(n app.Notification) bool {
	if n.Urgency == "high" {
		return true
	}
	switch n.Kind {
	case "approval", "error", "ambiguous":
		return true
	default:
		return false
	}
}

func retryDelay(attempt int) time.Duration {
	d := baseRetryBackoff
	for i := 1; i < attempt; i++ {
		if d >= maxRetryBackoffDur/2 {
			return maxRetryBackoffDur
		}
		d *= 2
	}
	return min(d, maxRetryBackoffDur)
}

func checkQuietHours(localNow time.Time, spec string) (bool, time.Time) {
	startStr, endStr, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return false, time.Time{}
	}
	sh, sm, ok1 := parseHHMM(startStr)
	eh, em, ok2 := parseHHMM(endStr)
	if !ok1 || !ok2 {
		return false, time.Time{}
	}
	curMin := localNow.Hour()*60 + localNow.Minute()
	startMin := sh*60 + sm
	endMin := eh*60 + em

	y, mo, day := localNow.Date()
	loc := localNow.Location()
	if startMin < endMin {
		if curMin >= startMin && curMin < endMin {
			return true, time.Date(y, mo, day, eh, em, 0, 0, loc)
		}
		return false, time.Time{}
	}
	// Overnight window (e.g. 22:00-07:00)
	if curMin >= startMin {
		tomorrow := localNow.AddDate(0, 0, 1)
		ty, tmo, td := tomorrow.Date()
		return true, time.Date(ty, tmo, td, eh, em, 0, 0, loc)
	}
	if curMin < endMin {
		return true, time.Date(y, mo, day, eh, em, 0, 0, loc)
	}
	return false, time.Time{}
}

func parseHHMM(s string) (int, int, bool) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, 0, false
	}
	h, e1 := strconv.Atoi(hs)
	m, e2 := strconv.Atoi(ms)
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

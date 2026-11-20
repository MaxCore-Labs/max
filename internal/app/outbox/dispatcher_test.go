package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/outbox"
	"max/internal/platform"
	"max/internal/testutil/testdb"
)

type fakeNotifier struct {
	mu    sync.Mutex
	fail  bool
	sent  []app.Notification
}

func (n *fakeNotifier) Notify(_ context.Context, msg app.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail {
		return errors.New("downstream unreachable")
	}
	n.sent = append(n.sent, msg)
	return nil
}

type stepClock struct {
	now time.Time
}

func (c *stepClock) Now() time.Time { return c.now }

func TestDispatchOnceOnSuccess(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	clk := &stepClock{now: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)}
	notif := &fakeNotifier{}

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   `{"kind":"notification","title":"Báo cáo xong"}`,
			Urgency:       "normal",
			NextAttemptAt: clk.now.UnixMilli(),
		})
	})

	d := outbox.NewDispatcher(outbox.Config{
		Store:    st,
		Notifier: notif,
		Clock:    clk,
		TZ:       "UTC",
	})
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	if len(notif.sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(notif.sent))
	}
}

func TestRetryWithBackoff(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	clk := &stepClock{now: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)}
	notif := &fakeNotifier{fail: true}
	var logBuf bytes.Buffer
	log := platform.NewLogger(&logBuf, "info")

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   `{"kind":"notification","title":"Retry test"}`,
			Urgency:       "normal",
			NextAttemptAt: clk.now.UnixMilli(),
		})
	})

	d := outbox.NewDispatcher(outbox.Config{
		Store:    st,
		Notifier: notif,
		Clock:    clk,
		TZ:       "UTC",
		Logger:   log,
	})

	var prevNext int64
	for attempt := 1; attempt <= 10; attempt++ {
		if err := d.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", attempt, err)
		}
		rows, _ := st.Read().ListOutboxPending(ctx, 10)
		if attempt < 10 {
			if len(rows) != 1 || rows[0].Attempts != attempt {
				t.Fatalf("after attempt %d: rows=%+v", attempt, rows)
			}
			if rows[0].NextAttemptAt <= prevNext {
				t.Fatalf("attempt %d next_attempt_at %d did not increase over %d", attempt, rows[0].NextAttemptAt, prevNext)
			}
			prevNext = rows[0].NextAttemptAt
			clk.now = time.UnixMilli(rows[0].NextAttemptAt).Add(time.Second)
		} else {
			if len(rows) != 0 {
				t.Fatalf("after 10th failed attempt, expected row retired from pending, got %+v", rows)
			}
		}
	}
	if !strings.Contains(logBuf.String(), "max attempts") && !strings.Contains(logBuf.String(), "dropped") {
		t.Fatalf("expected drop log on 10th failure, got:\n%s", logBuf.String())
	}
}

func TestDedupeKey(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	clk := &stepClock{now: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)}
	notif := &fakeNotifier{}

	dedupe := "alert:quota:short"
	for range 3 {
		_ = st.WithTx(ctx, func(tx app.Tx) error {
			return tx.InsertOutbox(ctx, app.OutboxRecord{
				Topic:         "inbox",
				PayloadJSON:   `{"kind":"notification","title":"Dedupe"}`,
				DedupeKey:     &dedupe,
				Urgency:       "normal",
				NextAttemptAt: clk.now.UnixMilli(),
			})
		})
	}

	d := outbox.NewDispatcher(outbox.Config{Store: st, Notifier: notif, Clock: clk, TZ: "UTC"})
	_ = d.Tick(ctx)
	if len(notif.sent) != 1 {
		t.Fatalf("deduped sent = %d, want 1", len(notif.sent))
	}
}

func TestQuietHours(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	// 23:30 UTC inside quiet hours 22:00-07:00.
	clk := &stepClock{now: time.Date(2026, 11, 20, 23, 30, 0, 0, time.UTC)}
	notif := &fakeNotifier{}

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   `{"kind":"notification","title":"Normal digest"}`,
			Urgency:       "normal",
			NextAttemptAt: clk.now.UnixMilli(),
		})
		_ = tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   `{"kind":"approval","title":"Duyệt chi tiền"}`,
			Urgency:       "high",
			NextAttemptAt: clk.now.UnixMilli(),
		})
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   `{"kind":"error","title":"Lỗi nghiêm trọng"}`,
			Urgency:       "high",
			NextAttemptAt: clk.now.UnixMilli(),
		})
	})

	d := outbox.NewDispatcher(outbox.Config{
		Store:      st,
		Notifier:   notif,
		Clock:      clk,
		TZ:         "UTC",
		QuietHours: "22:00-07:00",
	})
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick during quiet hours: %v", err)
	}
	// Approval and error sent immediately; normal held!
	if len(notif.sent) != 2 {
		t.Fatalf("sent during quiet hours = %+v, want 2 (approval + error)", notif.sent)
	}

	// Advance clock past 07:00 next morning -> held normal notification now dispatches!
	clk.now = time.Date(2026, 11, 21, 7, 1, 0, 0, time.UTC)
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick after quiet hours: %v", err)
	}
	if len(notif.sent) != 3 {
		t.Fatalf("sent after quiet hours = %d, want 3", len(notif.sent))
	}
}

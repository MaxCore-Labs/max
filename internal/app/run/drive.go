package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
)

func (s *Service) driveEngine(ctx context.Context, claimed actor.Claimed, eng app.Engine, spec app.RunSpec, priorConvTurns int) Outcome {
	runCtx, cancel := context.WithTimeout(ctx, s.deps.WallTimeout)
	defer cancel()

	var askedApproval bool
	perm := func(pctx context.Context, ask app.ToolAsk) domain.Decision {
		if s.deps.Permission == nil {
			return domain.Decision{Verdict: domain.Allow}
		}
		dec := s.deps.Permission(pctx, ask)
		if dec.Verdict == domain.Ask {
			askedApproval = true
		}
		return dec
	}

	evCh, err := eng.Run(runCtx, spec, perm)
	if err != nil {
		return Outcome{Status: domain.RunInterrupted, Err: err.Error()}
	}

	var (
		out             Outcome
		textBuf         strings.Builder
		started         bool
		afterToolResult bool
		sawFinal        bool
	)
	out.SessionID = spec.SessionID

	for ev := range evCh {
		if s.deps.OnEvent != nil {
			s.deps.OnEvent(claimed, ev)
		}
		if !started && (ev.Type == "session" || ev.Type == "text_delta" || ev.Type == "tool_call" || ev.Type == "final") {
			started = true
			out.Turns = 1
		}
		switch ev.Type {
		case "session":
			if ev.SessionID != "" {
				out.SessionID = ev.SessionID
			}
		case "text_delta":
			if afterToolResult {
				out.Turns++
				afterToolResult = false
			}
			textBuf.WriteString(ev.Text)
		case "tool_call":
			if afterToolResult {
				out.Turns++
				afterToolResult = false
			}
		case "tool_result":
			afterToolResult = true
		case "usage":
			if ev.InputTokens > 0 {
				out.InputTokens = ev.InputTokens
			}
			if ev.OutputTokens > 0 {
				out.OutputTokens = ev.OutputTokens
			}
		case "limit":
			out.Status = domain.RunQuotaStopped
			out.LimitResetAt = ev.LimitResetAt
			cancel()
		case "error":
			if out.Status == "" {
				out.Status = domain.RunInterrupted
				out.Err = ev.ErrMessage
			}
			cancel()
		case "final":
			sawFinal = true
			if ev.InputTokens > 0 {
				out.InputTokens = ev.InputTokens
			}
			if ev.OutputTokens > 0 {
				out.OutputTokens = ev.OutputTokens
			}
			if ev.Text != "" && textBuf.Len() == 0 {
				textBuf.WriteString(ev.Text)
			}
		}

		if out.Status == "" && (out.Turns > domain.MaxTurnsPerRun || priorConvTurns+out.Turns > domain.MaxTurnsPerTask) {
			out.Status = domain.RunTurnLimit
			cancel()
		}
	}

	out.Summary = truncateRunes(strings.TrimSpace(textBuf.String()), 1000)

	if out.Status == "" {
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
			out.Status = domain.RunTimedOut
		case ctx.Err() != nil:
			out.Status = domain.RunInterrupted
			out.Err = ctx.Err().Error()
		case !sawFinal:
			out.Status = domain.RunInterrupted
			if out.Err == "" {
				out.Err = "engine stream closed without final event"
			}
		case askedApproval:
			out.Status = domain.RunParked
		default:
			out.Status = domain.RunSucceeded
		}
	}
	return out
}

func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// NotifyEngineUnavailableOnce inserts a deduplicated owner Inbox item when an engine loses login or health.
func NotifyEngineUnavailableOnce(ctx context.Context, store app.Store, ids app.IDGen, eng domain.EngineName, reason string, now time.Time) error {
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	nowMs := now.UnixMilli()
	day := now.UTC().Format("2006-01-02")
	dedupe := fmt.Sprintf("engine-down:%s:%s:%s", eng, reason, day)
	refKind := "engine"
	refID := string(eng)
	return store.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:        ids.New(),
			Kind:      "system",
			Title:     fmt.Sprintf("Engine %s tạm ngừng (%s)", eng, reason),
			Body:      fmt.Sprintf("Engine %s không sẵn sàng (%s). Các agent chỉ dùng %s sẽ xếp hàng chờ; agent có engine dự phòng sẽ tự chuyển.", eng, reason, eng),
			RefKind:   &refKind,
			RefID:     &refID,
			Urgency:   "high",
			DedupeKey: &dedupe,
			CreatedAt: nowMs,
		})
	})
}

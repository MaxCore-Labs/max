package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/platform"
)

// ErrNotFound indicates a requested thread does not exist.
var ErrNotFound = errors.New("thread not found")

// BusyInfo reports what work is currently occupying an agent when a thread checks its snapshot.
type BusyInfo struct {
	RunID          domain.ID
	Title          string
	MinutesRunning int
}

// ThreadSnapshot bundles thread metadata, recent messages, live run state, and busy indicator.
type ThreadSnapshot struct {
	Thread      app.ThreadRecord
	Messages    []app.MessageRecord
	ActiveRunID *domain.ID
	LiveText    string
	BusyWith    *BusyInfo
}

// Service implements durable owner chat threads backed by SQLite and actor wakeups.
type Service struct {
	store    app.Store
	actors   *actor.Service
	clock    app.Clock
	ids      app.IDGen
	liveText func(runID domain.ID) string
}

// NewService constructs a chat Service.
func NewService(store app.Store, actors *actor.Service, clock app.Clock, ids app.IDGen, liveText func(domain.ID) string) *Service {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Service{
		store:    store,
		actors:   actors,
		clock:    clock,
		ids:      ids,
		liveText: liveText,
	}
}

// CreateThread creates a new owner chat thread targeting agentID (defaulting to chief-of-staff if empty).
func (s *Service) CreateThread(ctx context.Context, agentID domain.ID, title string) (domain.ID, error) {
	nowMs := s.clock.Now().UnixMilli()
	cleanTitle := strings.TrimSpace(title)
	if cleanTitle == "" {
		cleanTitle = "Cuộc trò chuyện mới"
	}

	var threadID domain.ID
	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		targetAgent := strings.TrimSpace(agentID)
		if targetAgent == "" {
			ag, ok, err := tx.GetAgentBySlug(ctx, "chief-of-staff")
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("default agent chief-of-staff not found")
			}
			targetAgent = ag.ID
		}
		threadID = s.ids.New()
		if err := tx.InsertThread(ctx, app.ThreadRecord{
			ID:        threadID,
			AgentID:   targetAgent,
			Title:     cleanTitle,
			CreatedAt: nowMs,
			UpdatedAt: nowMs,
		}); err != nil {
			return err
		}
		_, err := tx.EnsureThreadConversation(ctx, targetAgent, threadID, s.ids.New())
		return err
	})
	return threadID, err
}

// Send persists an owner message and wakes the thread's agent at interactive priority (100) in one transaction.
func (s *Service) Send(ctx context.Context, threadID domain.ID, content string, attachments []string, idemKey string) (messageID, wakeupID domain.ID, err error) {
	cleanContent := platform.Redact(strings.TrimSpace(content))
	if cleanContent == "" {
		return "", "", errors.New("message content cannot be empty")
	}
	if attachments == nil {
		attachments = []string{}
	}
	attBytes, _ := json.Marshal(attachments)
	nowMs := s.clock.Now().UnixMilli()
	trimmedIdem := strings.TrimSpace(idemKey)

	err = s.store.WithTx(ctx, func(tx app.Tx) error {
		th, ok, err := tx.GetThread(ctx, threadID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}

		convID, err := tx.EnsureThreadConversation(ctx, th.AgentID, threadID, s.ids.New())
		if err != nil {
			return err
		}

		if trimmedIdem != "" {
			existingMsg, found, err := tx.GetMessageByIdem(ctx, trimmedIdem)
			if err != nil {
				return err
			}
			if found {
				messageID = existingMsg.ID
				wakeIdem := "chat-msg:" + trimmedIdem
				wPayload, _ := json.Marshal(map[string]string{
					"message_id": messageID,
					"thread_id":  threadID,
					"text":       existingMsg.Content,
				})
				wID, _, wErr := s.actors.Wake(ctx, tx, actor.WakeInput{
					AgentID:        th.AgentID,
					Kind:           domain.WakeMessage,
					Priority:       domain.PrioInteractive,
					ConversationID: &convID,
					Payload:        wPayload,
					IdempotencyKey: &wakeIdem,
				})
				wakeupID = wID
				return wErr
			}
		}

		messageID = s.ids.New()
		var idemPtr *string
		var wakeIdemPtr *string
		if trimmedIdem != "" {
			cp := trimmedIdem
			idemPtr = &cp
			wcp := "chat-msg:" + trimmedIdem
			wakeIdemPtr = &wcp
		}
		if err := tx.InsertMessage(ctx, app.MessageRecord{
			ID:              messageID,
			ThreadID:        threadID,
			Role:            "user",
			Content:         cleanContent,
			AttachmentsJSON: string(attBytes),
			IdempotencyKey:  idemPtr,
			CreatedAt:       nowMs,
		}); err != nil {
			return err
		}
		if err := tx.TouchThread(ctx, threadID, nowMs); err != nil {
			return err
		}

		wPayload, _ := json.Marshal(map[string]string{
			"message_id": messageID,
			"thread_id":  threadID,
			"text":       cleanContent,
		})
		wID, _, wErr := s.actors.Wake(ctx, tx, actor.WakeInput{
			AgentID:        th.AgentID,
			Kind:           domain.WakeMessage,
			Priority:       domain.PrioInteractive,
			ConversationID: &convID,
			Payload:        wPayload,
			IdempotencyKey: wakeIdemPtr,
		})
		wakeupID = wID
		return wErr
	})
	return messageID, wakeupID, err
}

// Reset clears CLI session bindings on the thread's conversation so the next turn starts a fresh session.
func (s *Service) Reset(ctx context.Context, threadID domain.ID) error {
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		th, ok, err := tx.GetThread(ctx, threadID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		return tx.ResetThreadConversation(ctx, th.AgentID, threadID)
	})
}

// Snapshot returns thread metadata, latest messages, active run live text, and busy status if the agent is occupied elsewhere.
func (s *Service) Snapshot(ctx context.Context, threadID domain.ID) (ThreadSnapshot, error) {
	th, ok, err := s.store.Read().GetThread(ctx, threadID)
	if err != nil {
		return ThreadSnapshot{}, err
	}
	if !ok {
		return ThreadSnapshot{}, ErrNotFound
	}

	msgs, _, err := s.ListMessages(ctx, threadID, 50, "")
	if err != nil {
		return ThreadSnapshot{}, err
	}

	snap := ThreadSnapshot{
		Thread:   th,
		Messages: msgs,
	}

	conv, hasConv, _ := s.store.Read().GetConversationByThread(ctx, th.AgentID, threadID)
	ag, hasAg, _ := s.store.Read().GetAgent(ctx, th.AgentID)
	if !hasAg || ag.ActiveRunID == nil || *ag.ActiveRunID == "" {
		return snap, nil
	}

	activeRunID := *ag.ActiveRunID
	if hasConv {
		if r, ok, _ := s.store.Read().GetRun(ctx, activeRunID); ok {
			if r.ConversationID == conv.ID {
				snap.ActiveRunID = &activeRunID
				if s.liveText != nil {
					snap.LiveText = s.liveText(activeRunID)
				}
				return snap, nil
			}
			snap.BusyWith = s.describeBusyRun(ctx, r)
			return snap, nil
		}
	}
	return snap, nil
}

func (s *Service) describeBusyRun(ctx context.Context, r app.RunRecord) *BusyInfo {
	title := "Công việc khác"
	if r.TaskID != nil && *r.TaskID != "" {
		if tk, ok, _ := s.store.Read().GetTask(ctx, *r.TaskID); ok && tk.Title != "" {
			title = tk.Title
		}
	} else if conv, ok, _ := s.store.Read().GetConversation(ctx, r.ConversationID); ok && conv.ThreadID != nil {
		if th, tok, _ := s.store.Read().GetThread(ctx, *conv.ThreadID); tok && th.Title != "" {
			title = th.Title
		}
	}
	mins := int(s.clock.Now().UnixMilli()-r.StartedAt) / 60000
	if mins < 0 {
		mins = 0
	}
	return &BusyInfo{
		RunID:          r.ID,
		Title:          title,
		MinutesRunning: mins,
	}
}

// ListThreads returns paginated threads ordered by updated_at DESC.
func (s *Service) ListThreads(ctx context.Context, limit int, cursor string) ([]app.ThreadRecord, *string, error) {
	if limit <= 0 {
		limit = 50
	}
	ts, id := parseCursor(cursor)
	rows, err := s.store.Read().ListThreads(ctx, limit+1, ts, id)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		c := formatCursor(last.UpdatedAt, last.ID)
		next = &c
		rows = rows[:limit]
	}
	return rows, next, nil
}

// ListMessages returns paginated thread messages ordered newest-first using the `before` cursor.
func (s *Service) ListMessages(ctx context.Context, threadID domain.ID, limit int, before string) ([]app.MessageRecord, *string, error) {
	if limit <= 0 {
		limit = 50
	}
	ts, id := parseCursor(before)
	rows, err := s.store.Read().ListThreadMessages(ctx, threadID, limit+1, ts, id)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		c := formatCursor(last.CreatedAt, last.ID)
		next = &c
		rows = rows[:limit]
	}
	return rows, next, nil
}

func formatCursor(ts int64, id string) string {
	return strconv.FormatInt(ts, 10) + "_" + id
}

func parseCursor(c string) (*int64, string) {
	c = strings.TrimSpace(c)
	if c == "" {
		return nil, ""
	}
	rawTS, id, ok := strings.Cut(c, "_")
	if !ok {
		return nil, ""
	}
	ts, err := strconv.ParseInt(rawTS, 10, 64)
	if err != nil {
		return nil, ""
	}
	return &ts, id
}

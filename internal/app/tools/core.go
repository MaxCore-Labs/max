package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	"max/internal/domain"
	"max/internal/platform"
)

const (
	maxNotesLines     = 80
	maxToolResultChar = 8000
)

// Caller identifies the verified agent run invoking a Max tool.
type Caller struct {
	RunID, AgentID, ConversationID domain.ID
	TaskID                         *domain.ID
	Hops                           int
	Policy                         domain.AgentPolicy
	Origin                         domain.RootOrigin
}

// Result is the transport-independent tool response returned by Max tools.
type Result struct {
	Text    string
	IsError bool
}

// Service implements Max's core MCP tools independent of transport protocol.
type Service struct {
	store     app.Store
	vaultDir  string
	clock     app.Clock
	ids       app.IDGen
	coord     *coord.Service
	actors    *actor.Service
	memory    app.EpisodicMemory
	mu        sync.Mutex
	runCounts map[domain.ID]domain.RunCounters
}

// NewService constructs a core tools Service.
func NewService(store app.Store, vaultDir string, clock app.Clock, ids app.IDGen) *Service {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Service{
		store:     store,
		vaultDir:  vaultDir,
		clock:     clock,
		ids:       ids,
		runCounts: make(map[domain.ID]domain.RunCounters),
	}
}

// WithCoord attaches multi-agent task coordination and mailbox services to tools.Service.
func (s *Service) WithCoord(coordSvc *coord.Service, actors *actor.Service) *Service {
	s.coord = coordSvc
	s.actors = actors
	return s
}

// WithMemory attaches an EpisodicMemory port to tools.Service.
func (s *Service) WithMemory(mem app.EpisodicMemory) *Service {
	s.memory = mem
	return s
}

func (s *Service) checkPolicy(c Caller, name string, class domain.ToolClass, rel string) (bool, Result) {
	dec := domain.Decide(c.Policy, domain.ToolCall{
		Name:  name,
		Class: class,
		Rel:   rel,
	}, c.Origin, nil, s.clock.Now())
	if dec.Verdict != domain.Allow {
		return false, Result{Text: fmt.Sprintf("tool %s denied (%s): %s", name, dec.Verdict, dec.Reason), IsError: true}
	}
	return true, Result{}
}

// Ping returns caller run and agent identity for health/connectivity verification.
func (s *Service) Ping(_ context.Context, c Caller) Result {
	if ok, res := s.checkPolicy(c, "ping", domain.ToolClassC, ""); !ok {
		return res
	}
	return Result{Text: fmt.Sprintf("pong agent=%s run=%s", c.Policy.Scope.Slug, c.RunID)}
}

// NotifyOwner records a non-blocking owner notification in inbox_items and outbox.
func (s *Service) NotifyOwner(ctx context.Context, c Caller, text, urgency string) Result {
	if ok, res := s.checkPolicy(c, "notify_owner", domain.ToolClassC, ""); !ok {
		return res
	}
	cleanText := platform.Redact(strings.TrimSpace(text))
	if cleanText == "" {
		return Result{Text: "notify_owner requires non-empty text", IsError: true}
	}
	if urgency == "" {
		urgency = "normal"
	}
	nowMs := s.clock.Now().UnixMilli()
	itemID := s.ids.New()

	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		convID := c.ConversationID
		agID := c.AgentID
		if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:             itemID,
			Kind:           "notification",
			Title:          truncateChars(cleanText, 120),
			Body:           cleanText,
			ConversationID: &convID,
			AgentID:        &agID,
			Urgency:        urgency,
			CreatedAt:      nowMs,
		}); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"inbox_id": itemID, "kind": "notification"})
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   string(payload),
			Urgency:       urgency,
			NextAttemptAt: nowMs,
		})
	})
	if err != nil {
		return Result{Text: fmt.Sprintf("notify_owner failed: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Đã thông báo cho chủ nhân (mã %s).", itemID)}
}

// AskOwner posts a single question per run to Inbox, sets conversations.waiting_for, and instructs the agent to end its turn.
func (s *Service) AskOwner(ctx context.Context, c Caller, question string, choices []string) Result {
	if ok, res := s.checkPolicy(c, "ask_owner", domain.ToolClassC, ""); !ok {
		return res
	}
	cleanQ := platform.Redact(strings.TrimSpace(question))
	if cleanQ == "" {
		return Result{Text: "ask_owner requires non-empty question", IsError: true}
	}

	nowMs := s.clock.Now().UnixMilli()
	itemID := s.ids.New()
	choicesJSON, _ := json.Marshal(choices)

	var alreadyAsked bool
	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		n, err := tx.CountQuestionsByRun(ctx, c.RunID)
		if err != nil {
			return err
		}
		if n >= 1 {
			alreadyAsked = true
			return nil
		}
		refKind := "run"
		refID := c.RunID
		convID := c.ConversationID
		agID := c.AgentID
		if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:             itemID,
			Kind:           "question",
			Title:          truncateChars(cleanQ, 120),
			Body:           cleanQ,
			RefKind:        &refKind,
			RefID:          &refID,
			ConversationID: &convID,
			AgentID:        &agID,
			ChoicesJSON:    string(choicesJSON),
			Urgency:        "normal",
			CreatedAt:      nowMs,
		}); err != nil {
			return err
		}
		wfBytes, _ := json.Marshal(map[string]string{"kind": "owner", "inbox_id": itemID})
		wf := string(wfBytes)
		if err := tx.SetConversationWaitingFor(ctx, c.ConversationID, &wf); err != nil {
			return err
		}
		outPayload, _ := json.Marshal(map[string]string{"inbox_id": itemID, "kind": "question"})
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   string(outPayload),
			Urgency:       "normal",
			NextAttemptAt: nowMs,
		})
	})
	if err != nil {
		return Result{Text: fmt.Sprintf("ask_owner failed: %v", err), IsError: true}
	}
	if alreadyAsked {
		return Result{Text: "chỉ một câu hỏi mỗi lượt (only one question per run)", IsError: true}
	}
	return Result{
		Text: fmt.Sprintf("Đã gửi câu hỏi tới chủ nhân (mã %s). Hãy kết thúc lượt ngay để chờ phản hồi; không thử cách khác.", itemID),
	}
}

// NoteUpdate writes <= 80 redacted lines to agents/<slug>/notes/NOTES.md.
func (s *Service) NoteUpdate(_ context.Context, c Caller, content string) Result {
	slug := c.Policy.Scope.Slug
	rel := fmt.Sprintf("agents/%s/notes/NOTES.md", slug)
	if ok, res := s.checkPolicy(c, "note_update", domain.ToolClassWd, rel); !ok {
		return res
	}
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed != "" && len(strings.Split(trimmed, "\n")) > maxNotesLines {
		return Result{Text: "note_update vượt trần 80 dòng (exceeds 80-line cap)", IsError: true}
	}
	redacted := platform.Redact(content)
	fullPath := filepath.Join(s.vaultDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return Result{Text: fmt.Sprintf("mkdir notes dir: %v", err), IsError: true}
	}
	if err := os.WriteFile(fullPath, []byte(redacted), 0o644); err != nil {
		return Result{Text: fmt.Sprintf("write NOTES.md: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Updated %s", rel)}
}

// CapOutput enforces the 8,000-character MCP output cap, spilling larger payloads to agents/<slug>/artifacts/_tool/.
func (s *Service) CapOutput(c Caller, toolName, text string) Result {
	if len(text) <= maxToolResultChar && utf8.RuneCountInString(text) <= maxToolResultChar {
		return Result{Text: text}
	}
	slug := c.Policy.Scope.Slug
	if slug == "" {
		slug = "unknown"
	}
	rel := fmt.Sprintf("agents/%s/artifacts/_tool/%s-%s.txt", slug, toolName, s.ids.New())
	fullPath := filepath.Join(s.vaultDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err == nil {
		_ = os.WriteFile(fullPath, []byte(text), 0o644)
	}
	footer := fmt.Sprintf("\n\n... [truncated total=%d chars; full output saved at %s]", len(text), rel)
	budget := max(0, maxToolResultChar-len(footer))
	prefix := truncateChars(text, budget)
	return Result{Text: prefix + footer}
}

func truncateChars(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes && utf8.RuneCountInString(s) <= maxBytes {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		rl := utf8.RuneLen(r)
		if b.Len()+rl > maxBytes || n+1 > maxBytes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

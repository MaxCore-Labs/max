package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/platform"
)

const (
	recallSearchTimeout = 2 * time.Second
	maxRecallHits       = 5
	maxRecallPerConv    = 2
)

type memoryOutboxPayload struct {
	AgentID string `json:"agent_id"`
	Content string `json:"content"`
}

func (s *Service) injectRecallBlock(ctx context.Context, c actor.Claimed, basePrompt string) string {
	if s.deps.Memory == nil || !s.deps.Memory.Healthy() || c.Priority <= domain.PrioMaintenance {
		return basePrompt
	}
	if strings.Contains(basePrompt, "<recall>") {
		return basePrompt
	}
	query := buildRecallQuery(ctx, s.deps.Store.Read(), c)
	if query == "" {
		return basePrompt
	}

	rctx, cancel := context.WithTimeout(ctx, recallSearchTimeout)
	hits, err := s.deps.Memory.Search(rctx, c.AgentID, query, maxRecallHits*3)
	cancel()
	if err != nil || len(hits) == 0 {
		return basePrompt
	}

	perConv := make(map[domain.ID]int)
	var lines []string
	for _, h := range hits {
		if len(lines) >= maxRecallHits {
			break
		}
		if h.ConversationID != "" {
			if perConv[h.ConversationID] >= maxRecallPerConv {
				continue
			}
			perConv[h.ConversationID]++
		}
		text := strings.TrimSpace(h.Content)
		if text == "" {
			continue
		}
		if h.AuthorAgentID != "" && h.AuthorAgentID != c.AgentID {
			lines = append(lines, fmt.Sprintf("- [author:%s] %s", h.AuthorAgentID, text))
		} else {
			lines = append(lines, "- "+text)
		}
	}
	if len(lines) == 0 {
		return basePrompt
	}
	block := "<recall>\n" + strings.Join(lines, "\n") + "\n</recall>"
	if idx := strings.Index(basePrompt, "<inbox>"); idx >= 0 {
		return basePrompt[:idx] + block + "\n\n" + basePrompt[idx:]
	}
	if basePrompt == "" {
		return block
	}
	return basePrompt + "\n\n" + block
}

func buildRecallQuery(ctx context.Context, rd app.Reader, c actor.Claimed) string {
	var parts []string
	if c.TaskID != nil && *c.TaskID != "" {
		if tk, ok, err := rd.GetTask(ctx, *c.TaskID); err == nil && ok {
			parts = append(parts, tk.Title)
		}
	}
	for _, w := range c.Wakeups {
		if len(w.Payload) > 0 {
			parts = append(parts, string(w.Payload))
		}
	}
	q := strings.TrimSpace(strings.Join(parts, " "))
	if len(q) > 512 {
		q = q[:512]
	}
	return q
}

func (s *Service) recordMemoryInFinalizeTx(ctx context.Context, tx app.Tx, runRec app.RunRecord, rawSummary string, nowMs int64) (needPostTxRemember bool, cleanSummary string, err error) {
	if s.deps.Memory == nil || runRec.Priority <= domain.PrioMaintenance {
		return false, "", nil
	}
	clean := strings.TrimSpace(platform.Redact(rawSummary))
	if clean == "" {
		return false, "", nil
	}
	if txMem, ok := s.deps.Memory.(app.TxMemoryWriter); ok && s.deps.Memory.Healthy() {
		if err := txMem.RememberInTx(ctx, tx, runRec.AgentID, runRec.ConversationID, clean); err != nil {
			return false, "", err
		}
		return false, clean, nil
	}
	if !s.deps.Memory.Healthy() {
		return false, clean, enqueueMemoryOutbox(ctx, tx, runRec.ID, runRec.AgentID, clean, nowMs)
	}
	return true, clean, nil
}

func enqueueMemoryOutbox(ctx context.Context, tx app.Tx, runID, agentID domain.ID, cleanContent string, nowMs int64) error {
	payload, _ := json.Marshal(memoryOutboxPayload{
		AgentID: agentID,
		Content: cleanContent,
	})
	dedupe := "memory.remember:" + runID
	return tx.InsertOutbox(ctx, app.OutboxRecord{
		Topic:         "memory.remember",
		PayloadJSON:   string(payload),
		DedupeKey:     &dedupe,
		Urgency:       "normal",
		NextAttemptAt: nowMs,
	})
}

// FlushPendingMemories delivers queued memory.remember outbox items to mem when mem is Healthy.
func FlushPendingMemories(ctx context.Context, store app.Store, mem app.EpisodicMemory, now time.Time) (int, error) {
	if mem == nil || !mem.Healthy() {
		return 0, nil
	}
	pending, err := store.Read().ListOutboxPending(ctx, 100)
	if err != nil {
		return 0, err
	}
	sent := 0
	nowMs := now.UnixMilli()
	for _, rec := range pending {
		if rec.Topic != "memory.remember" || rec.NextAttemptAt > nowMs {
			continue
		}
		var p memoryOutboxPayload
		if err := json.Unmarshal([]byte(rec.PayloadJSON), &p); err != nil {
			continue
		}
		if err := mem.Remember(ctx, domain.ID(p.AgentID), p.Content, nil); err != nil {
			return sent, err
		}
		if err := store.WithTx(ctx, func(tx app.Tx) error {
			return tx.MarkOutboxSent(ctx, rec.ID, nowMs)
		}); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

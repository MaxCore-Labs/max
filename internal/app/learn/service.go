package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
	"max/prompts"
)

const (
	quietDuration       = 15 * time.Minute
	explicitQuietDur    = 30 * time.Second
	minUnlearnedChars   = 40
	maxLearnInputBudget = 48000
	disabledSkillsKVKey = "skills.disabled"
)

// Service orchestrates read-only learning passes, deterministic domain gating, single-commit vault writes, and 1-commit undo.
type Service struct {
	store  app.Store
	vault  app.Vault
	engine app.Engine
	clock  app.Clock
	ids    app.IDGen
}

// NewService constructs a learning Service.
func NewService(store app.Store, vault app.Vault, engine app.Engine, clock app.Clock, ids app.IDGen) *Service {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Service{store: store, vault: vault, engine: engine, clock: clock, ids: ids}
}

// Due returns thread IDs that have unlearned owner turns and have been quiet for 15m (or 30s if owner wrote "ghi nhớ").
func (s *Service) Due(ctx context.Context, now time.Time) ([]domain.ID, error) {
	threads, err := s.store.Read().ListThreads(ctx, 100, nil, "")
	if err != nil {
		return nil, err
	}
	var due []domain.ID
	for _, th := range threads {
		msgs, err := s.store.Read().ListThreadMessagesAsc(ctx, th.ID, th.LearnedThroughMsg, 500)
		if err != nil || len(msgs) == 0 {
			continue
		}
		var (
			userRunes   int
			hasExplicit bool
			lastMsgMs   int64
		)
		for _, m := range msgs {
			if m.CreatedAt > lastMsgMs {
				lastMsgMs = m.CreatedAt
			}
			if m.Role == "user" {
				trimmed := strings.TrimSpace(m.Content)
				userRunes += utf8.RuneCountInString(trimmed)
				if strings.Contains(strings.ToLower(trimmed), "ghi nhớ") {
					hasExplicit = true
				}
			}
		}
		if lastMsgMs == 0 {
			continue
		}
		quietFor := now.Sub(time.UnixMilli(lastMsgMs))
		if hasExplicit {
			if quietFor >= explicitQuietDur {
				due = append(due, th.ID)
			}
			continue
		}
		if userRunes < minUnlearnedChars {
			continue
		}
		if quietFor >= quietDuration {
			due = append(due, th.ID)
		}
	}
	return due, nil
}

// Run executes one PrioMaintenance read-only learning pass bounded to 48,000 input chars and writes 1 "learn:" commit.
func (s *Service) Run(ctx context.Context, threadID domain.ID) (string, error) {
	th, ok, err := s.store.Read().GetThread(ctx, threadID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("thread %q not found", threadID)
	}
	allMsgs, err := s.store.Read().ListThreadMessagesAsc(ctx, threadID, th.LearnedThroughMsg, 500)
	if err != nil || len(allMsgs) == 0 {
		return "", err
	}

	batch, lastMsgID, transcript := selectWithinBudget(allMsgs, maxLearnInputBudget)
	msgMeta := make(map[domain.ID]domain.MsgMeta, len(batch))
	for _, m := range batch {
		msgMeta[m.ID] = domain.MsgMeta{ID: m.ID, Role: m.Role}
	}

	prop, err := s.invokeReadOnlyModel(ctx, th.AgentID, transcript)
	if err != nil {
		return "", err
	}

	existingFacts := s.listSlugs(ctx, "memory/facts", ".md")
	existingSkills := s.listSlugs(ctx, "skills", "")
	delete(existingSkills, "_proposed")
	disabledSkills := s.loadDisabledSkills(ctx)

	factGate := domain.GateFacts(prop.Facts, msgMeta, existingFacts)
	var acceptedSkills []domain.SkillProposal
	blocked := append([]domain.Blocked(nil), factGate.Blocked...)
	for _, sk := range prop.Skills {
		if ok, reason := domain.GateSkill(sk, existingSkills, disabledSkills); ok {
			acceptedSkills = append(acceptedSkills, sk)
		} else {
			blocked = append(blocked, domain.Blocked{Item: "skill:" + sk.Slug, Reason: reason})
		}
	}

	commitPaths, err := s.applyVaultWrites(ctx, factGate, acceptedSkills, blocked)
	if err != nil {
		return "", err
	}

	var sha string
	if len(commitPaths) > 0 {
		sha, err = s.vault.CommitPaths(ctx, commitPaths, fmt.Sprintf("learn: thread %s (%d facts, %d skills)", threadID, len(factGate.Accepted), len(acceptedSkills)))
		if err != nil {
			return "", err
		}
	}

	nowMs := s.clock.Now().UnixMilli()
	if err := s.store.WithTx(ctx, func(tx app.Tx) error {
		for _, sk := range acceptedSkills {
			tp := fmt.Sprintf("skills/_proposed/%s/SKILL.md", sk.Slug)
			payload, _ := json.Marshal(sk)
			if err := tx.InsertProposal(ctx, app.ProposalRecord{
				ID:          s.ids.New(),
				Kind:        "skill",
				AgentID:     &th.AgentID,
				TargetPath:  &tp,
				Title:       fmt.Sprintf("Skill đề xuất: %s", sk.Slug),
				PayloadJSON: string(payload),
				Status:      "pending",
				CreatedAt:   nowMs,
			}); err != nil {
				return err
			}
		}
		for _, ed := range prop.InstructionEdits {
			tp := strings.TrimSpace(ed.TargetPath)
			if tp == "" || strings.TrimSpace(ed.Diff) == "" {
				continue
			}
			payload, _ := json.Marshal(ed)
			title := strings.TrimSpace(ed.Title)
			if title == "" {
				title = "Đề xuất cập nhật " + tp
			}
			if err := tx.InsertProposal(ctx, app.ProposalRecord{
				ID:          s.ids.New(),
				Kind:        "instruction_edit",
				AgentID:     &th.AgentID,
				TargetPath:  &tp,
				Title:       title,
				PayloadJSON: string(payload),
				Status:      "pending",
				CreatedAt:   nowMs,
			}); err != nil {
				return err
			}
		}
		return tx.SetThreadLearnedThrough(ctx, threadID, lastMsgID)
	}); err != nil {
		return sha, err
	}
	return sha, nil
}

// UndoLast reverts the most recent "learn:" commit in the vault git history.
func (s *Service) UndoLast(ctx context.Context) error {
	_, err := s.vault.RevertLastByPrefix(ctx, "learn:")
	return err
}

func selectWithinBudget(msgs []app.MessageRecord, budget int) ([]app.MessageRecord, domain.ID, string) {
	var (
		selected []app.MessageRecord
		sb       strings.Builder
		used     int
	)
	for i, m := range msgs {
		line := fmt.Sprintf("[%s] (%s): %s\n", m.ID, m.Role, m.Content)
		runes := utf8.RuneCountInString(line)
		if i > 0 && used+runes > budget {
			break
		}
		if i == 0 && runes > budget {
			rs := []rune(line)
			line = string(rs[:budget])
			runes = budget
		}
		sb.WriteString(line)
		used += runes
		selected = append(selected, m)
	}
	return selected, selected[len(selected)-1].ID, sb.String()
}

func (s *Service) invokeReadOnlyModel(ctx context.Context, agentID domain.ID, transcript string) (domain.Proposal, error) {
	if s.engine == nil {
		return domain.Proposal{}, nil
	}
	ch, err := s.engine.Run(ctx, app.RunSpec{
		RunID:        "learn-" + s.ids.New(),
		AgentID:      agentID,
		AgentSlug:    "learning-pass",
		SystemPrompt: prompts.Learning,
		Prompt:       transcript,
		Model:        "default",
		Autonomy:     domain.AutonomySuggest,
		JSONSchema:   `{"type":"object"}`,
	}, nil)
	if err != nil {
		return domain.Proposal{}, err
	}
	var out strings.Builder
	for ev := range ch {
		if ev.Type == "text_delta" {
			out.WriteString(ev.Text)
		}
	}
	raw := strings.TrimSpace(out.String())
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return domain.Proposal{}, nil
	}
	var prop domain.Proposal
	if err := json.Unmarshal([]byte(raw), &prop); err != nil {
		return domain.Proposal{}, fmt.Errorf("parse learning proposal json: %w", err)
	}
	return prop, nil
}

func (s *Service) applyVaultWrites(
	ctx context.Context,
	factGate domain.GateResult,
	skills []domain.SkillProposal,
	blocked []domain.Blocked,
) ([]string, error) {
	var paths []string
	addPath := func(p string) {
		for _, ex := range paths {
			if ex == p {
				return
			}
		}
		paths = append(paths, p)
	}

	for oldSlug, newSlug := range factGate.SupersedeUpdates {
		oldRel := fmt.Sprintf("memory/facts/%s.md", oldSlug)
		raw, err := s.vault.Read(ctx, oldRel)
		if err != nil {
			continue
		}
		updated := setFrontmatterField(string(raw), "superseded_by", newSlug)
		if err := s.vault.Write(ctx, oldRel, []byte(updated)); err != nil {
			return nil, err
		}
		addPath(oldRel)
	}

	if len(factGate.Accepted) > 0 {
		memRaw, _ := s.vault.Read(ctx, "memory/MEMORY.md")
		memStr := strings.TrimRight(string(memRaw), "\n")
		if memStr == "" {
			memStr = "# Memory Index\n"
		}
		for oldSlug := range factGate.SupersedeUpdates {
			memStr = removeIndexSlugLine(memStr, oldSlug)
		}
		for _, f := range factGate.Accepted {
			rel := fmt.Sprintf("memory/facts/%s.md", f.Slug)
			stmt := platform.Redact(f.Statement)
			var fm strings.Builder
			fm.WriteString("---\n")
			_, _ = fmt.Fprintf(&fm, "slug: %s\n", f.Slug)
			_, _ = fmt.Fprintf(&fm, "type: %s\n", defaultStr(f.Type, "company"))
			_, _ = fmt.Fprintf(&fm, "confidence: %d\n", f.Confidence)
			_, _ = fmt.Fprintf(&fm, "evidence_msg_id: %s\n", f.EvidenceMsgID)
			if f.Supersedes != "" {
				_, _ = fmt.Fprintf(&fm, "supersedes: %s\n", f.Supersedes)
			}
			fm.WriteString("---\n\n")
			fm.WriteString(stmt + "\n")
			if err := s.vault.Write(ctx, rel, []byte(fm.String())); err != nil {
				return nil, err
			}
			addPath(rel)
			memStr += fmt.Sprintf("\n- [%s](facts/%s.md): %s", f.Slug, f.Slug, stmt)
		}
		if err := s.vault.Write(ctx, "memory/MEMORY.md", []byte(memStr+"\n")); err != nil {
			return nil, err
		}
		addPath("memory/MEMORY.md")
	}

	for _, sk := range skills {
		rel := fmt.Sprintf("skills/_proposed/%s/SKILL.md", sk.Slug)
		content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", sk.Slug, platform.Redact(sk.Description), platform.Redact(sk.Body))
		if err := s.vault.Write(ctx, rel, []byte(content)); err != nil {
			return nil, err
		}
		addPath(rel)
	}

	if len(blocked) > 0 {
		logRel := "memory/learning-log.md"
		prev, _ := s.vault.Read(ctx, logRel)
		var lb strings.Builder
		if len(prev) > 0 {
			lb.WriteString(strings.TrimRight(string(prev), "\n") + "\n\n")
		} else {
			lb.WriteString("# Learning Log\n\n")
		}
		if !strings.Contains(lb.String(), "## Blocked") {
			lb.WriteString("## Blocked\n")
		}
		ts := s.clock.Now().UTC().Format(time.RFC3339)
		for _, b := range blocked {
			_, _ = fmt.Fprintf(&lb, "- [%s] `%s`: %s\n", ts, b.Item, b.Reason)
		}
		if err := s.vault.Write(ctx, logRel, []byte(lb.String())); err != nil {
			return nil, err
		}
		addPath(logRel)
	}
	return paths, nil
}

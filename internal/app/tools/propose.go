package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

// RememberFact validates an explicit owner-evidenced fact through domain.GateFacts and writes it to memory/facts/ (class Wt via Go gate).
func (s *Service) RememberFact(
	ctx context.Context,
	c Caller,
	slug, factType, statement string,
	confidence int,
	evidenceMsgID domain.ID,
	supersedes string,
) Result {
	if c.Policy.Autonomy == domain.AutonomySuggest {
		return Result{Text: "suggest-mode agent cannot write facts", IsError: true}
	}
	if s.vault == nil {
		return Result{Text: "vault unavailable for remember_fact", IsError: true}
	}
	conv, ok, err := s.store.Read().GetConversation(ctx, c.ConversationID)
	if err != nil || !ok || conv.ThreadID == nil || *conv.ThreadID == "" {
		return Result{Text: "remember_fact yêu cầu cuộc trò chuyện trực tiếp với chủ nhân (missing owner chat thread)", IsError: true}
	}
	threadMsgs, err := s.store.Read().ListThreadMessages(ctx, *conv.ThreadID, 200, nil, "")
	if err != nil || len(threadMsgs) == 0 {
		return Result{Text: "không tìm thấy tin nhắn của chủ nhân trong cuộc trò chuyện", IsError: true}
	}
	meta := make(map[domain.ID]domain.MsgMeta, len(threadMsgs))
	for _, m := range threadMsgs {
		meta[m.ID] = domain.MsgMeta{ID: m.ID, Role: m.Role}
	}

	existing := make(map[string]bool)
	if entries, lErr := s.vault.ListDir(ctx, "memory/facts"); lErr == nil {
		for _, e := range entries {
			if strings.HasSuffix(e, ".md") {
				existing[strings.TrimSuffix(e, ".md")] = true
			}
		}
	}

	gate := domain.GateFacts([]domain.FactProposal{{
		Slug:          slug,
		Type:          factType,
		Statement:     statement,
		Confidence:    confidence,
		EvidenceMsgID: evidenceMsgID,
		Supersedes:    supersedes,
	}}, meta, existing)
	if len(gate.Accepted) == 0 {
		reason := "bị chặn bởi cổng kiểm tra"
		if len(gate.Blocked) > 0 {
			reason = gate.Blocked[0].Reason
		}
		return Result{Text: fmt.Sprintf("remember_fact bị từ chối: %s", reason), IsError: true}
	}

	f := gate.Accepted[0]
	cleanStmt := platform.Redact(f.Statement)
	var commitPaths []string

	if oldSlug := f.Supersedes; oldSlug != "" {
		oldRel := fmt.Sprintf("memory/facts/%s.md", oldSlug)
		if oldRaw, rErr := s.vault.Read(ctx, oldRel); rErr == nil {
			updatedOld := strings.Replace(string(oldRaw), "\n---\n", "\nsuperseded_by: "+f.Slug+"\n---\n", 1)
			_ = s.vault.Write(ctx, oldRel, []byte(updatedOld))
			commitPaths = append(commitPaths, oldRel)
		}
	}

	factRel := fmt.Sprintf("memory/facts/%s.md", f.Slug)
	tName := strings.TrimSpace(f.Type)
	if tName == "" {
		tName = "company"
	}
	var doc strings.Builder
	doc.WriteString("---\n")
	_, _ = fmt.Fprintf(&doc, "slug: %s\ntype: %s\nconfidence: %d\nevidence_msg_id: %s\n", f.Slug, tName, f.Confidence, f.EvidenceMsgID)
	if f.Supersedes != "" {
		_, _ = fmt.Fprintf(&doc, "supersedes: %s\n", f.Supersedes)
	}
	doc.WriteString("---\n\n" + cleanStmt + "\n")
	if err := s.vault.Write(ctx, factRel, []byte(doc.String())); err != nil {
		return Result{Text: fmt.Sprintf("write fact failed: %v", err), IsError: true}
	}
	commitPaths = append(commitPaths, factRel)

	memRaw, _ := s.vault.Read(ctx, "memory/MEMORY.md")
	memStr := strings.TrimRight(string(memRaw), "\r\n")
	if memStr == "" {
		memStr = "# Memory Index"
	}
	memStr += fmt.Sprintf("\n- [%s](facts/%s.md): %s\n", f.Slug, f.Slug, cleanStmt)
	if err := s.vault.Write(ctx, "memory/MEMORY.md", []byte(memStr)); err != nil {
		return Result{Text: fmt.Sprintf("write MEMORY.md failed: %v", err), IsError: true}
	}
	commitPaths = append(commitPaths, "memory/MEMORY.md")

	if _, err := s.vault.CommitPaths(ctx, commitPaths, fmt.Sprintf("learn: fact %s", f.Slug)); err != nil {
		return Result{Text: fmt.Sprintf("commit fact failed: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Đã ghi nhớ sự thật %s vào memory/facts/%s.md.", f.Slug, f.Slug)}
}

// RoutinePropose stores a disabled routine proposal and posts an owner Inbox item (never writes routines/*.md directly).
func (s *Service) RoutinePropose(ctx context.Context, c Caller, slug, title, schedule, body string) Result {
	if ok, res := s.checkPolicy(c, "routine_propose", domain.ToolClassP, ""); !ok {
		return res
	}
	slug = strings.TrimSpace(slug)
	title = strings.TrimSpace(title)
	schedule = strings.TrimSpace(schedule)
	if slug == "" || title == "" || schedule == "" {
		return Result{Text: "routine_propose requires slug, title, and schedule", IsError: true}
	}
	payload, _ := json.Marshal(map[string]string{
		"slug":     slug,
		"title":    platform.Redact(title),
		"schedule": schedule,
		"body":     platform.Redact(body),
	})
	tp := fmt.Sprintf("routines/%s.md", slug)
	return s.insertOwnerProposal(ctx, c, "routine", &tp, fmt.Sprintf("Đề xuất lịch định kỳ: %s", title), string(payload))
}

// AgentPropose stores a disabled agent instantiation proposal and posts an owner Inbox item.
func (s *Service) AgentPropose(ctx context.Context, c Caller, slug, templateSlug, teamSlug, reason string) Result {
	if ok, res := s.checkPolicy(c, "agent_propose", domain.ToolClassP, ""); !ok {
		return res
	}
	slug = strings.TrimSpace(slug)
	templateSlug = strings.TrimSpace(templateSlug)
	teamSlug = strings.TrimSpace(teamSlug)
	if slug == "" || templateSlug == "" || teamSlug == "" {
		return Result{Text: "agent_propose requires slug, template, and team", IsError: true}
	}
	payload, _ := json.Marshal(map[string]string{
		"slug":     slug,
		"template": templateSlug,
		"team":     teamSlug,
		"reason":   platform.Redact(reason),
	})
	tp := fmt.Sprintf("agents/%s/AGENT.md", slug)
	return s.insertOwnerProposal(ctx, c, "agent", &tp, fmt.Sprintf("Đề xuất tạo agent: %s (%s)", slug, templateSlug), string(payload))
}

// InstructionPropose stores a unified diff proposal against AGENT.md, TEAM.md, or COMPANY.md for owner review.
func (s *Service) InstructionPropose(ctx context.Context, c Caller, targetPath, title, diff string) Result {
	if ok, res := s.checkPolicy(c, "instruction_propose", domain.ToolClassP, ""); !ok {
		return res
	}
	cleanTarget, valid := domain.CleanRel(strings.TrimSpace(targetPath))
	if !valid || (!strings.HasSuffix(cleanTarget, "/AGENT.md") && !strings.HasSuffix(cleanTarget, "/TEAM.md") && cleanTarget != "company/COMPANY.md") {
		return Result{Text: "instruction_propose target_path phải là AGENT.md, TEAM.md hoặc company/COMPANY.md", IsError: true}
	}
	if strings.TrimSpace(diff) == "" {
		return Result{Text: "instruction_propose requires non-empty diff", IsError: true}
	}
	if strings.TrimSpace(title) == "" {
		title = "Đề xuất sửa chỉ dẫn " + cleanTarget
	}
	payload, _ := json.Marshal(map[string]string{
		"target_path": cleanTarget,
		"title":       platform.Redact(title),
		"diff":        platform.Redact(diff),
	})
	return s.insertOwnerProposal(ctx, c, "instruction_edit", &cleanTarget, title, string(payload))
}

func (s *Service) insertOwnerProposal(ctx context.Context, c Caller, kind string, targetPath *string, title, payloadJSON string) Result {
	propID := s.ids.New()
	nowMs := s.clock.Now().UnixMilli()
	agID := c.AgentID
	refKind := "proposal"

	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertProposal(ctx, app.ProposalRecord{
			ID:          propID,
			Kind:        kind,
			AgentID:     &agID,
			TargetPath:  targetPath,
			Title:       truncateChars(platform.Redact(title), 120),
			PayloadJSON: payloadJSON,
			Status:      "pending",
			CreatedAt:   nowMs,
		}); err != nil {
			return err
		}
		if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:        s.ids.New(),
			Kind:      "proposal",
			Title:     truncateChars(platform.Redact(title), 120),
			Body:      truncateChars(payloadJSON, 500),
			RefKind:   &refKind,
			RefID:     &propID,
			AgentID:   &agID,
			Urgency:   "normal",
			CreatedAt: nowMs,
		}); err != nil {
			return err
		}
		outPayload, _ := json.Marshal(map[string]string{"proposal_id": propID, "kind": kind})
		return tx.InsertOutbox(ctx, app.OutboxRecord{
			Topic:         "inbox",
			PayloadJSON:   string(outPayload),
			Urgency:       "normal",
			NextAttemptAt: nowMs,
		})
	})
	if err != nil {
		return Result{Text: fmt.Sprintf("lưu đề xuất thất bại: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Đã tạo đề xuất chờ chủ nhân duyệt (mã %s, trạng thái: tắt).", propID)}
}

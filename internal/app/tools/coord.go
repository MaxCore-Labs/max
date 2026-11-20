package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"max/internal/app"
	"max/internal/app/coord"
	"max/internal/domain"
)

func (s *Service) ensureCoord() *coord.Service {
	if s.coord != nil {
		return s.coord
	}
	return coord.NewService(coord.Deps{
		Store:  s.store,
		Actors: s.actors,
		Clock:  s.clock,
		IDs:    s.ids,
	})
}

func (s *Service) getRunCounters(runID domain.ID) domain.RunCounters {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runCounts[runID]
}

func (s *Service) incDelegateCount(runID domain.ID) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	rc := s.runCounts[runID]
	rc.Delegates++
	s.runCounts[runID] = rc
	s.mu.Unlock()
}

func (s *Service) incMessageCount(runID domain.ID) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	rc := s.runCounts[runID]
	rc.Messages++
	s.runCounts[runID] = rc
	s.mu.Unlock()
}

func (s *Service) effectiveHops(ctx context.Context, c Caller) int {
	hops := c.Hops
	if hops > 0 || c.RunID == "" {
		return hops
	}
	ws, err := s.store.Read().ListClaimedWakeupsByRun(ctx, c.RunID)
	if err != nil {
		return 0
	}
	for _, w := range ws {
		if w.Hops > hops {
			hops = w.Hops
		}
	}
	return hops
}

func (s *Service) resolveCurrentTaskID(ctx context.Context, c Caller) *domain.ID {
	if c.TaskID != nil && strings.TrimSpace(*c.TaskID) != "" {
		cp := *c.TaskID
		return &cp
	}
	if c.ConversationID == "" {
		return nil
	}
	conv, ok, err := s.store.Read().GetConversation(ctx, c.ConversationID)
	if err != nil || !ok || conv.TaskID == nil {
		return nil
	}
	return conv.TaskID
}

func (s *Service) resolveAgent(ctx context.Context, ref string) (app.AgentRecord, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return app.AgentRecord{}, false, nil
	}
	if ag, ok, err := s.store.Read().GetAgentBySlug(ctx, ref); err != nil || ok {
		return ag, ok, err
	}
	return s.store.Read().GetAgent(ctx, ref)
}

func (s *Service) reportHopsCapInbox(ctx context.Context, c Caller, hops int) {
	nowMs := s.clock.Now().UnixMilli()
	_ = s.store.WithTx(ctx, func(tx app.Tx) error {
		agID := c.AgentID
		convID := c.ConversationID
		return tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:             s.ids.New(),
			Kind:           "escalation",
			Title:          fmt.Sprintf("Chuỗi phối hợp chạm trần hops (%d/%d)", hops, domain.MaxCausalHops),
			Body:           fmt.Sprintf("Run %s của agent %s bị chặn để chống vòng lặp gián tiếp.", c.RunID, c.AgentID),
			ConversationID: &convID,
			AgentID:        &agID,
			Urgency:        "high",
			CreatedAt:      nowMs,
		})
	})
}

// TaskCreate creates a root task on behalf of the calling agent.
func (s *Service) TaskCreate(
	ctx context.Context,
	c Caller,
	title, description, doneCriteria string,
	goalID *string,
	blockedBy []string,
	needsReview bool,
	dueAt *time.Time,
) Result {
	if ok, res := s.checkPolicy(c, "task_create", domain.ToolClassC, ""); !ok {
		return res
	}
	origin := c.Origin
	if origin == "" {
		origin = domain.OriginAgent
	}
	agID := c.AgentID
	runID := c.RunID
	id, err := s.ensureCoord().Create(ctx, coord.NewTask{
		Title:          title,
		Description:    description,
		DoneCriteria:   doneCriteria,
		GoalID:         goalID,
		Assignee:       &agID,
		BlockedBy:      blockedBy,
		NeedsReview:    needsReview,
		DueAt:          dueAt,
		Origin:         origin,
		CreatedByAgent: &agID,
		CauseRunID:     &runID,
		Hops:           s.effectiveHops(ctx, c),
	})
	if err != nil {
		return Result{Text: fmt.Sprintf("task_create failed: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Created task %s (%s)", id, strings.TrimSpace(title))}
}

// TaskUpdate updates a task's status (and optional blocked_reason).
func (s *Service) TaskUpdate(ctx context.Context, c Caller, taskID, status, blockedReason string) Result {
	if ok, res := s.checkPolicy(c, "task_update", domain.ToolClassC, ""); !ok {
		return res
	}
	var rPtr *domain.BlockedReason
	if strings.TrimSpace(blockedReason) != "" {
		r := domain.BlockedReason(strings.TrimSpace(blockedReason))
		rPtr = &r
	}
	if err := s.ensureCoord().SetStatus(ctx, strings.TrimSpace(taskID), domain.TaskStatus(strings.TrimSpace(status)), rPtr, coord.Actor{
		Kind: "agent",
		ID:   c.AgentID,
	}); err != nil {
		return Result{Text: fmt.Sprintf("task_update failed: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Task %s -> %s", strings.TrimSpace(taskID), strings.TrimSpace(status))}
}

// TaskComment appends a comment to a task without @mention wakeups.
func (s *Service) TaskComment(ctx context.Context, c Caller, taskID, body string) Result {
	if ok, res := s.checkPolicy(c, "task_comment", domain.ToolClassC, ""); !ok {
		return res
	}
	if err := s.ensureCoord().Comment(ctx, strings.TrimSpace(taskID), coord.Actor{Kind: "agent", ID: c.AgentID}, body); err != nil {
		return Result{Text: fmt.Sprintf("task_comment failed: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Comment added to task %s", strings.TrimSpace(taskID))}
}

// TaskGet formats task details and comments within the 8,000-character budget, reporting remaining comments if truncated.
func (s *Service) TaskGet(ctx context.Context, c Caller, taskID string) Result {
	if ok, res := s.checkPolicy(c, "task_get", domain.ToolClassC, ""); !ok {
		return res
	}
	rd := s.store.Read()
	tk, ok, err := rd.GetTask(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return Result{Text: fmt.Sprintf("task_get failed: %v", err), IsError: true}
	}
	if !ok {
		return Result{Text: fmt.Sprintf("task %q not found", taskID), IsError: true}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Task %s: %s\nStatus: %s", tk.ID, tk.Title, tk.Status)
	if tk.BlockedReason != nil {
		fmt.Fprintf(&b, " (%s)", *tk.BlockedReason)
	}
	fmt.Fprintf(&b, "\nOrigin: %s | Depth: %d\nDescription: %s\n", tk.RootOrigin, tk.Depth, tk.Description)
	if tk.DoneCriteria != "" {
		fmt.Fprintf(&b, "Done criteria: %s\n", tk.DoneCriteria)
	}

	totalEvents, _ := rd.CountTaskEvents(ctx, tk.ID)
	evs, _ := rd.ListTaskEvents(ctx, tk.ID, 500)
	var comments []app.TaskEventRecord
	for _, ev := range evs {
		if ev.Kind == "comment" || ev.Kind == "message" || ev.Kind == "review" {
			comments = append(comments, ev)
		}
	}
	totalComments := len(comments)
	if totalEvents > len(evs) {
		totalComments += totalEvents - len(evs)
	}
	if len(comments) > 0 {
		b.WriteString("\nComments:\n")
	}
	shown := 0
	const footerReserve = 120
	for _, cm := range comments {
		actorLabel := cm.ActorKind
		if cm.ActorID != nil {
			actorLabel = *cm.ActorID
		}
		line := fmt.Sprintf("- [%s] %s\n", actorLabel, cm.Body)
		if b.Len()+len(line)+footerReserve > maxToolResultChar ||
			utf8.RuneCountInString(b.String())+utf8.RuneCountInString(line)+footerReserve > maxToolResultChar {
			break
		}
		b.WriteString(line)
		shown++
	}
	if remaining := totalComments - shown; remaining > 0 {
		fmt.Fprintf(&b, "\n... (còn %d bình luận / %d remaining comments)\n", remaining, remaining)
	}
	return s.CapOutput(c, "task_get", b.String())
}

// TaskList lists tasks scoped to the caller's own team or tasks created/assigned/reviewed by the caller.
func (s *Service) TaskList(ctx context.Context, c Caller, status string, limit int) Result {
	if ok, res := s.checkPolicy(c, "task_list", domain.ToolClassC, ""); !ok {
		return res
	}
	rd := s.store.Read()
	ag, _, _ := rd.GetAgent(ctx, c.AgentID)
	rows, err := rd.ListScopedTasks(ctx, app.TaskFilter{
		ViewerAgentID: c.AgentID,
		ViewerTeamID:  ag.TeamID,
		Status:        strings.TrimSpace(status),
		Limit:         limit,
	})
	if err != nil {
		return Result{Text: fmt.Sprintf("task_list failed: %v", err), IsError: true}
	}
	if len(rows) == 0 {
		return Result{Text: "No tasks found."}
	}
	var b strings.Builder
	for _, tk := range rows {
		fmt.Fprintf(&b, "- %s [%s] %s\n", tk.ID, tk.Status, tk.Title)
	}
	return s.CapOutput(c, "task_list", b.String())
}

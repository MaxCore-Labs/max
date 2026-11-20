package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/coord"
	"max/internal/domain"
)

func (s *Server) ListTasks(ctx context.Context, req gen.ListTasksRequestObject) (gen.ListTasksResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListTasks400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	ts, id := parsePageCursor(req.Params.Cursor)
	f := app.TaskFilter{
		Limit:    limit + 1,
		BeforeTS: ts,
		BeforeID: id,
	}
	if req.Params.Status != nil {
		f.Status = strings.TrimSpace(*req.Params.Status)
	}
	if req.Params.AssigneeId != nil {
		f.AssigneeID = strings.TrimSpace(*req.Params.AssigneeId)
	}
	if req.Params.TeamId != nil && strings.TrimSpace(*req.Params.TeamId) != "" {
		rawTeam := strings.TrimSpace(*req.Params.TeamId)
		if tm, ok, _ := s.deps.Store.Read().GetTeamBySlug(ctx, rawTeam); ok {
			f.TeamID = tm.ID
		} else {
			f.TeamID = rawTeam
		}
	}
	if req.Params.GoalId != nil {
		f.GoalID = strings.TrimSpace(*req.Params.GoalId)
	}
	if req.Params.RootId != nil {
		f.RootID = strings.TrimSpace(*req.Params.RootId)
	}

	// ListScopedTasks caps Limit at 100; handle limit==100 safely by fetching 100 and checking extra if needed.
	queryLimit := limit + 1
	if queryLimit > 100 {
		queryLimit = 100
	}
	f.Limit = queryLimit
	rows, err := s.deps.Store.Read().ListScopedTasks(ctx, f)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		next = makePageCursor(rows[limit-1].UpdatedAt, rows[limit-1].ID)
		rows = rows[:limit]
	} else if limit == 100 && len(rows) == 100 {
		last := rows[99]
		tsCheck := last.UpdatedAt
		more, _ := s.deps.Store.Read().ListScopedTasks(ctx, app.TaskFilter{
			TeamID: f.TeamID, Status: f.Status, AssigneeID: f.AssigneeID,
			GoalID: f.GoalID, RootID: f.RootID, Limit: 1, BeforeTS: &tsCheck, BeforeID: last.ID,
		})
		if len(more) > 0 {
			next = makePageCursor(last.UpdatedAt, last.ID)
		}
	}

	items := make([]gen.Task, 0, len(rows))
	for _, tk := range rows {
		items = append(items, s.toGenTask(ctx, tk, false))
	}
	return gen.ListTasks200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) GetTask(ctx context.Context, req gen.GetTaskRequestObject) (gen.GetTaskResponseObject, error) {
	tk, ok, err := s.deps.Store.Read().GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetTask404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "task not found"}}, nil
	}
	return gen.GetTask200JSONResponse(s.toGenTask(ctx, tk, true)), nil
}

func (s *Server) GetTaskTree(ctx context.Context, req gen.GetTaskTreeRequestObject) (gen.GetTaskTreeResponseObject, error) {
	tk, ok, err := s.deps.Store.Read().GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetTaskTree404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "task not found"}}, nil
	}
	node, err := s.buildTaskTree(ctx, tk, 0)
	if err != nil {
		return nil, err
	}
	return gen.GetTaskTree200JSONResponse(node), nil
}

func (s *Server) buildTaskTree(ctx context.Context, tk app.TaskRecord, relDepth int) (gen.TaskTreeNode, error) {
	node := gen.TaskTreeNode{
		Task:     s.toGenTask(ctx, tk, false),
		Children: []gen.TaskTreeNode{},
	}
	if relDepth >= domain.MaxTaskDepth {
		return node, nil
	}
	children, err := s.deps.Store.Read().ListChildTasks(ctx, tk.ID)
	if err != nil {
		return node, err
	}
	for _, ch := range children {
		sub, err := s.buildTaskTree(ctx, ch, relDepth+1)
		if err != nil {
			return node, err
		}
		node.Children = append(node.Children, sub)
	}
	return node, nil
}

func (s *Server) CreateTask(ctx context.Context, req gen.CreateTaskRequestObject) (gen.CreateTaskResponseObject, error) {
	if req.Body == nil || s.deps.Coord == nil {
		return gen.CreateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	var desc, doneCrit string
	if req.Body.Description != nil {
		desc = *req.Body.Description
	}
	if req.Body.DoneCriteria != nil {
		doneCrit = *req.Body.DoneCriteria
	}
	var blockedBy []domain.ID
	if req.Body.BlockedBy != nil {
		blockedBy = *req.Body.BlockedBy
	}
	needsReview := false
	if req.Body.NeedsReview != nil {
		needsReview = *req.Body.NeedsReview
	}
	var dueAt *time.Time
	if req.Body.DueAt != nil {
		t := time.UnixMilli(*req.Body.DueAt)
		dueAt = &t
	}
	var assignee *domain.ID
	if req.Body.AssigneeAgentId != nil && strings.TrimSpace(*req.Body.AssigneeAgentId) != "" {
		ag, ok, _ := s.resolveAgent(ctx, strings.TrimSpace(*req.Body.AssigneeAgentId))
		if !ok {
			return gen.CreateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "assignee agent not found"}}, nil
		}
		assignee = &ag.ID
	}

	taskID, err := s.deps.Coord.Create(ctx, coord.NewTask{
		Title:        req.Body.Title,
		Description:  desc,
		DoneCriteria: doneCrit,
		GoalID:       req.Body.GoalId,
		ParentID:     req.Body.ParentId,
		Assignee:     assignee,
		BlockedBy:    blockedBy,
		NeedsReview:  needsReview,
		DueAt:        dueAt,
		Origin:       domain.OriginOwner,
	})
	if err != nil {
		return gen.CreateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	_ = s.appendAudit(ctx, "task.create", taskID, map[string]string{"title": req.Body.Title})
	tk, _, _ := s.deps.Store.Read().GetTask(ctx, taskID)
	return gen.CreateTask201JSONResponse(s.toGenTask(ctx, tk, true)), nil
}

func (s *Server) UpdateTask(ctx context.Context, req gen.UpdateTaskRequestObject) (gen.UpdateTaskResponseObject, error) {
	if req.Body == nil || s.deps.Coord == nil {
		return gen.UpdateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	tk, ok, err := s.deps.Store.Read().GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.UpdateTask404JSONResponse{Error: "task not found"}, nil
	}

	ownerActor := coord.Actor{Kind: "owner"}
	if req.Body.AssigneeAgentId != nil {
		var newAssignee *domain.ID
		if strings.TrimSpace(*req.Body.AssigneeAgentId) != "" {
			ag, aok, _ := s.resolveAgent(ctx, strings.TrimSpace(*req.Body.AssigneeAgentId))
			if !aok {
				return gen.UpdateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "assignee agent not found"}}, nil
			}
			newAssignee = &ag.ID
		}
		nowMs := s.deps.Clock.Now().UnixMilli()
		var curReason *domain.BlockedReason
		if tk.BlockedReason != nil {
			r := domain.BlockedReason(*tk.BlockedReason)
			curReason = &r
		}
		if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
			return tx.UpdateTaskAssignee(ctx, tk.ID, newAssignee, domain.TaskStatus(tk.Status), curReason, nowMs)
		}); err != nil {
			return gen.UpdateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
		}
	}

	if req.Body.Status != nil && strings.TrimSpace(*req.Body.Status) != "" {
		to := domain.TaskStatus(strings.TrimSpace(*req.Body.Status))
		if to == domain.TaskCancelled {
			if err := s.deps.Coord.Cancel(ctx, tk.ID, ownerActor); err != nil {
				return gen.UpdateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
			}
		} else {
			var reason *domain.BlockedReason
			if req.Body.BlockedReason != nil && strings.TrimSpace(*req.Body.BlockedReason) != "" {
				r := domain.BlockedReason(strings.TrimSpace(*req.Body.BlockedReason))
				reason = &r
			}
			if err := s.deps.Coord.SetStatus(ctx, tk.ID, to, reason, ownerActor); err != nil {
				return gen.UpdateTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
			}
		}
	}

	_ = s.appendAudit(ctx, "task.update", tk.ID, req.Body)
	updated, _, _ := s.deps.Store.Read().GetTask(ctx, tk.ID)
	return gen.UpdateTask200JSONResponse(s.toGenTask(ctx, updated, true)), nil
}

func (s *Server) CommentTask(ctx context.Context, req gen.CommentTaskRequestObject) (gen.CommentTaskResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Body) == "" || s.deps.Coord == nil {
		return gen.CommentTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "body is required"}}, nil
	}
	if _, ok, err := s.deps.Store.Read().GetTask(ctx, req.TaskId); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return gen.CommentTask404JSONResponse{Error: "task not found"}, nil
	}
	if err := s.deps.Coord.Comment(ctx, req.TaskId, coord.Actor{Kind: "owner"}, req.Body.Body); err != nil {
		return gen.CommentTask400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	evs, _ := s.deps.Store.Read().ListTaskEvents(ctx, req.TaskId, 500)
	if len(evs) == 0 {
		return gen.CommentTask201JSONResponse{}, nil
	}
	return gen.CommentTask201JSONResponse(toGenTaskEvent(evs[len(evs)-1])), nil
}

func (s *Server) ListTaskEvents(ctx context.Context, req gen.ListTaskEventsRequestObject) (gen.ListTaskEventsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListTaskEvents400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	if _, ok, err := s.deps.Store.Read().GetTask(ctx, req.TaskId); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return gen.ListTaskEvents404JSONResponse{Error: "task not found"}, nil
	}
	ts, id := parsePageCursor(req.Params.Cursor)
	rows, err := s.deps.Store.Read().ListTaskEventsPage(ctx, req.TaskId, limit+1, ts, id)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		next = makePageCursor(rows[limit-1].CreatedAt, rows[limit-1].ID)
		rows = rows[:limit]
	}
	items := make([]gen.TaskEvent, 0, len(rows))
	for _, ev := range rows {
		items = append(items, toGenTaskEvent(ev))
	}
	return gen.ListTaskEvents200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) toGenTask(ctx context.Context, tk app.TaskRecord, includeArtifacts bool) gen.Task {
	blockers, _ := s.deps.Store.Read().ListTaskBlockers(ctx, tk.ID)
	if blockers == nil {
		blockers = []string{}
	}
	artifacts := []string{}
	if includeArtifacts && s.deps.Vault != nil && tk.AssigneeAgentID != nil {
		if ag, ok, _ := s.deps.Store.Read().GetAgent(ctx, *tk.AssigneeAgentID); ok {
			if entries, err := s.deps.Vault.ListDir(ctx, fmt.Sprintf("agents/%s/artifacts", ag.Slug)); err == nil {
				for _, name := range entries {
					artifacts = append(artifacts, fmt.Sprintf("agents/%s/artifacts/%s", ag.Slug, name))
				}
			}
		}
	}
	return gen.Task{
		Id:              tk.ID,
		GoalId:          tk.GoalID,
		ParentId:        tk.ParentID,
		RootId:          tk.RootID,
		RootOrigin:      string(tk.RootOrigin),
		Depth:           tk.Depth,
		Title:           tk.Title,
		Description:     tk.Description,
		DoneCriteria:    tk.DoneCriteria,
		Status:          tk.Status,
		BlockedReason:   tk.BlockedReason,
		AssigneeAgentId: tk.AssigneeAgentID,
		CreatedByAgent:  tk.CreatedByAgent,
		CheckoutRunId:   tk.CheckoutRunID,
		NeedsReview:     tk.NeedsReview,
		ReviewerAgentId: tk.ReviewerAgentID,
		ReviewRounds:    tk.ReviewRounds,
		IdleRuns:        tk.IdleRuns,
		DueAt:           tk.DueAt,
		BlockedBy:       blockers,
		Artifacts:       artifacts,
		CreatedAt:       tk.CreatedAt,
		UpdatedAt:       tk.UpdatedAt,
	}
}

func toGenTaskEvent(ev app.TaskEventRecord) gen.TaskEvent {
	return gen.TaskEvent{
		Id:            ev.ID,
		TaskId:        ev.TaskID,
		Kind:          ev.Kind,
		ActorKind:     ev.ActorKind,
		ActorId:       ev.ActorID,
		TargetAgentId: ev.TargetAgentID,
		Body:          ev.Body,
		CreatedAt:     ev.CreatedAt,
	}
}

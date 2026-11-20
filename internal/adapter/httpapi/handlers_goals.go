package httpapi

import (
	"context"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
)

func (s *Server) ListGoals(ctx context.Context, req gen.ListGoalsRequestObject) (gen.ListGoalsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListGoals400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	status := ""
	if req.Params.Status != nil {
		status = strings.TrimSpace(*req.Params.Status)
	}
	ts, id := parsePageCursor(req.Params.Cursor)
	rows, err := s.deps.Store.Read().ListGoalsPage(ctx, status, limit+1, ts, id)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		next = makePageCursor(rows[limit-1].CreatedAt, rows[limit-1].ID)
		rows = rows[:limit]
	}
	items := make([]gen.Goal, 0, len(rows))
	for _, g := range rows {
		items = append(items, gen.Goal{
			Id:          g.ID,
			Title:       g.Title,
			Description: g.Description,
			Status:      g.Status,
			CreatedAt:   g.CreatedAt,
		})
	}
	return gen.ListGoals200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) CreateGoal(ctx context.Context, req gen.CreateGoalRequestObject) (gen.CreateGoalResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Title) == "" {
		return gen.CreateGoal400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "title is required"}}, nil
	}
	desc := ""
	if req.Body.Description != nil {
		desc = strings.TrimSpace(*req.Body.Description)
	}
	g := app.GoalRecord{
		ID:          s.deps.IDs.New(),
		Title:       strings.TrimSpace(req.Body.Title),
		Description: desc,
		Status:      "active",
		CreatedAt:   s.deps.Clock.Now().UnixMilli(),
	}
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertGoal(ctx, g)
	}); err != nil {
		return nil, err
	}
	_ = s.appendAudit(ctx, "goal.create", g.ID, map[string]string{"title": g.Title})
	return gen.CreateGoal201JSONResponse{
		Id:          g.ID,
		Title:       g.Title,
		Description: g.Description,
		Status:      g.Status,
		CreatedAt:   g.CreatedAt,
	}, nil
}

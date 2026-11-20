package httpapi

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/domain"
)

func (s *Server) ListRuns(ctx context.Context, req gen.ListRunsRequestObject) (gen.ListRunsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListRuns400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	agentID := ""
	if req.Params.AgentId != nil {
		agentID = strings.TrimSpace(*req.Params.AgentId)
	}
	status := ""
	if req.Params.Status != nil {
		status = strings.TrimSpace(*req.Params.Status)
	}
	ts, id := parsePageCursor(req.Params.Cursor)
	rows, err := s.deps.Store.Read().ListRunsFiltered(ctx, agentID, status, req.Params.Since, limit+1, ts, id)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		next = makePageCursor(last.StartedAt, last.ID)
		rows = rows[:limit]
	}
	items := make([]gen.Run, 0, len(rows))
	for _, r := range rows {
		items = append(items, toGenRun(r))
	}
	return gen.ListRuns200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) GetRun(ctx context.Context, req gen.GetRunRequestObject) (gen.GetRunResponseObject, error) {
	r, ok, err := s.deps.Store.Read().GetRun(ctx, req.RunId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetRun404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "run not found"}}, nil
	}
	return gen.GetRun200JSONResponse(toGenRun(r)), nil
}

func (s *Server) ListRunEvents(ctx context.Context, req gen.ListRunEventsRequestObject) (gen.ListRunEventsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListRunEvents400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	afterID := int64(0)
	if req.Params.Cursor != nil && strings.TrimSpace(*req.Params.Cursor) != "" {
		if v, err := strconv.ParseInt(strings.TrimSpace(*req.Params.Cursor), 10, 64); err == nil {
			afterID = v
		}
	}
	all, err := s.deps.Store.Read().ListRunEvents(ctx, req.RunId)
	if err != nil {
		return nil, err
	}
	var filtered []app.RunEventRecord
	for _, ev := range all {
		if ev.ID > afterID {
			filtered = append(filtered, ev)
		}
	}
	var next *string
	if len(filtered) > limit {
		nc := strconv.FormatInt(filtered[limit-1].ID, 10)
		next = &nc
		filtered = filtered[:limit]
	}
	items := make([]gen.RunEvent, 0, len(filtered))
	for _, ev := range filtered {
		items = append(items, gen.RunEvent{
			Id:        ev.ID,
			RunId:     ev.RunID,
			AgentId:   ev.AgentID,
			Type:      ev.Type,
			Data:      ev.DataJSON,
			CreatedAt: ev.CreatedAt,
		})
	}
	return gen.ListRunEvents200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) CancelRun(ctx context.Context, req gen.CancelRunRequestObject) (gen.CancelRunResponseObject, error) {
	mode := "requeue"
	if req.Body != nil && req.Body.Mode != nil && *req.Body.Mode == gen.Drop {
		mode = "drop"
	}

	if s.deps.CancelRun != nil {
		s.deps.CancelRun(req.RunId)
	}

	nowMs := s.deps.Clock.Now().UnixMilli()
	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		r, ok, err := tx.GetRun(ctx, req.RunId)
		if err != nil || !ok {
			return err
		}
		if r.Status.Terminal() && r.EndedAt != nil {
			return nil
		}

		if mode == "drop" {
			if err := tx.FinishWakeupsForRun(ctx, req.RunId, "dropped"); err != nil {
				return err
			}
		} else {
			if err := tx.RequeueWakeupsForRun(ctx, req.RunId, true); err != nil {
				return err
			}
		}

		if err := tx.ClearTaskCheckout(ctx, req.RunId, nowMs); err != nil {
			return err
		}

		endedAt := nowMs
		if err := tx.UpdateRunFinal(ctx, app.RunRecord{
			ID:               r.ID,
			Status:           domain.RunCancelled,
			Turns:            r.Turns,
			InputTokens:      r.InputTokens,
			OutputTokens:     r.OutputTokens,
			TouchedPathsJSON: r.TouchedPathsJSON,
			Error:            r.Error,
			Summary:          r.Summary,
			VaultCommit:      r.VaultCommit,
			EndedAt:          &endedAt,
		}); err != nil {
			return err
		}

		if ag, ok, err := tx.GetAgent(ctx, r.AgentID); err == nil && ok {
			_ = tx.SetAgentActiveRun(ctx, r.AgentID, nil)
			_ = tx.AdjustTeamRunning(ctx, ag.TeamID, -1)
			_ = tx.RecomputeAgentReady(ctx, r.AgentID, nowMs)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	_ = s.appendAudit(ctx, "run.cancel", req.RunId, map[string]string{"mode": mode})

	updated, _, err := s.deps.Store.Read().GetRun(ctx, req.RunId)
	if err != nil {
		return nil, err
	}
	return gen.CancelRun200JSONResponse(toGenRun(updated)), nil
}

func (s *Server) StreamRun(context.Context, gen.StreamRunRequestObject) (gen.StreamRunResponseObject, error) {
	return gen.StreamRun200TexteventStreamResponse{Body: bytes.NewReader(nil)}, nil
}

func toGenRun(r app.RunRecord) gen.Run {
	return gen.Run{
		Id:             r.ID,
		AgentId:        r.AgentID,
		ConversationId: r.ConversationID,
		TaskId:         r.TaskID,
		Status:         string(r.Status),
		Priority:       int(r.Priority),
		Engine:         string(r.Engine),
		Model:          r.Model,
		Turns:          r.Turns,
		InputTokens:    r.InputTokens,
		OutputTokens:   r.OutputTokens,
		Summary:        r.Summary,
		Error:          r.Error,
		StartedAt:      r.StartedAt,
		EndedAt:        r.EndedAt,
	}
}

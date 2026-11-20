package httpapi

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/scheduler"
	"max/internal/domain"
)

func (s *Server) ListRoutines(ctx context.Context, req gen.ListRoutinesRequestObject) (gen.ListRoutinesResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListRoutines400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	if s.deps.Vault == nil {
		return gen.ListRoutines200JSONResponse{Items: []gen.Routine{}}, nil
	}
	entries, err := s.deps.Vault.ListDir(ctx, "routines")
	if err != nil {
		return gen.ListRoutines200JSONResponse{Items: []gen.Routine{}}, nil
	}
	sort.Strings(entries)
	afterSlug := ""
	if req.Params.Cursor != nil {
		afterSlug = strings.TrimSpace(*req.Params.Cursor)
	}

	var slugs []string
	for _, name := range entries {
		if !strings.HasSuffix(name, ".md") || name[0] == '.' || name[0] == '_' {
			continue
		}
		slug := strings.TrimSuffix(name, ".md")
		if afterSlug == "" || slug > afterSlug {
			slugs = append(slugs, slug)
		}
	}
	var next *string
	if len(slugs) > limit {
		nc := slugs[limit-1]
		next = &nc
		slugs = slugs[:limit]
	}

	items := make([]gen.Routine, 0, len(slugs))
	for _, slug := range slugs {
		r, ok := s.loadGenRoutine(ctx, slug)
		if ok {
			items = append(items, r)
		}
	}
	return gen.ListRoutines200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) UpsertRoutine(ctx context.Context, req gen.UpsertRoutineRequestObject) (gen.UpsertRoutineResponseObject, error) {
	if req.Body == nil || s.deps.Vault == nil {
		return gen.UpsertRoutine400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	enabled := true
	if req.Body.Enabled != nil {
		enabled = *req.Body.Enabled
	}
	var sched, atStr, origin, notify, catchUp, body string
	var timeoutMin int
	var conns []string
	if req.Body.Schedule != nil {
		sched = *req.Body.Schedule
	}
	if req.Body.At != nil {
		atStr = *req.Body.At
	}
	if req.Body.Origin != nil {
		origin = *req.Body.Origin
	}
	if req.Body.TimeoutMin != nil {
		timeoutMin = *req.Body.TimeoutMin
	}
	if req.Body.Notify != nil {
		notify = *req.Body.Notify
	}
	if req.Body.CatchUp != nil {
		catchUp = *req.Body.CatchUp
	}
	if req.Body.Connections != nil {
		conns = *req.Body.Connections
	}
	if req.Body.Body != nil {
		body = *req.Body.Body
	}

	encoded, parsed, err := scheduler.ValidateAndEncodeRoutine(scheduler.RoutineUpsertInput{
		Slug:        req.Slug,
		Agent:       req.Body.Agent,
		Schedule:    sched,
		At:          atStr,
		Enabled:     enabled,
		Origin:      origin,
		TimeoutMin:  timeoutMin,
		Notify:      notify,
		CatchUp:     catchUp,
		Connections: conns,
		Body:        body,
	})
	if err != nil {
		var verr *scheduler.RoutineValidationError
		if errors.As(err, &verr) {
			fld := verr.Field
			return gen.UpsertRoutine400JSONResponse{
				ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: verr.Error(), Field: &fld},
			}, nil
		}
		return gen.UpsertRoutine400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}

	rel := "routines/" + parsed.Slug + ".md"
	if err := s.deps.Vault.Write(ctx, rel, encoded); err != nil {
		return nil, err
	}
	if _, err := s.deps.Vault.CommitPaths(ctx, []string{rel}, "vault(routine): upsert "+parsed.Slug); err != nil {
		return nil, err
	}

	nowMs := s.deps.Clock.Now().UnixMilli()
	_ = s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		st, ok, _ := tx.GetRoutineState(ctx, parsed.Slug)
		if !ok {
			st = app.RoutineStateRecord{Slug: parsed.Slug}
		}
		st.BadFileReported = false
		if enabled {
			st.AutoDisabled = false
			st.ConsecutiveFailures = 0
		}
		st.UpdatedAt = nowMs
		return tx.UpsertRoutineState(ctx, st)
	})
	_ = s.appendAudit(ctx, "routine.upsert", parsed.Slug, map[string]any{"agent": parsed.Agent, "origin": parsed.Origin})
	item, _ := s.loadGenRoutine(ctx, parsed.Slug)
	return gen.UpsertRoutine200JSONResponse(item), nil
}

func (s *Server) SetRoutineEnabled(ctx context.Context, req gen.SetRoutineEnabledRequestObject) (gen.SetRoutineEnabledResponseObject, error) {
	if req.Body == nil || s.deps.Vault == nil {
		return gen.SetRoutineEnabled400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	rel := "routines/" + req.Slug + ".md"
	raw, err := s.deps.Vault.Read(ctx, rel)
	if err != nil {
		return gen.SetRoutineEnabled404JSONResponse{Error: "routine not found"}, nil
	}
	rf, err := domain.ParseRoutine(req.Slug, raw)
	if err != nil {
		return gen.SetRoutineEnabled400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	var atStr string
	if rf.At != nil {
		atStr = rf.At.Format(time.RFC3339)
	}
	encoded, _, err := scheduler.ValidateAndEncodeRoutine(scheduler.RoutineUpsertInput{
		Slug:        rf.Slug,
		Agent:       rf.Agent,
		Schedule:    rf.Schedule,
		At:          atStr,
		Enabled:     req.Body.Enabled,
		Origin:      rf.Origin,
		TimeoutMin:  rf.TimeoutMin,
		Notify:      rf.Notify,
		CatchUp:     rf.CatchUp,
		Connections: rf.Connections,
		Body:        rf.Body,
	})
	if err != nil {
		return gen.SetRoutineEnabled400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	if err := s.deps.Vault.Write(ctx, rel, encoded); err != nil {
		return nil, err
	}
	if _, err := s.deps.Vault.CommitPaths(ctx, []string{rel}, "vault(routine): set enabled "+req.Slug); err != nil {
		return nil, err
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	_ = s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		st, ok, _ := tx.GetRoutineState(ctx, req.Slug)
		if !ok {
			st = app.RoutineStateRecord{Slug: req.Slug}
		}
		if req.Body.Enabled {
			st.AutoDisabled = false
			st.ConsecutiveFailures = 0
		}
		st.UpdatedAt = nowMs
		return tx.UpsertRoutineState(ctx, st)
	})
	_ = s.appendAudit(ctx, "routine.set_enabled", req.Slug, map[string]bool{"enabled": req.Body.Enabled})
	item, _ := s.loadGenRoutine(ctx, req.Slug)
	return gen.SetRoutineEnabled200JSONResponse(item), nil
}

func (s *Server) RunRoutineNow(ctx context.Context, req gen.RunRoutineNowRequestObject) (gen.RunRoutineNowResponseObject, error) {
	if s.deps.Scheduler == nil {
		return gen.RunRoutineNow404JSONResponse{Error: "scheduler unavailable"}, nil
	}
	taskID, wakeupID, err := s.deps.Scheduler.TriggerRoutineNow(ctx, req.Slug, s.deps.Clock.Now())
	if err != nil {
		if errors.Is(err, scheduler.ErrRoutineNotFound) {
			return gen.RunRoutineNow404JSONResponse{Error: "routine not found"}, nil
		}
		var busy *scheduler.RoutineBusyError
		if errors.As(err, &busy) {
			code := "routine_active"
			var runPtr, taskPtr *string
			if busy.RunID != "" {
				cp := busy.RunID
				runPtr = &cp
			}
			if busy.TaskID != "" {
				cp := busy.TaskID
				taskPtr = &cp
			}
			return gen.RunRoutineNow409JSONResponse{
				Error:        busy.Error(),
				Code:         &code,
				ActiveRunId:  runPtr,
				ActiveTaskId: taskPtr,
			}, nil
		}
		return gen.RunRoutineNow400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	_ = s.appendAudit(ctx, "routine.run_now", req.Slug, map[string]string{"taskId": taskID})
	return gen.RunRoutineNow202JSONResponse{TaskId: taskID, WakeupId: wakeupID}, nil
}

func (s *Server) loadGenRoutine(ctx context.Context, slug string) (gen.Routine, bool) {
	raw, err := s.deps.Vault.Read(ctx, "routines/"+slug+".md")
	if err != nil {
		return gen.Routine{}, false
	}
	st, _, _ := s.deps.Store.Read().GetRoutineState(ctx, slug)
	rf, pErr := domain.ParseRoutine(slug, raw)
	if pErr != nil {
		pe := pErr.Error()
		return gen.Routine{
			Slug:                slug,
			Connections:         []string{},
			ConsecutiveFailures: st.ConsecutiveFailures,
			AutoDisabled:        st.AutoDisabled,
			LastFiredAt:         st.LastFiredAt,
			ActiveTaskId:        st.ActiveTaskID,
			ParseError:          &pe,
		}, true
	}
	conns := rf.Connections
	if conns == nil {
		conns = []string{}
	}
	out := gen.Routine{
		Slug:                rf.Slug,
		Agent:               rf.Agent,
		Enabled:             rf.Enabled,
		Origin:              rf.Origin,
		TimeoutMin:          rf.TimeoutMin,
		CatchUp:             rf.CatchUp,
		Connections:         conns,
		Body:                rf.Body,
		LastFiredAt:         st.LastFiredAt,
		ActiveTaskId:        st.ActiveTaskID,
		ConsecutiveFailures: st.ConsecutiveFailures,
		AutoDisabled:        st.AutoDisabled,
	}
	if rf.Schedule != "" {
		sc := rf.Schedule
		out.Schedule = &sc
	}
	if rf.At != nil {
		atStr := rf.At.Format(time.RFC3339)
		out.At = &atStr
	}
	if rf.Notify != "" {
		nt := rf.Notify
		out.Notify = &nt
	}
	return out, true
}

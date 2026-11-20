package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	"max/internal/domain"
	"max/internal/platform"
)

func (s *Server) ListAgents(ctx context.Context, req gen.ListAgentsRequestObject) (gen.ListAgentsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListAgents400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	var f app.AgentFilter
	f.Limit = limit + 1
	if req.Params.Team != nil && strings.TrimSpace(*req.Params.Team) != "" {
		rawTeam := strings.TrimSpace(*req.Params.Team)
		if tm, ok, _ := s.deps.Store.Read().GetTeamBySlug(ctx, rawTeam); ok {
			f.TeamID = tm.ID
		} else {
			f.TeamID = rawTeam
		}
	}
	if req.Params.Template != nil {
		f.Template = strings.TrimSpace(*req.Params.Template)
	}
	if req.Params.Status != nil {
		f.Status = strings.TrimSpace(*req.Params.Status)
	}
	if req.Params.Q != nil {
		f.Query = strings.TrimSpace(*req.Params.Q)
	}
	if req.Params.Cursor != nil {
		f.AfterSlug = strings.TrimSpace(*req.Params.Cursor)
	}

	rows, err := s.deps.Store.Read().ListAgentsPage(ctx, f)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		nc := rows[limit-1].Slug
		next = &nc
		rows = rows[:limit]
	}
	items := make([]gen.Agent, 0, len(rows))
	for _, ag := range rows {
		items = append(items, toGenAgent(ag))
	}
	return gen.ListAgents200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) GetAgent(ctx context.Context, req gen.GetAgentRequestObject) (gen.GetAgentResponseObject, error) {
	ag, ok, err := s.resolveAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetAgent404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "agent not found"}}, nil
	}
	return gen.GetAgent200JSONResponse(s.toGenAgentDetail(ctx, ag)), nil
}

func (s *Server) CreateAgentFromTemplate(ctx context.Context, req gen.CreateAgentFromTemplateRequestObject) (gen.CreateAgentFromTemplateResponseObject, error) {
	if req.Body == nil || s.deps.AgentSync == nil {
		return gen.CreateAgentFromTemplate400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	var params map[string]any
	if req.Body.Params != nil {
		params = *req.Body.Params
	}
	if err := s.deps.AgentSync.Instantiate(ctx, req.Body.Template, req.Body.Slug, req.Body.Team, params); err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already registered") {
			return gen.CreateAgentFromTemplate409JSONResponse{Error: err.Error()}, nil
		}
		return gen.CreateAgentFromTemplate400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	ag, ok, err := s.deps.Store.Read().GetAgentBySlug(ctx, strings.TrimSpace(req.Body.Slug))
	if err != nil || !ok {
		return nil, fmt.Errorf("lookup instantiated agent: %w", err)
	}
	_ = s.appendAudit(ctx, "agent.create", ag.Slug, map[string]any{"template": req.Body.Template, "team": req.Body.Team})
	s.publishTeamCount(ctx, ag.TeamID)
	return gen.CreateAgentFromTemplate201JSONResponse(s.toGenAgentDetail(ctx, ag)), nil
}

func (s *Server) CreateAgentsBulk(ctx context.Context, req gen.CreateAgentsBulkRequestObject) (gen.CreateAgentsBulkResponseObject, error) {
	if req.Body == nil || s.deps.AgentSync == nil {
		return gen.CreateAgentsBulk400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	prefix := ""
	if req.Body.SlugPrefix != nil {
		prefix = *req.Body.SlugPrefix
	}
	created, probs, err := s.deps.AgentSync.InstantiateBulk(ctx, req.Body.Template, req.Body.Team, prefix, req.Body.Rows)
	if err != nil {
		return gen.CreateAgentsBulk400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	if created == nil {
		created = []string{}
	}
	genProbs := make([]gen.BulkProblem, 0, len(probs))
	for _, p := range probs {
		genProbs = append(genProbs, gen.BulkProblem{Slug: p.Slug, Path: p.Path, Message: p.Message})
	}
	_ = s.appendAudit(ctx, "agent.bulk_create", req.Body.Team, map[string]any{"template": req.Body.Template, "created": len(created)})
	if tm, ok, _ := s.deps.Store.Read().GetTeamBySlug(ctx, strings.TrimSpace(req.Body.Team)); ok {
		s.publishTeamCount(ctx, tm.ID)
	}
	return gen.CreateAgentsBulk200JSONResponse{Created: created, Problems: genProbs}, nil
}

func (s *Server) UpdateAgent(ctx context.Context, req gen.UpdateAgentRequestObject) (gen.UpdateAgentResponseObject, error) {
	if req.Body == nil || s.deps.AgentSync == nil {
		return gen.UpdateAgent400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	ag, ok, err := s.resolveAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.UpdateAgent404JSONResponse{Error: "agent not found"}, nil
	}

	unlocked, err := s.deps.AgentSync.UpdateAgentFile(ctx, ag.Slug, agentsync.AgentPatch{
		Name:               req.Body.Name,
		Team:               req.Body.Team,
		Manager:            req.Body.Manager,
		Engines:            req.Body.Engines,
		Models:             req.Body.Models,
		Autonomy:           req.Body.Autonomy,
		Web:                req.Body.Web,
		Browser:            req.Body.Browser,
		Skills:             req.Body.Skills,
		QuotaTokensDay:     req.Body.QuotaTokensDay,
		Heartbeat:          req.Body.Heartbeat,
		Sensitive:          req.Body.Sensitive,
		AllowUngatedEngine: req.Body.AllowUngatedEngine,
		Body:               req.Body.Body,
	})
	if err != nil {
		if errors.Is(err, agentsync.ErrUngatedEngine) {
			code := "ungated_engine"
			return gen.UpdateAgent409JSONResponse{Error: err.Error(), Code: &code}, nil
		}
		return gen.UpdateAgent400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}

	if unlocked || (req.Body.AllowUngatedEngine != nil && *req.Body.AllowUngatedEngine) {
		_ = s.appendAudit(ctx, "agent.unlock_engine", ag.Slug, req.Body)
	}
	_ = s.appendAudit(ctx, "agent.update", ag.Slug, req.Body)

	updated, _, _ := s.deps.Store.Read().GetAgent(ctx, ag.ID)
	s.deps.Bus.Publish(platform.Event{Topic: "agent:" + updated.ID, Type: "agent.updated", Data: map[string]string{"agentId": updated.ID}})
	return gen.UpdateAgent200JSONResponse(s.toGenAgentDetail(ctx, updated)), nil
}

func (s *Server) PauseAgent(ctx context.Context, req gen.PauseAgentRequestObject) (gen.PauseAgentResponseObject, error) {
	ag, ok, err := s.setAgentStatus(ctx, req.AgentId, domain.AgentPaused, "agent.pause")
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.PauseAgent404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "agent not found"}}, nil
	}
	return gen.PauseAgent200JSONResponse(toGenAgent(ag)), nil
}

func (s *Server) ResumeAgent(ctx context.Context, req gen.ResumeAgentRequestObject) (gen.ResumeAgentResponseObject, error) {
	ag, ok, err := s.setAgentStatus(ctx, req.AgentId, domain.AgentActive, "agent.resume")
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.ResumeAgent404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "agent not found"}}, nil
	}
	return gen.ResumeAgent200JSONResponse(toGenAgent(ag)), nil
}

func (s *Server) setAgentStatus(ctx context.Context, idOrSlug string, status domain.AgentStatus, action string) (app.AgentRecord, bool, error) {
	ag, ok, err := s.resolveAgent(ctx, idOrSlug)
	if err != nil || !ok {
		return app.AgentRecord{}, ok, err
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.SetAgentStatus(ctx, ag.ID, status); err != nil {
			return err
		}
		return tx.RecomputeAgentReady(ctx, ag.ID, nowMs)
	}); err != nil {
		return app.AgentRecord{}, false, err
	}
	_ = s.appendAudit(ctx, action, ag.Slug, map[string]string{"status": string(status)})
	updated, _, _ := s.deps.Store.Read().GetAgent(ctx, ag.ID)
	s.deps.Bus.Publish(platform.Event{Topic: "agent:" + ag.ID, Type: action, Data: map[string]string{"agentId": ag.ID, "status": string(status)}})
	s.publishTeamCount(ctx, ag.TeamID)
	return updated, true, nil
}

func (s *Server) WakeAgent(ctx context.Context, req gen.WakeAgentRequestObject) (gen.WakeAgentResponseObject, error) {
	ag, ok, err := s.resolveAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.WakeAgent404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "agent not found"}}, nil
	}
	msg := "owner manual wakeup"
	if req.Body != nil && req.Body.Message != nil && strings.TrimSpace(*req.Body.Message) != "" {
		msg = strings.TrimSpace(*req.Body.Message)
	}
	payload, _ := json.Marshal(map[string]string{"message": msg})
	var wakeID domain.ID
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		convID, err := tx.EnsureSystemConversation(ctx, ag.ID, s.deps.IDs.New())
		if err != nil {
			return err
		}
		wid, _, err := s.deps.Actors.Wake(ctx, tx, actor.WakeInput{
			AgentID:        ag.ID,
			Kind:           domain.WakeMessage,
			Priority:       domain.PrioInteractive,
			ConversationID: &convID,
			Payload:        payload,
		})
		wakeID = wid
		return err
	}); err != nil {
		return nil, err
	}
	_ = s.appendAudit(ctx, "agent.wake", ag.Slug, map[string]string{"wakeupId": wakeID})
	return gen.WakeAgent202JSONResponse{WakeupId: wakeID}, nil
}

func (s *Server) ListAgentRuns(ctx context.Context, req gen.ListAgentRunsRequestObject) (gen.ListAgentRunsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListAgentRuns400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	ag, ok, err := s.resolveAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.ListAgentRuns404JSONResponse{Error: "agent not found"}, nil
	}
	status := ""
	if req.Params.Status != nil {
		status = strings.TrimSpace(*req.Params.Status)
	}
	ts, id := parsePageCursor(req.Params.Cursor)
	rows, err := s.deps.Store.Read().ListRunsFiltered(ctx, ag.ID, status, nil, limit+1, ts, id)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		next = makePageCursor(rows[limit-1].StartedAt, rows[limit-1].ID)
		rows = rows[:limit]
	}
	items := make([]gen.Run, 0, len(rows))
	for _, r := range rows {
		items = append(items, toGenRun(r))
	}
	return gen.ListAgentRuns200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) GetAgentNotes(ctx context.Context, req gen.GetAgentNotesRequestObject) (gen.GetAgentNotesResponseObject, error) {
	ag, ok, err := s.resolveAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetAgentNotes404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "agent not found"}}, nil
	}
	var notes string
	if s.deps.Vault != nil {
		if raw, err := s.deps.Vault.Read(ctx, fmt.Sprintf("agents/%s/notes/NOTES.md", ag.Slug)); err == nil {
			notes = string(raw)
		}
	}
	return gen.GetAgentNotes200JSONResponse{Content: notes}, nil
}

func (s *Server) resolveAgent(ctx context.Context, idOrSlug string) (app.AgentRecord, bool, error) {
	if ag, ok, err := s.deps.Store.Read().GetAgent(ctx, idOrSlug); err != nil || ok {
		return ag, ok, err
	}
	return s.deps.Store.Read().GetAgentBySlug(ctx, idOrSlug)
}

func (s *Server) publishTeamCount(ctx context.Context, teamID domain.ID) {
	if count, err := s.deps.Store.Read().CountAgentsByTeam(ctx, teamID); err == nil {
		s.deps.Bus.Publish(platform.Event{
			Topic: "team:" + teamID,
			Type:  "team.counts",
			Data:  map[string]any{"teamId": teamID, "agentCount": count},
		})
	}
}

func toGenAgent(ag app.AgentRecord) gen.Agent {
	engs := []string{"agy"}
	_ = json.Unmarshal([]byte(ag.EnginesJSON), &engs)
	mods := map[string]string{}
	_ = json.Unmarshal([]byte(ag.ModelsJSON), &mods)
	skills := []string{}
	_ = json.Unmarshal([]byte(ag.SkillsJSON), &skills)
	return gen.Agent{
		Id: ag.ID, Slug: ag.Slug, Name: ag.Name, TemplateSlug: ag.TemplateSlug,
		TeamId: ag.TeamID, ManagerAgentId: ag.ManagerAgentID, Engines: engs, Models: mods,
		AllowUngatedEngine: ag.AllowUngatedEngine, ScopeViolations: ag.ScopeViolations,
		Autonomy: string(ag.Autonomy), Web: ag.Web, Browser: string(ag.Browser), Shell: ag.Shell,
		Skills: skills, QuotaTokensDay: ag.QuotaTokensDay, Status: string(ag.Status),
		Ready: ag.Ready, ActiveRunId: ag.ActiveRunID, LastRunAt: ag.LastRunAt,
	}
}

func (s *Server) toGenAgentDetail(ctx context.Context, ag app.AgentRecord) gen.AgentDetail {
	b := toGenAgent(ag)
	det := gen.AgentDetail{
		ActiveRunId: b.ActiveRunId, AllowUngatedEngine: b.AllowUngatedEngine, Autonomy: b.Autonomy,
		Browser: b.Browser, Engines: b.Engines, Id: b.Id, LastRunAt: b.LastRunAt,
		ManagerAgentId: b.ManagerAgentId, Models: b.Models, Name: b.Name, QuotaTokensDay: b.QuotaTokensDay,
		Ready: b.Ready, ScopeViolations: b.ScopeViolations, Shell: b.Shell, Skills: b.Skills,
		Slug: b.Slug, Status: b.Status, TeamId: b.TeamId, TemplateSlug: b.TemplateSlug, Web: b.Web,
	}
	if tm, ok, _ := s.deps.Store.Read().GetTeam(ctx, ag.TeamID); ok {
		det.TeamSlug = &tm.Slug
	}
	if ag.ManagerAgentID != nil {
		if mgr, ok, _ := s.deps.Store.Read().GetAgent(ctx, *ag.ManagerAgentID); ok {
			det.ManagerSlug = &mgr.Slug
		}
	}
	var params map[string]any
	if json.Unmarshal([]byte(ag.ParamsJSON), &params) == nil && len(params) > 0 {
		det.Params = &params
	}
	if s.deps.Vault != nil {
		if raw, err := s.deps.Vault.Read(ctx, fmt.Sprintf("agents/%s/AGENT.md", ag.Slug)); err == nil {
			if parsed, perr := agentsync.Parse(raw); perr == nil {
				det.Body = parsed.Body
				det.Sensitive = parsed.Sensitive
				if parsed.Heartbeat != "" {
					hb := parsed.Heartbeat
					det.Heartbeat = &hb
				}
			}
		}
	}
	return det
}

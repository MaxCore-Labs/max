package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/agentsync"
)

func (s *Server) ListTeams(ctx context.Context, req gen.ListTeamsRequestObject) (gen.ListTeamsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListTeams400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	afterSlug := ""
	if req.Params.Cursor != nil {
		afterSlug = strings.TrimSpace(*req.Params.Cursor)
	}
	rows, err := s.deps.Store.Read().ListTeamsPage(ctx, afterSlug, limit+1)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		nc := rows[limit-1].Slug
		next = &nc
		rows = rows[:limit]
	}
	items := make([]gen.Team, 0, len(rows))
	for _, r := range rows {
		items = append(items, toGenTeam(r.TeamRecord, r.AgentCount, nil))
	}
	return gen.ListTeams200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) GetTeam(ctx context.Context, req gen.GetTeamRequestObject) (gen.GetTeamResponseObject, error) {
	tm, ok, err := s.resolveTeam(ctx, req.TeamId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetTeam404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "team not found"}}, nil
	}
	cnt, _ := s.deps.Store.Read().CountAgentsByTeam(ctx, tm.ID)
	var bodyPtr *string
	if s.deps.Vault != nil {
		if raw, err := s.deps.Vault.Read(ctx, fmt.Sprintf("teams/%s/TEAM.md", tm.Slug)); err == nil {
			if tf, perr := agentsync.ParseTeam(raw); perr == nil {
				b := tf.Body
				bodyPtr = &b
			}
		}
	}
	return gen.GetTeam200JSONResponse(toGenTeam(tm, cnt, bodyPtr)), nil
}

func (s *Server) UpdateTeam(ctx context.Context, req gen.UpdateTeamRequestObject) (gen.UpdateTeamResponseObject, error) {
	if req.Body == nil || s.deps.AgentSync == nil {
		return gen.UpdateTeam400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"}}, nil
	}
	tm, ok, err := s.resolveTeam(ctx, req.TeamId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.UpdateTeam404JSONResponse{Error: "team not found"}, nil
	}

	var mandatePatch *agentsync.TeamMandate
	if req.Body.Mandate != nil {
		mandatePatch = &agentsync.TeamMandate{
			Outcome:  req.Body.Mandate.Outcome,
			Measures: req.Body.Mandate.Measures,
		}
	}
	if err := s.deps.AgentSync.UpdateTeamFile(ctx, tm.Slug, agentsync.TeamPatch{
		Name:           req.Body.Name,
		Mandate:        mandatePatch,
		MaxRunning:     req.Body.MaxRunning,
		QuotaTokensDay: req.Body.QuotaTokensDay,
		Body:           req.Body.Body,
	}); err != nil {
		return gen.UpdateTeam400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}

	_ = s.appendAudit(ctx, "team.update", tm.Slug, req.Body)
	updated, _, _ := s.deps.Store.Read().GetTeam(ctx, tm.ID)
	cnt, _ := s.deps.Store.Read().CountAgentsByTeam(ctx, updated.ID)
	s.publishTeamCount(ctx, updated.ID)
	return gen.UpdateTeam200JSONResponse(toGenTeam(updated, cnt, req.Body.Body)), nil
}

func (s *Server) ListTemplates(ctx context.Context, req gen.ListTemplatesRequestObject) (gen.ListTemplatesResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListTemplates400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	if s.deps.AgentSync == nil {
		return gen.ListTemplates200JSONResponse{Items: []gen.RoleTemplate{}}, nil
	}
	all, err := s.deps.AgentSync.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	afterSlug := ""
	if req.Params.Cursor != nil {
		afterSlug = strings.TrimSpace(*req.Params.Cursor)
	}
	var filtered []agentsync.NamedTemplate
	for _, nt := range all {
		if afterSlug == "" || nt.Slug > afterSlug {
			filtered = append(filtered, nt)
		}
	}
	var next *string
	if len(filtered) > limit {
		nc := filtered[limit-1].Slug
		next = &nc
		filtered = filtered[:limit]
	}
	items := make([]gen.RoleTemplate, 0, len(filtered))
	for _, nt := range filtered {
		engs := make([]string, len(nt.File.Engines))
		for i, e := range nt.File.Engines {
			engs[i] = string(e)
		}
		reqParams := nt.File.RequiredParams
		if reqParams == nil {
			reqParams = []string{}
		}
		var defaults *map[string]any
		if len(nt.File.ParamDefaults) > 0 {
			cp := nt.File.ParamDefaults
			defaults = &cp
		}
		items = append(items, gen.RoleTemplate{
			Slug:           nt.Slug,
			Name:           nt.File.Name,
			Engines:        engs,
			Autonomy:       string(nt.File.Autonomy),
			Web:            nt.File.Web,
			Browser:        string(nt.File.Browser),
			Sensitive:      nt.File.Sensitive,
			RequiredParams: reqParams,
			ParamDefaults:  defaults,
		})
	}
	return gen.ListTemplates200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) resolveTeam(ctx context.Context, idOrSlug string) (app.TeamRecord, bool, error) {
	if tm, ok, err := s.deps.Store.Read().GetTeam(ctx, idOrSlug); err != nil || ok {
		return tm, ok, err
	}
	return s.deps.Store.Read().GetTeamBySlug(ctx, idOrSlug)
}

func toGenTeam(tm app.TeamRecord, agentCount int, body *string) gen.Team {
	var mandate gen.TeamMandate
	_ = json.Unmarshal([]byte(tm.MandateJSON), &mandate)
	if mandate.Measures == nil {
		mandate.Measures = []string{}
	}
	return gen.Team{
		Id:             tm.ID,
		Slug:           tm.Slug,
		Name:           tm.Name,
		Mandate:        mandate,
		MaxRunning:     tm.MaxRunning,
		Running:        tm.Running,
		QuotaTokensDay: tm.QuotaTokensDay,
		Status:         tm.Status,
		AgentCount:     agentCount,
		Body:           body,
	}
}

package httpapi

import (
	"context"
	"strconv"
	"strings"
	"time"

	"max/internal/adapter/httpapi/gen"
	"max/internal/domain"
)

func (s *Server) GetUsage(ctx context.Context, req gen.GetUsageRequestObject) (gen.GetUsageResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.GetUsage400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	days := 7
	if req.Params.Days != nil && *req.Params.Days > 0 {
		days = *req.Params.Days
	}
	sinceDay := s.deps.Clock.Now().AddDate(0, 0, -days).UTC().Format("2006-01-02")

	offset := 0
	if req.Params.Cursor != nil && strings.TrimSpace(*req.Params.Cursor) != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(*req.Params.Cursor)); err == nil && v >= 0 {
			offset = v
		}
	}

	rows, err := s.deps.Store.Read().ListUsagePage(ctx, sinceDay, limit+1, offset)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		nc := strconv.Itoa(offset + limit)
		next = &nc
		rows = rows[:limit]
	}

	items := make([]gen.DailyUsageRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, gen.DailyUsageRow{
			Day:     r.Day,
			AgentId: r.AgentID,
			TeamId:  r.TeamID,
			Engine:  string(r.Engine),
			Runs:    r.Runs,
			Tokens:  r.Tokens,
		})
	}
	return gen.GetUsage200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) GetQuota(ctx context.Context, _ gen.GetQuotaRequestObject) (gen.GetQuotaResponseObject, error) {
	resp, err := s.buildQuotaResponse(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetQuota200JSONResponse(resp), nil
}

func (s *Server) UpdateQuotaSettings(ctx context.Context, req gen.UpdateQuotaSettingsRequestObject) (gen.UpdateQuotaSettingsResponseObject, error) {
	if s.deps.Quota != nil && req.Body != nil {
		shortHours := 0
		if req.Body.ShortHours != nil {
			shortHours = *req.Body.ShortHours
		}
		bgShare := 0.0
		if req.Body.BackgroundShare != nil {
			bgShare = *req.Body.BackgroundShare
		}
		s.deps.Quota.UpdateSettings(shortHours, bgShare, req.Body.EstShortTokens, req.Body.EstWeekTokens)
		_ = s.appendAudit(ctx, "quota.update", "quota", req.Body)
	}
	resp, err := s.buildQuotaResponse(ctx)
	if err != nil {
		return nil, err
	}
	return gen.UpdateQuotaSettings200JSONResponse(resp), nil
}

func (s *Server) buildQuotaResponse(ctx context.Context) (gen.QuotaResponse, error) {
	shortHours := 5
	bgShare := 0.7
	if s.deps.Quota != nil {
		cfg := s.deps.Quota.ConfigSnapshot()
		shortHours = cfg.ShortHours
		bgShare = cfg.BackgroundShare
	}

	now := s.deps.Clock.Now()
	var engines []gen.EngineQuotaInfo
	for _, eng := range []domain.EngineName{domain.EngineAgy, domain.EngineClaude, domain.EngineCodex} {
		var view domain.QuotaView
		if s.deps.Quota != nil {
			v, err := s.deps.Quota.View(ctx, eng, now)
			if err != nil {
				return gen.QuotaResponse{}, err
			}
			view = v
		}
		var limMs *int64
		if view.LimitedUntil != nil && view.LimitedUntil.After(time.Time{}) {
			u := view.LimitedUntil.UnixMilli()
			limMs = &u
		}
		engines = append(engines, gen.EngineQuotaInfo{
			Engine:       string(eng),
			UsedShort:    view.UsedShort,
			UsedWeek:     view.UsedWeek,
			EstShort:     view.EstShort,
			EstWeek:      view.EstWeek,
			LimitedUntil: limMs,
		})
	}
	return gen.QuotaResponse{
		ShortHours:      shortHours,
		BackgroundShare: bgShare,
		Engines:         engines,
	}, nil
}

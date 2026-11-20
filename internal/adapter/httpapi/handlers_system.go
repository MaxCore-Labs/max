package httpapi

import (
	"context"
	"encoding/json"
	"strconv"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/domain"
)

const kvEngineSettingsKey = "settings.engines"

type persistedSettings struct {
	DefaultEngine string                  `json:"defaultEngine"`
	Engines       []gen.EngineSettingItem `json:"engines"`
}

func (s *Server) GetStatus(ctx context.Context, _ gen.GetStatusRequestObject) (gen.GetStatusResponseObject, error) {
	caps := map[domain.EngineName]int{domain.EngineAgy: 3}
	used := map[domain.EngineName]int{}
	enabled := map[domain.EngineName]bool{domain.EngineAgy: true}
	if s.deps.Slots != nil {
		caps, used, enabled = s.deps.Slots.Snapshot()
	}

	now := s.deps.Clock.Now()
	var engList []gen.EngineStatusItem
	for _, eng := range []domain.EngineName{domain.EngineAgy, domain.EngineClaude, domain.EngineCodex} {
		c, exists := caps[eng]
		if !exists && s.deps.Engines[eng] == nil {
			continue
		}
		var h app.EngineHealth
		if drv := s.deps.Engines[eng]; drv != nil {
			h = drv.Health(ctx)
		}
		var limUntil *int64
		if s.deps.Quota != nil {
			if v, err := s.deps.Quota.View(ctx, eng, now); err == nil && v.LimitedUntil != nil {
				u := v.LimitedUntil.UnixMilli()
				limUntil = &u
			}
		}
		ver := h.Version
		engList = append(engList, gen.EngineStatusItem{
			Name:         string(eng),
			Enabled:      enabled[eng],
			Installed:    h.Installed,
			LoggedIn:     h.LoggedIn,
			Version:      &ver,
			Slots:        c,
			Used:         used[eng],
			LimitedUntil: limUntil,
		})
	}

	byPrioRaw, err := s.deps.Store.Read().CountQueuedByPriority(ctx)
	if err != nil {
		return nil, err
	}
	byPrio := make(map[string]int, len(byPrioRaw))
	totalQueued := 0
	for p, n := range byPrioRaw {
		byPrio[strconv.Itoa(p)] = n
		totalQueued += n
	}

	backlogRows, err := s.deps.Store.Read().ListQueuedBacklogStats(ctx)
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	backlog := make([]gen.PriorityBacklogItem, 0, len(backlogRows))
	quotaDelayed := 0
	for _, row := range backlogRows {
		ageSec := max(int64(0), (nowMs-row.OldestCreatedAt)/1000)
		reason := s.classifyBacklogReason(ctx, row, caps, used, enabled, nowMs)
		if reason == "quota_limited" {
			quotaDelayed += row.Count
		}
		backlog = append(backlog, gen.PriorityBacklogItem{
			Priority:     row.Priority,
			Count:        row.Count,
			OldestAgeSec: ageSec,
			Reason:       reason,
		})
	}

	warnings := s.computeStatusWarnings(ctx)
	var warnPtr *[]string
	if len(warnings) > 0 {
		warnPtr = &warnings
	}

	return gen.GetStatus200JSONResponse{
		Engines: engList,
		Queue: gen.QueueSummary{
			Total:        totalQueued,
			ByPriority:   byPrio,
			QuotaDelayed: &quotaDelayed,
			Backlog:      &backlog,
		},
		Deps: map[string]string{
			"sqlite": "ok",
		},
		Warnings: warnPtr,
	}, nil
}

func (s *Server) classifyBacklogReason(
	ctx context.Context,
	row app.QueuedBacklogRow,
	caps, used map[domain.EngineName]int,
	enabled map[domain.EngineName]bool,
	nowMs int64,
) string {
	if row.Priority < int(domain.PrioInteractive) && row.CooldownUntil > nowMs {
		return "cooldown"
	}
	if row.AgentStatus == domain.AgentQuotaPaused {
		return "quota_limited"
	}

	eligibleCount := 0
	quotaBlockedCount := 0
	slotsFullCount := 0
	prio := domain.Priority(row.Priority)

	for _, eng := range []domain.EngineName{domain.EngineAgy, domain.EngineClaude, domain.EngineCodex} {
		if !enabled[eng] || (row.EngineMask&domain.EngineBit(eng)) == 0 {
			continue
		}
		eligibleCount++
		if s.engineQuotaHeld(ctx, eng, prio, nowMs) {
			quotaBlockedCount++
			continue
		}
		total := caps[eng]
		limit := total
		if total >= 2 && prio < domain.PrioInteractive {
			limit = total - 1
		}
		if used[eng] >= limit {
			slotsFullCount++
		}
	}

	if eligibleCount > 0 && quotaBlockedCount == eligibleCount {
		return "quota_limited"
	}
	if (row.ActiveRunID != nil && *row.ActiveRunID != "") ||
		(row.TeamMaxRunning > 0 && row.TeamRunning >= row.TeamMaxRunning) ||
		(eligibleCount > 0 && quotaBlockedCount+slotsFullCount == eligibleCount) {
		return "slots_full"
	}
	return "ready"
}

func (s *Server) engineQuotaHeld(ctx context.Context, eng domain.EngineName, prio domain.Priority, nowMs int64) bool {
	if s.deps.Quota != nil {
		return s.deps.Quota.Admit(eng, prio) != domain.AdmitNow
	}
	if qs, ok, err := s.deps.Store.Read().GetQuotaState(ctx, eng); err == nil && ok && qs.LimitedUntil != nil {
		return *qs.LimitedUntil > nowMs
	}
	return false
}

func (s *Server) GetSettings(ctx context.Context, _ gen.GetSettingsRequestObject) (gen.GetSettingsResponseObject, error) {
	ps := s.loadCurrentSettings(ctx)
	return gen.GetSettings200JSONResponse{
		DefaultEngine: ps.DefaultEngine,
		Engines:       ps.Engines,
	}, nil
}

func (s *Server) UpdateSettings(ctx context.Context, req gen.UpdateSettingsRequestObject) (gen.UpdateSettingsResponseObject, error) {
	if req.Body == nil {
		return gen.UpdateSettings400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing settings body"},
		}, nil
	}

	cur := s.loadCurrentSettings(ctx)
	if req.Body.DefaultEngine != nil && *req.Body.DefaultEngine != "" {
		de := domain.EngineName(*req.Body.DefaultEngine)
		if domain.EngineBit(de) == 0 {
			return gen.UpdateSettings400JSONResponse{
				ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "invalid defaultEngine"},
			}, nil
		}
		cur.DefaultEngine = *req.Body.DefaultEngine
	}

	if req.Body.Engines != nil {
		newCaps := make(map[domain.EngineName]int)
		newEnabled := make(map[domain.EngineName]bool)
		for _, it := range *req.Body.Engines {
			en := domain.EngineName(it.Name)
			if domain.EngineBit(en) == 0 {
				return gen.UpdateSettings400JSONResponse{
					ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "unknown engine " + it.Name},
				}, nil
			}
			if it.Slots < 1 || it.Slots > 8 {
				return gen.UpdateSettings400JSONResponse{
					ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "slots must be between 1 and 8"},
				}, nil
			}
			newCaps[en] = it.Slots
			newEnabled[en] = it.Enabled
		}

		if s.deps.Slots != nil {
			if err := s.deps.Slots.UpdateEngine(newCaps, newEnabled); err != nil {
				return gen.UpdateSettings400JSONResponse{
					ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()},
				}, nil
			}
		} else {
			anyOn := false
			for _, on := range newEnabled {
				if on {
					anyOn = true
				}
			}
			if !anyOn {
				return gen.UpdateSettings400JSONResponse{
					ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "at least one engine must remain enabled"},
				}, nil
			}
		}
		cur.Engines = *req.Body.Engines
	}

	raw, _ := json.Marshal(cur)
	nowMs := s.deps.Clock.Now().UnixMilli()
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.UpsertKV(ctx, kvEngineSettingsKey, string(raw), nowMs)
	}); err != nil {
		return nil, err
	}
	_ = s.appendAudit(ctx, "settings.update", kvEngineSettingsKey, cur)

	return gen.UpdateSettings200JSONResponse{
		DefaultEngine: cur.DefaultEngine,
		Engines:       cur.Engines,
	}, nil
}

func (s *Server) loadCurrentSettings(ctx context.Context) persistedSettings {
	if raw, ok, err := s.deps.Store.Read().GetKV(ctx, kvEngineSettingsKey); err == nil && ok {
		var ps persistedSettings
		if json.Unmarshal([]byte(raw), &ps) == nil && len(ps.Engines) > 0 {
			return ps
		}
	}
	caps := map[domain.EngineName]int{domain.EngineAgy: 3}
	enabled := map[domain.EngineName]bool{domain.EngineAgy: true}
	if s.deps.Slots != nil {
		caps, _, enabled = s.deps.Slots.Snapshot()
	}
	var items []gen.EngineSettingItem
	for _, eng := range []domain.EngineName{domain.EngineAgy, domain.EngineClaude, domain.EngineCodex} {
		c, ok := caps[eng]
		if !ok {
			continue
		}
		items = append(items, gen.EngineSettingItem{
			Name:         string(eng),
			Enabled:      enabled[eng],
			Slots:        c,
			DefaultModel: "default",
		})
	}
	return persistedSettings{
		DefaultEngine: string(domain.EngineAgy),
		Engines:       items,
	}
}

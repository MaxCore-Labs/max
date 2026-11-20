package agentsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"max/internal/app"
	"max/internal/domain"
)

// Problem reports a per-agent validation or sync error without halting other agents.
type Problem struct {
	Slug    string
	Path    string
	Message string
}

// Service synchronizes vault agents/<slug>/AGENT.md files into the SQLite agents table.
type Service struct {
	store app.Store
	vault app.Vault
	clock app.Clock
	ids   app.IDGen
}

// NewService constructs an agentsync Service.
func NewService(store app.Store, vault app.Vault, clock app.Clock, ids app.IDGen) *Service {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Service{
		store: store,
		vault: vault,
		clock: clock,
		ids:   ids,
	}
}

// SyncAll scans teams/*/TEAM.md and agents/*/AGENT.md, preserves previous DB rows on broken/ungated configs while creating Inbox alerts,
// and updates changed agents idempotently by config_rev.
func (s *Service) SyncAll(ctx context.Context) (updated int, problems []Problem, err error) {
	teamProbs, err := s.syncTeams(ctx)
	if err != nil {
		return 0, nil, err
	}
	problems = append(problems, teamProbs...)

	entries, err := s.vault.ListDir(ctx, "agents")
	if err != nil {
		return 0, nil, fmt.Errorf("list vault agents: %w", err)
	}
	sort.Strings(entries)

	type validAgent struct {
		slug string
		rev  string
		file AgentFile
	}
	var valid []validAgent

	for _, slug := range entries {
		if slug == "" || slug[0] == '.' || slug[0] == '_' {
			continue
		}
		rel := fmt.Sprintf("agents/%s/AGENT.md", slug)
		raw, readErr := s.vault.Read(ctx, rel)
		if readErr != nil {
			continue
		}

		sum := sha256.Sum256(raw)
		rev := hex.EncodeToString(sum[:])

		af, parseErr := Parse(raw)
		if parseErr != nil {
			problems = append(problems, Problem{Slug: slug, Path: rel, Message: parseErr.Error()})
			_ = s.reportSyncProblem(ctx, slug, rel, rev, parseErr.Error())
			continue
		}

		if errGate := checkSensitiveGate(af); errGate != nil {
			problems = append(problems, Problem{Slug: slug, Path: rel, Message: errGate.Error()})
			_ = s.reportSyncProblem(ctx, slug, rel, rev, errGate.Error())
			continue
		}

		valid = append(valid, validAgent{slug: slug, rev: rev, file: af})
	}

	if len(valid) == 0 {
		return 0, problems, nil
	}

	err = s.store.WithTx(ctx, func(tx app.Tx) error {
		managerLinks := make(map[string]string)

		for _, item := range valid {
			existing, exists, err := tx.GetAgentBySlug(ctx, item.slug)
			if err != nil {
				return err
			}
			if item.file.Manager != "" {
				managerLinks[item.slug] = item.file.Manager
			}
			if exists && existing.ConfigRev == item.rev {
				continue
			}

			teamID, err := tx.EnsureTeamBySlug(ctx, item.file.Team, s.ids.New())
			if err != nil {
				return err
			}

			agentID := s.ids.New()
			status := domain.AgentActive
			if exists {
				agentID = existing.ID
				status = existing.Status
			}

			var tmplPtr *string
			if item.file.Template != "" {
				cp := item.file.Template
				tmplPtr = &cp
			}
			enginesBytes, _ := json.Marshal(item.file.Engines)
			modelsBytes, _ := json.Marshal(item.file.Models)
			skills := item.file.Skills
			if skills == nil {
				skills = []string{}
			}
			skillsBytes, _ := json.Marshal(skills)
			params := item.file.Params
			if params == nil {
				params = map[string]any{}
			}
			paramsBytes, _ := json.Marshal(params)

			rec := app.AgentRecord{
				ID:                 agentID,
				Slug:               item.slug,
				Name:               item.file.Name,
				TemplateSlug:       tmplPtr,
				TeamID:             teamID,
				EnginesJSON:        string(enginesBytes),
				EngineMask:         domain.EngineMask(item.file.Engines),
				AllowUngatedEngine: item.file.AllowUngatedEngine,
				ModelsJSON:         string(modelsBytes),
				Autonomy:           item.file.Autonomy,
				Web:                item.file.Web,
				Browser:            item.file.Browser,
				Shell:              item.file.Template == "developer",
				SkillsJSON:         string(skillsBytes),
				ParamsJSON:         string(paramsBytes),
				QuotaTokensDay:     item.file.QuotaTokensDay,
				ConfigRev:          item.rev,
				Status:             status,
			}
			if err := tx.UpsertSyncedAgent(ctx, rec); err != nil {
				return err
			}
			updated++
		}

		for slug, mgrSlug := range managerLinks {
			ag, ok1, err := tx.GetAgentBySlug(ctx, slug)
			if err != nil || !ok1 {
				continue
			}
			mgr, ok2, err := tx.GetAgentBySlug(ctx, mgrSlug)
			if err != nil || !ok2 || mgr.ID == ag.ID {
				continue
			}
			mgrID := mgr.ID
			if ag.ManagerAgentID == nil || *ag.ManagerAgentID != mgrID {
				if err := tx.SetAgentManager(ctx, ag.ID, &mgrID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return updated, problems, err
}

func checkSensitiveGate(af AgentFile) error {
	if (af.Browser != domain.BrowserOwner && !af.Sensitive && af.Template != "developer") || af.AllowUngatedEngine {
		return nil
	}
	for _, e := range af.Engines {
		if lvl := engineIsolationLevel(e); lvl > 1 {
			return fmt.Errorf(
				"sensitive role or browser: owner is not allowed on level-%d engine %q without allow_ungated_engine: true",
				lvl, e,
			)
		}
	}
	return nil
}

func (s *Service) reportSyncProblem(ctx context.Context, slug, rel, rev, msg string) error {
	nowMs := s.clock.Now().UnixMilli()
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		refKind := "agent_file"
		refID := slug
		dedupe := fmt.Sprintf("agentsync:%s:%s", slug, rev)
		return tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:        s.ids.New(),
			Kind:      "error",
			Title:     fmt.Sprintf("Lỗi đồng bộ %s", rel),
			Body:      msg,
			RefKind:   &refKind,
			RefID:     &refID,
			Urgency:   "high",
			DedupeKey: &dedupe,
			CreatedAt: nowMs,
		})
	})
}

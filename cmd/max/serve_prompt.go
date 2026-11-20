package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"max/internal/adapter/mcpserver"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	"max/internal/app/prompt"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/platform"
	"max/prompts"
)

func resolveActiveSlots(cfg platform.Config, engines map[domain.EngineName]app.Engine) map[domain.EngineName]int {
	activeSlots := make(map[domain.EngineName]int, len(engines))
	if len(cfg.Engines) > 0 {
		for _, name := range cfg.Engines {
			if n, ok := cfg.Slots[name]; ok && n > 0 {
				activeSlots[name] = n
			} else {
				activeSlots[name] = 3
			}
		}
		return activeSlots
	}
	for name := range engines {
		if n, ok := cfg.Slots[name]; ok && n > 0 {
			activeSlots[name] = n
		} else {
			activeSlots[name] = 3
		}
	}
	return activeSlots
}

func registerActiveMCPRun(ctx context.Context, st app.Store, mcpSrv *mcpserver.Server, c actor.Claimed, slug, token string) {
	if mcpSrv == nil {
		return
	}
	ag, ok, _ := st.Read().GetAgent(ctx, c.AgentID)
	var teamSlug string
	if ok {
		if tm, tok, _ := st.Read().GetTeam(ctx, ag.TeamID); tok {
			teamSlug = tm.Slug
		}
	}
	origin := domain.OriginOwner
	if c.TaskID != nil && *c.TaskID != "" {
		if tk, tok, _ := st.Read().GetTask(ctx, *c.TaskID); tok && tk.RootOrigin != "" {
			origin = tk.RootOrigin
		}
	}
	maxHops := 0
	for _, w := range c.Wakeups {
		if w.Hops > maxHops {
			maxHops = w.Hops
		}
	}
	mcpSrv.RegisterRun(tools.Caller{
		RunID:          c.RunID,
		AgentID:        c.AgentID,
		ConversationID: c.ConversationID,
		TaskID:         c.TaskID,
		Hops:           maxHops,
		Origin:         origin,
		Policy: domain.AgentPolicy{
			Scope:    domain.AgentScope{Slug: slug, TeamSlug: teamSlug},
			Autonomy: ag.Autonomy,
			Web:      ag.Web,
			Browser:  ag.Browser,
			Shell:    ag.Shell,
		},
	}, token, 0)
}

type vaultPromptBuilder struct {
	store app.Store
	vault app.Vault
	clock app.Clock
	tz    string
}

func (b *vaultPromptBuilder) System(ctx context.Context, agentID domain.ID) (string, error) {
	ag, ok, err := b.store.Read().GetAgent(ctx, agentID)
	if err != nil || !ok {
		sys, _ := prompt.BuildSystem(prompt.SystemInput{Kernel: prompts.Kernel})
		return sys, nil
	}
	companyRaw, _ := b.vault.Read(ctx, "company/COMPANY.md")
	var teamRaw []byte
	if tm, tok, _ := b.store.Read().GetTeam(ctx, ag.TeamID); tok {
		teamRaw, _ = b.vault.Read(ctx, fmt.Sprintf("teams/%s/TEAM.md", tm.Slug))
	}
	var roleBody string
	if agentRaw, rerr := b.vault.Read(ctx, fmt.Sprintf("agents/%s/AGENT.md", ag.Slug)); rerr == nil {
		if parsed, perr := agentsync.Parse(agentRaw); perr == nil {
			roleBody = parsed.Body
		}
	}
	sys, _ := prompt.BuildSystem(prompt.SystemInput{
		Kernel:  prompts.Kernel,
		Company: string(companyRaw),
		Team:    string(teamRaw),
		Role:    roleBody,
	})
	return sys, nil
}

func (b *vaultPromptBuilder) RunMessage(ctx context.Context, c app.Claimed) (string, error) {
	ag, _, _ := b.store.Read().GetAgent(ctx, c.AgentID)
	var notes string
	if ag.Slug != "" {
		if raw, err := b.vault.Read(ctx, fmt.Sprintf("agents/%s/notes/NOTES.md", ag.Slug)); err == nil {
			notes = string(raw)
		}
	}
	items := make([]prompt.InboxItem, 0, len(c.Wakeups))
	for _, w := range c.Wakeups {
		summary := b.formatWakeupSummary(ctx, w)
		items = append(items, prompt.InboxItem{
			ID:      w.ID,
			Kind:    w.Kind,
			Summary: summary,
		})
	}
	return prompt.BuildRunMessage(prompt.RunInput{
		Now:   b.clock.Now(),
		TZ:    b.tz,
		Notes: notes,
		Inbox: items,
	}), nil
}

func (b *vaultPromptBuilder) formatWakeupSummary(ctx context.Context, w app.Wakeup) string {
	var payload map[string]any
	if json.Unmarshal(w.Payload, &payload) == nil {
		if txt, ok := payload["text"].(string); ok && strings.TrimSpace(txt) != "" {
			return fmt.Sprintf("Tin nhắn trực tiếp từ chủ nhân: %q (hãy trả lời trực tiếp bằng văn bản).", strings.TrimSpace(txt))
		}
		if msgID, ok := payload["message_id"].(string); ok && msgID != "" {
			if thID, tok := payload["thread_id"].(string); tok && thID != "" {
				if msgs, err := b.store.Read().ListThreadMessages(ctx, thID, 10, nil, ""); err == nil {
					for _, m := range msgs {
						if m.ID == msgID && strings.TrimSpace(m.Content) != "" {
							return fmt.Sprintf("Tin nhắn trực tiếp từ chủ nhân: %q (hãy trả lời trực tiếp bằng văn bản).", strings.TrimSpace(m.Content))
						}
					}
				}
			}
		}
	}
	return string(w.Payload)
}

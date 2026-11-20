package main

import (
	"context"
	"fmt"

	"max/internal/app"
	"max/internal/app/agentsync"
	"max/internal/app/prompt"
	"max/internal/domain"
	"max/prompts"
)

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
		items = append(items, prompt.InboxItem{
			ID:      w.ID,
			Kind:    w.Kind,
			Summary: string(w.Payload),
		})
	}
	return prompt.BuildRunMessage(prompt.RunInput{
		Now:   b.clock.Now(),
		TZ:    b.tz,
		Notes: notes,
		Inbox: items,
	}), nil
}

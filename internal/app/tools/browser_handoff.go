package tools

import (
	"context"
	"fmt"
	"strings"

	"max/internal/domain"
)

// Handoff transfers live browser control to the owner via loopback noVNC and parks the conversation (P4.02).
func (s *Service) Handoff(ctx context.Context, c Caller, reason, origin string) Result {
	if c.Policy.Autonomy == domain.AutonomySuggest {
		return Result{Text: "suggest-mode agent cannot initiate browser handoff", IsError: true}
	}
	if c.Policy.Browser == domain.BrowserNone || c.Policy.Browser == "" {
		return Result{Text: "browser profile is disabled for this agent", IsError: true}
	}
	if s.handoff == nil {
		return Result{Text: "browser handoff service unavailable", IsError: true}
	}
	msg, err := s.handoff.Handoff(ctx, c.AgentID, c.RunID, c.ConversationID, strings.TrimSpace(reason), strings.TrimSpace(origin))
	if err != nil {
		return Result{Text: fmt.Sprintf("handoff failed: %v", err), IsError: true}
	}
	return Result{Text: msg}
}

package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
)

const (
	defaultRunWallTimeout = 20 * time.Minute
	defaultCooldown       = 30 * time.Second
	coldResumeMaxAge      = 60 * time.Minute
	finalizeMaxRetries    = 3
)

// Outcome summarizes a completed CLI run passed to Finalize.
type Outcome struct {
	Status                    domain.RunStatus
	Turns                     int
	InputTokens, OutputTokens int64
	SessionID                 string
	Summary, Err              string
	TouchedPaths              []string
	LimitResetAt              *time.Time
	SessionRebuilt            bool
}

// TokenIdentity binds a live run token to its active run metadata for MCP / hook callers.
type TokenIdentity struct {
	RunID, AgentID, ConversationID domain.ID
	TaskID                         *domain.ID
	AgentSlug                      string
}

// Deps bundles dependencies injected into run.Service.
type Deps struct {
	Store           app.Store
	Actors          *actor.Service
	Engines         map[domain.EngineName]app.Engine
	Prompts         app.PromptBuilder
	Vault           app.Vault
	VaultDir        string
	Permission      app.PermissionFunc
	Clock           app.Clock
	IDs             app.IDGen
	WallTimeout     time.Duration
	FinalizeBackoff time.Duration
}

// Service orchestrates non-interactive CLI run execution and atomic post-run finalization.
type Service struct {
	deps   Deps
	tokMu  sync.RWMutex
	tokens map[string]TokenIdentity
}

// NewService constructs a run lifecycle Service.
func NewService(deps Deps) *Service {
	if deps.Clock == nil {
		deps.Clock = app.SystemClock{}
	}
	if deps.IDs == nil {
		deps.IDs = app.UUIDv7Gen{}
	}
	if deps.WallTimeout <= 0 {
		deps.WallTimeout = defaultRunWallTimeout
	}
	if deps.FinalizeBackoff <= 0 {
		deps.FinalizeBackoff = 100 * time.Millisecond
	}
	return &Service{
		deps:   deps,
		tokens: make(map[string]TokenIdentity),
	}
}

// ResolveRunToken looks up an active run token; returns false as soon as the run leaves running state.
func (s *Service) ResolveRunToken(token string) (TokenIdentity, bool) {
	if token == "" {
		return TokenIdentity{}, false
	}
	s.tokMu.RLock()
	defer s.tokMu.RUnlock()
	id, ok := s.tokens[token]
	return id, ok
}

func (s *Service) registerToken(id TokenIdentity) string {
	var raw [24]byte
	_, _ = rand.Read(raw[:])
	tok := "mrt_" + hex.EncodeToString(raw[:])
	s.tokMu.Lock()
	s.tokens[tok] = id
	s.tokMu.Unlock()
	return tok
}

func (s *Service) revokeToken(tok string) {
	s.tokMu.Lock()
	delete(s.tokens, tok)
	s.tokMu.Unlock()
}

// Execute runs the claimed agent conversation on its selected Engine outside any DB transaction,
// then atomically finalizes state with up to 3 retries on transient store errors.
func (s *Service) Execute(ctx context.Context, c actor.Claimed) (Outcome, error) {
	if s.deps.Vault != nil {
		if err := s.deps.Vault.RefuseIfProjectSettings(ctx); err != nil {
			out := Outcome{Status: domain.RunFailed, Err: err.Error()}
			_ = s.finalizeWithRetry(ctx, c.RunID, out)
			return out, err
		}
	}

	ag, ok, err := s.deps.Store.Read().GetAgent(ctx, c.AgentID)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, fmt.Errorf("agent %q not found for run %q", c.AgentID, c.RunID)
	}

	conv, _, err := s.deps.Store.Read().GetConversation(ctx, c.ConversationID)
	if err != nil {
		return Outcome{}, err
	}

	now := s.deps.Clock.Now()
	rebuilt := false
	effectiveClaim := c
	if effectiveClaim.SessionID != "" {
		lastRun, hasLast, err := s.deps.Store.Read().GetLastFinishedRunForConv(ctx, c.ConversationID)
		if err != nil {
			return Outcome{}, err
		}
		if hasLast && lastRun.EndedAt != nil {
			age := now.Sub(time.UnixMilli(*lastRun.EndedAt))
			if age > coldResumeMaxAge && lastRun.InputTokens > domain.ColdResumeTokens {
				effectiveClaim.SessionID = ""
				effectiveClaim.EngineChanged = true
				rebuilt = true
			}
		}
	}

	var sysPrompt, runPrompt string
	if s.deps.Prompts != nil {
		sysPrompt, err = s.deps.Prompts.System(ctx, c.AgentID)
		if err != nil {
			return Outcome{}, fmt.Errorf("build system prompt: %w", err)
		}
		runPrompt, err = s.deps.Prompts.RunMessage(ctx, effectiveClaim)
		if err != nil {
			return Outcome{}, fmt.Errorf("build run prompt: %w", err)
		}
	}
	if rebuilt && !strings.Contains(runPrompt, "<history>") {
		runPrompt = "<history>Session rebuilt after cold idle period.</history>\n" + runPrompt
	}

	eng, ok := s.deps.Engines[c.Engine]
	if !ok {
		return Outcome{}, fmt.Errorf("engine %q not registered", c.Engine)
	}

	token := s.registerToken(TokenIdentity{
		RunID:          c.RunID,
		AgentID:        c.AgentID,
		ConversationID: c.ConversationID,
		TaskID:         c.TaskID,
		AgentSlug:      ag.Slug,
	})
	defer s.revokeToken(token)

	spec := app.RunSpec{
		RunID:        c.RunID,
		AgentID:      c.AgentID,
		AgentSlug:    ag.Slug,
		Cwd:          s.deps.VaultDir,
		SystemPrompt: sysPrompt,
		Prompt:       runPrompt,
		Model:        "default",
		SessionID:    effectiveClaim.SessionID,
		RunToken:     token,
		Autonomy:     ag.Autonomy,
	}

	out := s.driveEngine(ctx, eng, spec, conv.TurnsUsed)
	out.SessionRebuilt = rebuilt

	s.revokeToken(token)

	if err := s.finalizeWithRetry(ctx, c.RunID, out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) driveEngine(ctx context.Context, eng app.Engine, spec app.RunSpec, priorConvTurns int) Outcome {
	runCtx, cancel := context.WithTimeout(ctx, s.deps.WallTimeout)
	defer cancel()

	var askedApproval bool
	perm := func(pctx context.Context, ask app.ToolAsk) domain.Decision {
		if s.deps.Permission == nil {
			return domain.Decision{Verdict: domain.Allow}
		}
		dec := s.deps.Permission(pctx, ask)
		if dec.Verdict == domain.Ask {
			askedApproval = true
		}
		return dec
	}

	evCh, err := eng.Run(runCtx, spec, perm)
	if err != nil {
		return Outcome{Status: domain.RunInterrupted, Err: err.Error()}
	}

	var (
		out             Outcome
		textBuf         strings.Builder
		started         bool
		afterToolResult bool
		sawFinal        bool
	)
	out.SessionID = spec.SessionID

	for ev := range evCh {
		if !started && (ev.Type == "session" || ev.Type == "text_delta" || ev.Type == "tool_call" || ev.Type == "final") {
			started = true
			out.Turns = 1
		}
		switch ev.Type {
		case "session":
			if ev.SessionID != "" {
				out.SessionID = ev.SessionID
			}
		case "text_delta":
			if afterToolResult {
				out.Turns++
				afterToolResult = false
			}
			textBuf.WriteString(ev.Text)
		case "tool_call":
			if afterToolResult {
				out.Turns++
				afterToolResult = false
			}
		case "tool_result":
			afterToolResult = true
		case "usage":
			if ev.InputTokens > 0 {
				out.InputTokens = ev.InputTokens
			}
			if ev.OutputTokens > 0 {
				out.OutputTokens = ev.OutputTokens
			}
		case "limit":
			out.Status = domain.RunQuotaStopped
			out.LimitResetAt = ev.LimitResetAt
			cancel()
		case "error":
			if out.Status == "" {
				out.Status = domain.RunInterrupted
				out.Err = ev.ErrMessage
			}
			cancel()
		case "final":
			sawFinal = true
			if ev.InputTokens > 0 {
				out.InputTokens = ev.InputTokens
			}
			if ev.OutputTokens > 0 {
				out.OutputTokens = ev.OutputTokens
			}
			if ev.Text != "" && textBuf.Len() == 0 {
				textBuf.WriteString(ev.Text)
			}
		}

		if out.Status == "" && (out.Turns > domain.MaxTurnsPerRun || priorConvTurns+out.Turns > domain.MaxTurnsPerTask) {
			out.Status = domain.RunTurnLimit
			cancel()
		}
	}

	out.Summary = truncateRunes(strings.TrimSpace(textBuf.String()), 1000)

	if out.Status == "" {
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
			out.Status = domain.RunTimedOut
		case ctx.Err() != nil:
			out.Status = domain.RunInterrupted
			out.Err = ctx.Err().Error()
		case !sawFinal:
			out.Status = domain.RunInterrupted
			if out.Err == "" {
				out.Err = "engine stream closed without final event"
			}
		case askedApproval:
			out.Status = domain.RunParked
		default:
			out.Status = domain.RunSucceeded
		}
	}
	return out
}

func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

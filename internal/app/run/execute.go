package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	"max/internal/app/prompt"
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
	ScopeViolations           int
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
	Store            app.Store
	Actors           *actor.Service
	Coord            *coord.Service
	Engines          map[domain.EngineName]app.Engine
	Prompts          app.PromptBuilder
	Vault            app.Vault
	VaultDir         string
	Permission       app.PermissionFunc
	OnEvent          func(c actor.Claimed, ev app.EngineEvent)
	OnRunStart       func(c actor.Claimed, slug, token string)
	OnRunEnd         func(runID domain.ID)
	PostRunAudit     func(ctx context.Context, runID domain.ID, slug string) []string
	PostRunAuditFull func(ctx context.Context, runID domain.ID, scope domain.AgentScope) (touched []string, violations int)
	Clock            app.Clock
	IDs              app.IDGen
	WallTimeout      time.Duration
	Cooldown         *time.Duration
	FinalizeBackoff  time.Duration
}

// Service orchestrates non-interactive CLI run execution and atomic post-run finalization.
type Service struct {
	deps    Deps
	tokMu   sync.RWMutex
	tokens  map[string]TokenIdentity
	touched map[domain.ID][]string
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
		deps:    deps,
		tokens:  make(map[string]TokenIdentity),
		touched: make(map[domain.ID][]string),
	}
}

// SetPermission wires a PermissionFunc after construction.
func (s *Service) SetPermission(fn app.PermissionFunc) {
	s.deps.Permission = fn
}

// RegisterTestToken mints an active run token for unit testing PermissionFunc.
func (s *Service) RegisterTestToken(id TokenIdentity) string {
	return s.registerToken(id)
}

// RecordTouch records an in-scope relative vault path modified during runID.
func (s *Service) RecordTouch(runID domain.ID, rel string) {
	if runID == "" || rel == "" {
		return
	}
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if !slices.Contains(s.touched[runID], rel) {
		s.touched[runID] = append(s.touched[runID], rel)
	}
}

// TouchedPaths returns a copy of in-scope vault paths recorded for runID.
func (s *Service) TouchedPaths(runID domain.ID) []string {
	s.tokMu.RLock()
	defer s.tokMu.RUnlock()
	return slices.Clone(s.touched[runID])
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
	if (effectiveClaim.EngineChanged || rebuilt) && !strings.Contains(runPrompt, "<history>") {
		hist := s.loadCarryOverHistory(ctx, effectiveClaim)
		if hist == "" && rebuilt {
			hist = "Session rebuilt after cold idle period."
		}
		if hist != "" {
			runPrompt = "<history>\n" + hist + "\n</history>\n" + runPrompt
		}
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
	if s.deps.OnRunStart != nil {
		s.deps.OnRunStart(c, ag.Slug, token)
	}
	defer func() {
		s.revokeToken(token)
		if s.deps.OnRunEnd != nil {
			s.deps.OnRunEnd(c.RunID)
		}
	}()

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

	out := s.driveEngine(ctx, c, eng, spec, conv.TurnsUsed)
	out.SessionRebuilt = rebuilt

	s.revokeToken(token)
	if s.deps.OnRunEnd != nil {
		s.deps.OnRunEnd(c.RunID)
	}

	merged := s.TouchedPaths(c.RunID)
	if s.deps.PostRunAuditFull != nil {
		var teamSlug string
		if tm, tok, _ := s.deps.Store.Read().GetTeam(ctx, ag.TeamID); tok {
			teamSlug = tm.Slug
		}
		audited, vCount := s.deps.PostRunAuditFull(ctx, c.RunID, domain.AgentScope{Slug: ag.Slug, TeamSlug: teamSlug})
		out.ScopeViolations += vCount
		for _, p := range audited {
			if !slices.Contains(merged, p) {
				merged = append(merged, p)
			}
		}
	} else if s.deps.PostRunAudit != nil {
		for _, p := range s.deps.PostRunAudit(ctx, c.RunID, ag.Slug) {
			if !slices.Contains(merged, p) {
				merged = append(merged, p)
			}
		}
	}
	for _, p := range merged {
		if !slices.Contains(out.TouchedPaths, p) {
			out.TouchedPaths = append(out.TouchedPaths, p)
		}
	}
	s.tokMu.Lock()
	delete(s.touched, c.RunID)
	s.tokMu.Unlock()

	finCtx, finCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finCancel()
	if err := s.finalizeWithRetry(finCtx, c.RunID, out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) loadCarryOverHistory(ctx context.Context, c actor.Claimed) string {
	rd := s.deps.Store.Read()
	var summary string
	var pEvents []prompt.TaskEvent

	if c.TaskID != nil && *c.TaskID != "" {
		if tk, ok, err := rd.GetTask(ctx, *c.TaskID); err == nil && ok {
			summary = fmt.Sprintf("%s (%s)", tk.Title, tk.Status)
		}
		if evs, err := rd.ListTaskEvents(ctx, *c.TaskID, 50); err == nil {
			for _, ev := range evs {
				actorStr := ev.ActorKind
				if ev.ActorID != nil {
					actorStr = *ev.ActorID
				}
				pEvents = append(pEvents, prompt.TaskEvent{
					Kind:      ev.Kind,
					Actor:     actorStr,
					Body:      ev.Body,
					CreatedAt: time.UnixMilli(ev.CreatedAt).UTC(),
				})
			}
		}
	}
	if summary == "" {
		if lastRun, ok, err := rd.GetLastFinishedRunForConv(ctx, c.ConversationID); err == nil && ok && lastRun.Summary != nil {
			summary = *lastRun.Summary
		}
	}
	if summary == "" && len(pEvents) == 0 {
		summary = "Engine switched for ongoing conversation."
	}
	return prompt.BuildHistory(summary, pEvents, 12000)
}

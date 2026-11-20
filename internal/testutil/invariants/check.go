package invariants

import (
	"context"
	"fmt"
	"time"

	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/domain"
)

// Violation describes an invariant failure with its canonical Spec section 11 code (I1..I12).
type Violation struct {
	Code    string
	Message string
}

// Config holds optional runtime observations evaluated alongside SQLite state invariants.
type Config struct {
	Quiescent          bool
	Recovered          bool
	SlotCaps           map[domain.EngineName]int
	ClaimP95           time.Duration
	ClaimP99           time.Duration
	InteractiveLatency time.Duration
	BackgroundAtCap    int
	StarvedAgents      int
}

// Option customizes invariant checks.
type Option func(*Config)

// WithQuiescent asserts all wakeups reached done/dropped and 0 runs remain running.
func WithQuiescent(q bool) Option { return func(c *Config) { c.Quiescent = q } }

// WithRecovered asserts post-crash recovery left 0 runs in running state (I7).
func WithRecovered(r bool) Option { return func(c *Config) { c.Recovered = r } }

// WithSlotCaps asserts live running counts per engine never exceed configured slots (I2).
func WithSlotCaps(caps map[domain.EngineName]int) Option {
	return func(c *Config) { c.SlotCaps = caps }
}

// WithClaimLatencies checks I8 (p95 < 20ms, p99 < 50ms when measured).
func WithClaimLatencies(p95, p99 time.Duration) Option {
	return func(c *Config) {
		c.ClaimP95 = p95
		c.ClaimP99 = p99
	}
}

// WithInteractiveLatency checks I9 (owner interactive wakeup starts <= 5s on idle agent).
func WithInteractiveLatency(d time.Duration) Option {
	return func(c *Config) { c.InteractiveLatency = d }
}

// Check evaluates invariants I1-I12 against store and optional runtime observations.
func Check(ctx context.Context, store app.Store, opts ...Option) []Violation {
	var cfg Config
	for _, fn := range opts {
		fn(&cfg)
	}

	var out []Violation
	sqlStore, ok := store.(*sqlite.Store)
	if !ok {
		return []Violation{{Code: "STORE", Message: "unsupported store implementation for invariant check"}}
	}

	dbViolations, err := sqlStore.CheckDBInvariants(ctx, sqlite.InvariantCheckParams{
		Quiescent: cfg.Quiescent,
		Recovered: cfg.Recovered,
		SlotCaps:  cfg.SlotCaps,
	})
	if err != nil {
		out = append(out, Violation{Code: "DB", Message: err.Error()})
	}
	for _, v := range dbViolations {
		out = append(out, Violation{Code: v.Code, Message: v.Message})
	}

	if cfg.ClaimP95 > 20*time.Millisecond || cfg.ClaimP99 > 50*time.Millisecond {
		out = append(out, Violation{
			Code:    "I8",
			Message: fmt.Sprintf("claim latency exceeded threshold: p95=%s (max 20ms), p99=%s (max 50ms)", cfg.ClaimP95, cfg.ClaimP99),
		})
	}
	if cfg.InteractiveLatency > 5*time.Second {
		out = append(out, Violation{
			Code:    "I9",
			Message: fmt.Sprintf("interactive start latency %s > 5s", cfg.InteractiveLatency),
		})
	}
	if cfg.BackgroundAtCap > 0 {
		out = append(out, Violation{
			Code:    "I10",
			Message: fmt.Sprintf("%d background run(s) admitted above short-window share", cfg.BackgroundAtCap),
		})
	}
	if cfg.StarvedAgents > 0 {
		out = append(out, Violation{
			Code:    "I11",
			Message: fmt.Sprintf("%d ready agent(s) starved while slots and quota available", cfg.StarvedAgents),
		})
	}

	return out
}

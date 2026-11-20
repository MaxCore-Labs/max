package domain

import (
	"errors"
	"fmt"
)

const (
	MaxTaskDepth       = 5
	MaxCausalHops      = 20
	MaxDelegatesPerRun = 10
	MaxMessagesPerRun  = 10
	MaxPairMsgsPerHour = 6
)

var (
	ErrDepth            = errors.New("task depth limit exceeded (max 5)")
	ErrHops             = errors.New("causal hops limit exceeded (max 20)")
	ErrRunCap           = errors.New("per-run coordination action cap exceeded (max 10)")
	ErrNotAllowedTarget = errors.New("delegation target outside sender team and manager hierarchy")
	ErrPairRate         = errors.New("agent pair message rate limit exceeded (max 6 per hour)")
)

// RunCounters tracks coordination tool invocations already performed in the active run.
type RunCounters struct {
	Delegates int
	Messages  int
}

// CoordCheck carries pure coordination context for anti-storm rule validation (Spec 8.7).
type CoordCheck struct {
	Depth            int
	Hops             int
	Run              RunCounters
	PairMsgsLastHour int
	SenderTeam       string
	TargetTeam       string
	TargetIsManager  bool
	SenderIsChief    bool
}

// CheckDelegate enforces depth, causal hops, per-run delegate count, and team/manager routing rules.
func CheckDelegate(c CoordCheck) error {
	if c.Depth >= MaxTaskDepth {
		return fmt.Errorf("%w: parent depth %d would place child at %d", ErrDepth, c.Depth, c.Depth+1)
	}
	if c.Hops >= MaxCausalHops {
		return fmt.Errorf("%w: hops=%d", ErrHops, c.Hops)
	}
	if c.Run.Delegates >= MaxDelegatesPerRun {
		return fmt.Errorf("%w: delegates=%d", ErrRunCap, c.Run.Delegates)
	}
	if !c.SenderIsChief && !c.TargetIsManager && (c.SenderTeam == "" || c.SenderTeam != c.TargetTeam) {
		return fmt.Errorf("%w: sender team %q cannot delegate to team %q", ErrNotAllowedTarget, c.SenderTeam, c.TargetTeam)
	}
	return nil
}

// CheckMessage enforces causal hops, per-run message count, and pairwise hourly message caps.
func CheckMessage(c CoordCheck) error {
	if c.Hops >= MaxCausalHops {
		return fmt.Errorf("%w: hops=%d", ErrHops, c.Hops)
	}
	if c.Run.Messages >= MaxMessagesPerRun {
		return fmt.Errorf("%w: messages=%d", ErrRunCap, c.Run.Messages)
	}
	if c.PairMsgsLastHour >= MaxPairMsgsPerHour {
		return fmt.Errorf("%w: pair_msgs_last_hour=%d", ErrPairRate, c.PairMsgsLastHour)
	}
	return nil
}

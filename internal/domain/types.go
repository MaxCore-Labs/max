package domain

// ID is a UUID v7 string generated at the application layer.
type ID = string

// EngineName identifies a supported CLI engine.
type EngineName string

const (
	EngineAgy    EngineName = "agy"
	EngineClaude EngineName = "claude"
	EngineCodex  EngineName = "codex"
)

// EngineBit returns the bitmask flag for e (claude=1, codex=2, agy=4).
func EngineBit(e EngineName) int {
	switch e {
	case EngineClaude:
		return 1
	case EngineCodex:
		return 2
	case EngineAgy:
		return 4
	default:
		return 0
	}
}

// EngineMask computes the bitwise OR mask for names (claude=1, codex=2, agy=4).
func EngineMask(names []EngineName) int {
	mask := 0
	for _, n := range names {
		mask |= EngineBit(n)
	}
	return mask
}

// Priority determines dispatch precedence (higher runs first).
type Priority int

const (
	PrioInteractive Priority = 100
	PrioTask        Priority = 50
	PrioRoutine     Priority = 30
	PrioMaintenance Priority = 10
)

// WakeKind classifies why an agent mailbox received work.
type WakeKind string

const (
	WakeMessage        WakeKind = "message"
	WakeTaskAssigned   WakeKind = "task_assigned"
	WakeTaskUpdate     WakeKind = "task_update"
	WakeChildDone      WakeKind = "child_done"
	WakeOwnerReply     WakeKind = "owner_reply"
	WakeApprovalResult WakeKind = "approval_result"
	WakeRoutine        WakeKind = "routine"
	WakeHeartbeat      WakeKind = "heartbeat"
	WakeContinue       WakeKind = "continue"
	WakeSystem         WakeKind = "system"
)

// Coalescible reports whether multiple queued wakeups of k in the same conversation may merge.
func (k WakeKind) Coalescible() bool {
	switch k {
	case WakeMessage, WakeTaskUpdate, WakeHeartbeat, WakeRoutine:
		return true
	default:
		return false
	}
}

// RunStatus represents the lifecycle state of a single CLI run.
type RunStatus string

const (
	RunRunning      RunStatus = "running"
	RunSucceeded    RunStatus = "succeeded"
	RunFailed       RunStatus = "failed"
	RunCancelled    RunStatus = "cancelled"
	RunTimedOut     RunStatus = "timed_out"
	RunTurnLimit    RunStatus = "turn_limit"
	RunParked       RunStatus = "parked"
	RunQuotaStopped RunStatus = "quota_stopped"
	RunInterrupted  RunStatus = "interrupted"
)

// Terminal reports whether s is a finished (non-running) run status.
func (s RunStatus) Terminal() bool {
	return s != RunRunning
}

// Autonomy controls whether an agent can perform data writes and actions autonomously.
type Autonomy string

const (
	AutonomySuggest Autonomy = "suggest"
	AutonomyAuto    Autonomy = "auto"
)

// ToolClass categorizes tools for policy evaluation (Spec 8.6).
type ToolClass string

const (
	ToolClassR       ToolClass = "R"
	ToolClassC       ToolClass = "C"
	ToolClassWd      ToolClass = "Wd"
	ToolClassWt      ToolClass = "Wt"
	ToolClassP       ToolClass = "P"
	ToolClassBa      ToolClass = "Ba"
	ToolClassE       ToolClass = "E"
	ToolClassX       ToolClass = "X"
	ToolClassUnknown ToolClass = "Unknown"
)

// Verdict is the outcome of a pure policy decision.
type Verdict int

const (
	Allow Verdict = iota
	Deny
	Ask
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case Ask:
		return "ask"
	default:
		return "unknown"
	}
}

// Decision pairs a Verdict with a human-readable explanation.
type Decision struct {
	Verdict Verdict
	Reason  string
}

// RootOrigin tracks the trust origin of the root task in a task tree.
type RootOrigin string

const (
	OriginOwner    RootOrigin = "owner"
	OriginRoutine  RootOrigin = "routine"
	OriginAgent    RootOrigin = "agent"
	OriginExternal RootOrigin = "external"
)

// BrowserProfile selects which browser identity an agent may use.
type BrowserProfile string

const (
	BrowserNone  BrowserProfile = "none"
	BrowserAnon  BrowserProfile = "anon"
	BrowserOwner BrowserProfile = "owner"
)

// AgentStatus represents an agent's operational availability.
type AgentStatus string

const (
	AgentActive      AgentStatus = "active"
	AgentPaused      AgentStatus = "paused"
	AgentQuotaPaused AgentStatus = "quota_paused"
	AgentArchived    AgentStatus = "archived"
)

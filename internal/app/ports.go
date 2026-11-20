package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"max/internal/domain"
)

// Clock supplies wall-clock time to application use cases (replaceable in tests).
type Clock interface {
	Now() time.Time
}

// SystemClock implements Clock using time.Now().
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// IDGen generates globally sortable UUID v7 identifiers.
type IDGen interface {
	New() domain.ID
}

// UUIDv7Gen implements IDGen using RFC 9562 UUID v7.
type UUIDv7Gen struct{}

func (UUIDv7Gen) New() domain.ID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// TeamRecord represents a row in the teams table.
type TeamRecord struct {
	ID             domain.ID
	Slug           string
	Name           string
	MandateJSON    string
	MaxRunning     int
	Running        int
	QuotaTokensDay *int64
	Status         string
}

// AgentRecord represents a row in the agents table.
type AgentRecord struct {
	ID                 domain.ID
	Slug               string
	Name               string
	TemplateSlug       *string
	TeamID             domain.ID
	ManagerAgentID     *domain.ID
	EnginesJSON        string
	EngineMask         int
	AllowUngatedEngine bool
	ModelsJSON         string
	ScopeViolations    int
	Autonomy           domain.Autonomy
	Web                bool
	Browser            domain.BrowserProfile
	Shell              bool
	SkillsJSON         string
	ParamsJSON         string
	QuotaTokensDay     *int64
	ConfigRev          string
	Status             domain.AgentStatus
	Ready              bool
	ReadyPriority      int
	ReadySince         *int64
	ActiveRunID        *domain.ID
	CooldownUntil      int64
	LastRunAt          *int64
	NextHeartbeatAt    *int64
}

// WakeupRecord represents a row in the wakeups table.
type WakeupRecord struct {
	ID             domain.ID
	AgentID        domain.ID
	ConversationID *domain.ID
	Kind           domain.WakeKind
	Priority       domain.Priority
	Payload        string
	IdempotencyKey *string
	Status         string
	RunID          *domain.ID
	CoalescedCount int
	Attempts       int
	CauseRunID     *domain.ID
	Hops           int
	NotBefore      int64
	CreatedAt      int64
}

// ConversationRecord represents a row in the conversations table.
type ConversationRecord struct {
	ID         domain.ID
	AgentID    domain.ID
	TaskID     *domain.ID
	ThreadID   *domain.ID
	Sessions   string
	LastEngine *string
	WaitingFor *string
	TurnsUsed  int
	ClosedAt   *int64
}

// RunRecord represents a row in the runs table.
type RunRecord struct {
	ID               domain.ID
	AgentID          domain.ID
	ConversationID   domain.ID
	TaskID           *domain.ID
	Status           domain.RunStatus
	Priority         domain.Priority
	Engine           domain.EngineName
	Model            string
	PGID             *int64
	Attempt          int
	Turns            int
	InputTokens      int64
	OutputTokens     int64
	CachedReadTokens int64
	ThoughtTokens    int64
	NotionalMicroUSD int64
	TouchedPathsJSON string
	Error            *string
	Summary          *string
	VaultCommit      *string
	StartedAt        int64
	EndedAt          *int64
}

// CandidateAgent represents an eligible ready agent row joined with its team running counts.
type CandidateAgent struct {
	ID            domain.ID
	TeamID        domain.ID
	EnginesJSON   string
	ModelsJSON    string
	CooldownUntil int64
	ReadyPriority int
	TeamRunning   int
	TeamMaxRun    int
}

// InboxItemRecord represents a row in inbox_items.
type InboxItemRecord struct {
	ID             domain.ID
	Kind           string
	Title          string
	Body           string
	RefKind        *string
	RefID          *string
	ConversationID *domain.ID
	AgentID        *domain.ID
	ChoicesJSON    string
	Urgency        string
	DedupeKey      *string
	CreatedAt      int64
}

// Reader exposes non-transactional read queries backed by the reader pool.
type Reader interface {
	GetKV(ctx context.Context, key string) (val string, ok bool, err error)
	GetAgent(ctx context.Context, id domain.ID) (AgentRecord, bool, error)
	GetTeam(ctx context.Context, id domain.ID) (TeamRecord, bool, error)
	GetConversation(ctx context.Context, id domain.ID) (ConversationRecord, bool, error)
	GetRun(ctx context.Context, id domain.ID) (RunRecord, bool, error)
	GetWakeup(ctx context.Context, agentID, wakeupID domain.ID) (WakeupRecord, bool, error)
	ListQueuedWakeups(ctx context.Context, agentID domain.ID) ([]WakeupRecord, error)
	ListInboxItems(ctx context.Context, limit int) ([]InboxItemRecord, error)
}

// Tx exposes write queries inside a single BEGIN IMMEDIATE SQLite transaction.
type Tx interface {
	Reader
	UpsertKV(ctx context.Context, key, val string, updatedAt int64) error
	InsertTeam(ctx context.Context, t TeamRecord) error
	InsertAgent(ctx context.Context, a AgentRecord) error
	SetAgentCooldown(ctx context.Context, agentID domain.ID, cooldownUntil int64) error
	InsertConversation(ctx context.Context, c ConversationRecord) error
	EnsureSystemConversation(ctx context.Context, agentID, newConvID domain.ID) (domain.ID, error)
	SetAgentActiveRun(ctx context.Context, agentID domain.ID, runID *domain.ID) error
	InsertRun(ctx context.Context, r RunRecord) error
	AdjustTeamRunning(ctx context.Context, teamID domain.ID, delta int) error
	InsertWakeupRaw(ctx context.Context, w WakeupRecord) error
	FindWakeupByIdemKey(ctx context.Context, idemKey string) (domain.ID, bool, error)
	CoalesceQueuedWakeup(ctx context.Context, agentID domain.ID, convID *domain.ID, kind domain.WakeKind, prio domain.Priority, payload string, notBefore int64) (domain.ID, bool, error)
	CountQueuedWakeups(ctx context.Context, agentID domain.ID) (int, error)
	RecomputeAgentReady(ctx context.Context, agentID domain.ID, nowMs int64) error
	ListCandidateAgents(ctx context.Context, nowMs int64, engineMask, limit int) ([]CandidateAgent, error)
	PeekTopDueWakeup(ctx context.Context, agentID domain.ID, nowMs int64) (WakeupRecord, bool, error)
	DrainDueWakeupsForConv(ctx context.Context, agentID domain.ID, origConvID *domain.ID, targetConvID, runID domain.ID, nowMs int64, limit int) ([]WakeupRecord, error)
	InsertInboxItem(ctx context.Context, item InboxItemRecord) error
	RecoverCrashState(ctx context.Context, nowMs int64, graceful bool, newID func() domain.ID) (int, error)
}

// Store owns transactional writes (single writer) and concurrent read queries.
type Store interface {
	WithTx(ctx context.Context, fn func(tx Tx) error) error
	Read() Reader
}

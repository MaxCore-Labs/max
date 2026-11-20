package app

import (
	"encoding/json"

	"max/internal/domain"
)

// Wakeup is the drained wakeup representation handed to a claimed run.
type Wakeup struct {
	ID             domain.ID
	Kind           domain.WakeKind
	Payload        json.RawMessage
	CoalescedCount int
	Attempts       int
	CauseRunID     *domain.ID
	Hops           int
}

// Claimed describes a run claimed inside ClaimBatch and ready to be spawned outside the DB transaction.
type Claimed struct {
	RunID, AgentID, ConversationID domain.ID
	TaskID                         *domain.ID
	Engine                         domain.EngineName
	Priority                       domain.Priority
	Wakeups                        []Wakeup
	SessionID                      string
	EngineChanged                  bool
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

// TaskRecord represents a row in the tasks table.
type TaskRecord struct {
	ID              domain.ID
	GoalID          *domain.ID
	ParentID        *domain.ID
	RootID          domain.ID
	RootOrigin      domain.RootOrigin
	Depth           int
	Title           string
	Description     string
	DoneCriteria    string
	Status          string
	BlockedReason   *string
	AssigneeAgentID *domain.ID
	CreatedByAgent  *domain.ID
	CheckoutRunID   *domain.ID
	NeedsReview     bool
	ReviewerAgentID *domain.ID
	ReviewRounds    int
	IdleRuns        int
	DueAt           *int64
	CreatedAt       int64
	UpdatedAt       int64
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

// RunEventRecord represents an append-only row in run_events.
type RunEventRecord struct {
	ID        int64
	RunID     domain.ID
	AgentID   domain.ID
	Type      string
	DataJSON  string
	CreatedAt int64
}

// QuotaStateRecord represents a row in quota_state.
type QuotaStateRecord struct {
	Engine         domain.EngineName
	EstShortTokens *int64
	EstWeekTokens  *int64
	LimitedUntil   *int64
	LastLimitAt    *int64
	ProbeAttempts  int
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

// ApprovalRecord represents a row in the approvals table.
type ApprovalRecord struct {
	ID             domain.ID
	RunID          domain.ID
	AgentID        domain.ID
	ConversationID domain.ID
	ToolName       string
	Input          string
	InputHash      string
	Preview        string
	Reason         string
	ByMax          bool
	Status         string
	DecisionKey    *string
	RequestedAt    int64
	ExpiresAt      int64
	DecidedAt      *int64
}

// UserRecord represents a row in the users table.
type UserRecord struct {
	ID           domain.ID
	Username     string
	PasswordHash string
	TOTPEnabled  bool
	CreatedAt    int64
	UpdatedAt    int64
}

// AuthSessionRecord represents a row in the auth_sessions table.
type AuthSessionRecord struct {
	TokenHash string
	UserID    domain.ID
	ExpiresAt int64
	CreatedAt int64
}

// OutboxRecord represents a row in the outbox table.
type OutboxRecord struct {
	ID            int64
	Topic         string
	PayloadJSON   string
	DedupeKey     *string
	Urgency       string
	Attempts      int
	NextAttemptAt int64
	SentAt        *int64
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

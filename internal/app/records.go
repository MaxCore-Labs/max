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
	ConfigRev                      string
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
	ConfigRev        string
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
	ConfigRev     string
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

// ThreadRecord represents a row in the threads table.
type ThreadRecord struct {
	ID                domain.ID
	AgentID           domain.ID
	Title             string
	LearnedThroughMsg *domain.ID
	LastMsgAt         *int64
	CreatedAt         int64
	UpdatedAt         int64
}

// MessageRecord represents a row in the messages table.
type MessageRecord struct {
	ID              domain.ID
	ThreadID        domain.ID
	Role            string
	Content         string
	AttachmentsJSON string
	RunID           *domain.ID
	IdempotencyKey  *string
	CreatedAt       int64
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
	ReadAt         *int64
	ResolvedAt     *int64
	CreatedAt      int64
}

// DailyUsageRecord represents a row in usage_daily.
type DailyUsageRecord struct {
	Day     string
	AgentID domain.ID
	TeamID  domain.ID
	Engine  domain.EngineName
	Runs    int
	Tokens  int64
}

// AuditLogRecord represents an append-only row in audit_log.
type AuditLogRecord struct {
	ID         int64
	TS         int64
	Actor      string
	Action     string
	Ref        string
	DetailJSON string
}

// EngineBinaryRecord represents a confirmed CLI binary hash in engine_binaries.
type EngineBinaryRecord struct {
	Engine      domain.EngineName
	Path        string
	SHA256      string
	Version     string
	ConfirmedAt int64
}

// TaskEventRecord represents an append-only row in task_events.
type TaskEventRecord struct {
	ID            domain.ID
	TaskID        domain.ID
	Kind          string
	ActorKind     string
	ActorID       *domain.ID
	TargetAgentID *domain.ID
	Body          string
	CreatedAt     int64
}

// TaskFilter scopes task list queries by visibility and optional filters.
type TaskFilter struct {
	ViewerAgentID string
	ViewerTeamID  string
	TeamID        string
	Status        string
	AssigneeID    string
	GoalID        string
	RootID        string
	Limit         int
	BeforeTS      *int64
	BeforeID      string
}

// AgentFilter scopes paginated agent queries with optional filters and slug cursor.
type AgentFilter struct {
	TeamID    string
	Template  string
	Status    string
	Query     string
	AfterSlug string
	Limit     int
}

// TeamSummaryRecord pairs a TeamRecord with its total agent count.
type TeamSummaryRecord struct {
	TeamRecord
	AgentCount int
}

// GoalRecord represents a row in the goals table.
type GoalRecord struct {
	ID          domain.ID
	Title       string
	Description string
	Status      string
	CreatedAt   int64
}

// RoutineStateRecord represents a row in routine_state.
type RoutineStateRecord struct {
	Slug                string
	LastScheduledMin    *int64
	LastFiredAt         *int64
	ActiveTaskID        *domain.ID
	ConsecutiveFailures int
	AutoDisabled        bool
	BadFileReported     bool
	UpdatedAt           int64
}

// DueHeartbeatAgent represents an active agent whose next_heartbeat_at is due.
type DueHeartbeatAgent struct {
	ID        domain.ID
	Slug      string
	Heartbeat string
	LastRunAt *int64
}

// ProposalRecord represents a row in the proposals table.
type ProposalRecord struct {
	ID          domain.ID
	Kind        string
	AgentID     *domain.ID
	TargetPath  *string
	Title       string
	PayloadJSON string
	Status      string
	DecidedAt   *int64
	CreatedAt   int64
}

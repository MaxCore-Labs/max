package app

import (
	"context"
	"encoding/json"
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

// Reader exposes non-transactional read queries backed by the reader pool.
type Reader interface {
	GetKV(ctx context.Context, key string) (val string, ok bool, err error)
	GetAgent(ctx context.Context, id domain.ID) (AgentRecord, bool, error)
	GetAgentBySlug(ctx context.Context, slug string) (AgentRecord, bool, error)
	GetTeam(ctx context.Context, id domain.ID) (TeamRecord, bool, error)
	GetTeamBySlug(ctx context.Context, slug string) (TeamRecord, bool, error)
	GetConversation(ctx context.Context, id domain.ID) (ConversationRecord, bool, error)
	GetTask(ctx context.Context, id domain.ID) (TaskRecord, bool, error)
	GetRun(ctx context.Context, id domain.ID) (RunRecord, bool, error)
	GetLastFinishedRunForConv(ctx context.Context, convID domain.ID) (RunRecord, bool, error)
	GetWakeup(ctx context.Context, agentID, wakeupID domain.ID) (WakeupRecord, bool, error)
	GetQuotaState(ctx context.Context, engine domain.EngineName) (QuotaStateRecord, bool, error)
	ListQueuedWakeups(ctx context.Context, agentID domain.ID) ([]WakeupRecord, error)
	ListClaimedWakeupsByRun(ctx context.Context, runID domain.ID) ([]WakeupRecord, error)
	ListRunEvents(ctx context.Context, runID domain.ID) ([]RunEventRecord, error)
	ListInboxItems(ctx context.Context, limit int) ([]InboxItemRecord, error)
	ListOutboxPending(ctx context.Context, limit int) ([]OutboxRecord, error)
	CountQuestionsByRun(ctx context.Context, runID domain.ID) (int, error)
	GetApproval(ctx context.Context, id domain.ID) (ApprovalRecord, bool, error)
	FindPendingApproval(ctx context.Context, agentID domain.ID, toolName, inputHash string) (ApprovalRecord, bool, error)
	GetQuotaWindowTokens(ctx context.Context, engine domain.EngineName, window string, windowStartMs int64) (tokens int64, runs int, err error)
	CountQuotaLedgerRows(ctx context.Context, engine domain.EngineName, window string) (int, error)
	CountFutureQuotaLedgerRows(ctx context.Context, afterMs int64) (int, error)
	CountAgentRunsSince(ctx context.Context, agentID domain.ID, sinceMs int64) (int, error)
	GetAgentDailyTokens(ctx context.Context, day string, agentID domain.ID) (int64, error)
	GetTeamDailyTokens(ctx context.Context, day string, teamID domain.ID) (int64, error)
	ListQuotaPausedAgents(ctx context.Context) ([]AgentRecord, error)
	GetOwnerUser(ctx context.Context) (UserRecord, bool, error)
	GetUserByID(ctx context.Context, id domain.ID) (UserRecord, bool, error)
	GetAuthSession(ctx context.Context, tokenHash string) (AuthSessionRecord, bool, error)
	ListAllAuthSessionHashes(ctx context.Context) ([]string, error)
	GetThread(ctx context.Context, id domain.ID) (ThreadRecord, bool, error)
	ListThreads(ctx context.Context, limit int, beforeUpdatedMs *int64, beforeID string) ([]ThreadRecord, error)
	GetConversationByThread(ctx context.Context, agentID, threadID domain.ID) (ConversationRecord, bool, error)
	GetMessageByIdem(ctx context.Context, idemKey string) (MessageRecord, bool, error)
	ListThreadMessages(ctx context.Context, threadID domain.ID, limit int, beforeCreatedMs *int64, beforeID string) ([]MessageRecord, error)
	GetInboxItem(ctx context.Context, id domain.ID) (InboxItemRecord, bool, error)
	ListApprovalsPage(ctx context.Context, status string, limit int, beforeTS *int64, beforeID string) ([]ApprovalRecord, error)
	ListInboxPage(ctx context.Context, unreadOnly bool, limit int, beforeTS *int64, beforeID string) ([]InboxItemRecord, error)
	ListRunsPage(ctx context.Context, agentID, status string, limit int, beforeTS *int64, beforeID string) ([]RunRecord, error)
	ListUsagePage(ctx context.Context, sinceDay string, limit int, offset int) ([]DailyUsageRecord, error)
	CountQueuedByPriority(ctx context.Context) (map[int]int, error)
	ListQueuedBacklogStats(ctx context.Context) ([]QueuedBacklogRow, error)
	ListAuditLogs(ctx context.Context, limit int) ([]AuditLogRecord, error)
	GetEngineBinary(ctx context.Context, engine domain.EngineName) (EngineBinaryRecord, bool, error)
	ListTaskBlockers(ctx context.Context, taskID domain.ID) ([]domain.ID, error)
	ListAllBlockerEdges(ctx context.Context) (map[domain.ID][]domain.ID, error)
	ListDependents(ctx context.Context, blockerID domain.ID) ([]TaskRecord, error)
	ListChildTasks(ctx context.Context, parentID domain.ID) ([]TaskRecord, error)
	ListTaskEvents(ctx context.Context, taskID domain.ID, limit int) ([]TaskEventRecord, error)
	CountTaskEvents(ctx context.Context, taskID domain.ID) (int, error)
	CountPairMessagesSince(ctx context.Context, taskID, agentA, agentB domain.ID, sinceMs int64) (int, error)
	ListScopedTasks(ctx context.Context, f TaskFilter) ([]TaskRecord, error)
	ListTeamAgents(ctx context.Context, teamID domain.ID) ([]AgentRecord, error)
	ListWatchdogCandidateTasks(ctx context.Context, staleBeforeMs, nowMs int64) ([]TaskRecord, error)
	GetConversationByTask(ctx context.Context, agentID, taskID domain.ID) (ConversationRecord, bool, error)
	GetMaxWakeupHopsForTask(ctx context.Context, agentID, taskID domain.ID) (int, error)
	HasWakeupIdemKey(ctx context.Context, idemKey string) (bool, error)
	HasTaskProgressedSince(ctx context.Context, taskID domain.ID, sinceMs int64) (statusChanged bool, childrenAdded int, err error)
	GetRoutineState(ctx context.Context, slug string) (RoutineStateRecord, bool, error)
	ListDueHeartbeatAgents(ctx context.Context, nowMs int64, limit int) ([]DueHeartbeatAgent, error)
	HasHeartbeatWork(ctx context.Context, agentID domain.ID, lastRunAt *int64) (bool, error)
	ListRunsFiltered(ctx context.Context, agentID, status string, sinceMs *int64, limit int, beforeTS *int64, beforeID string) ([]RunRecord, error)
	ListAgentsPage(ctx context.Context, f AgentFilter) ([]AgentRecord, error)
	ListTeamsPage(ctx context.Context, afterSlug string, limit int) ([]TeamSummaryRecord, error)
	CountAgentsByTeam(ctx context.Context, teamID domain.ID) (int, error)
	GetGoal(ctx context.Context, id domain.ID) (GoalRecord, bool, error)
	ListGoalsPage(ctx context.Context, status string, limit int, beforeTS *int64, beforeID string) ([]GoalRecord, error)
	ListTaskEventsPage(ctx context.Context, taskID domain.ID, limit int, afterTS *int64, afterID string) ([]TaskEventRecord, error)
	ListActiveRunsByTasks(ctx context.Context, taskIDs []domain.ID) ([]RunRecord, error)
	ListThreadMessagesAsc(ctx context.Context, threadID domain.ID, afterMsgID *domain.ID, limit int) ([]MessageRecord, error)
	GetProposal(ctx context.Context, id domain.ID) (ProposalRecord, bool, error)
	ListProposals(ctx context.Context, status string, limit int) ([]ProposalRecord, error)
	GetGrant(ctx context.Context, id domain.ID) (GrantRecord, bool, error)
	ListGrantsByAgent(ctx context.Context, agentID domain.ID) ([]GrantRecord, error)
	GetConnection(ctx context.Context, name string) (ConnectionRecord, bool, error)
	ListConnections(ctx context.Context) ([]ConnectionRecord, error)
	GetToolCall(ctx context.Context, runID domain.ID, toolUseID string) (ToolCallRecord, bool, error)
	FindToolCallByIdem(ctx context.Context, idemKey string) (ToolCallRecord, bool, error)
	FindDoneToolCallByTarget(ctx context.Context, convID domain.ID, toolName, targetVal string) (ToolCallRecord, bool, error)
	ListToolCallsByRun(ctx context.Context, runID domain.ID) ([]ToolCallRecord, error)
	GetOwnerTOTP(ctx context.Context) (TOTPRecord, bool, error)
}

// Tx exposes write queries inside a single BEGIN IMMEDIATE SQLite transaction.
type Tx interface {
	Reader
	UpsertKV(ctx context.Context, key, val string, updatedAt int64) error
	InsertTeam(ctx context.Context, t TeamRecord) error
	UpsertSyncedTeam(ctx context.Context, t TeamRecord) error
	EnsureTeamBySlug(ctx context.Context, slug, newID string) (domain.ID, error)
	InsertAgent(ctx context.Context, a AgentRecord) error
	UpsertSyncedAgent(ctx context.Context, a AgentRecord) error
	SetAgentManager(ctx context.Context, agentID domain.ID, managerID *domain.ID) error
	SetAgentStatus(ctx context.Context, agentID domain.ID, status domain.AgentStatus) error
	RecordScopeViolations(ctx context.Context, agentID domain.ID, addCount int, nowMs int64) (total int, paused bool, err error)
	SetAgentCooldown(ctx context.Context, agentID domain.ID, cooldownUntil int64) error
	SetAgentLastRunAt(ctx context.Context, agentID domain.ID, lastRunAt int64) error
	InsertConversation(ctx context.Context, c ConversationRecord) error
	EnsureSystemConversation(ctx context.Context, agentID, newConvID domain.ID) (domain.ID, error)
	SetConversationWaitingFor(ctx context.Context, convID domain.ID, waitingFor *string) error
	UpdateConversationAfterRun(ctx context.Context, convID domain.ID, sessionsJSON string, lastEngine *string, addTurns int) error
	InsertTask(ctx context.Context, tk TaskRecord) error
	InsertGoal(ctx context.Context, g GoalRecord) error
	ClearTaskCheckout(ctx context.Context, runID domain.ID, updatedAt int64) error
	BlockTask(ctx context.Context, taskID domain.ID, reason string, updatedAt int64) error
	SetAgentActiveRun(ctx context.Context, agentID domain.ID, runID *domain.ID) error
	InsertRun(ctx context.Context, r RunRecord) error
	UpdateRunFinal(ctx context.Context, r RunRecord) error
	InsertRunEvent(ctx context.Context, ev RunEventRecord) error
	AdjustTeamRunning(ctx context.Context, teamID domain.ID, delta int) error
	InsertWakeupRaw(ctx context.Context, w WakeupRecord) error
	FindWakeupByIdemKey(ctx context.Context, idemKey string) (domain.ID, bool, error)
	CoalesceQueuedWakeup(ctx context.Context, agentID domain.ID, convID *domain.ID, kind domain.WakeKind, prio domain.Priority, payload string, notBefore int64) (domain.ID, bool, error)
	CountQueuedWakeups(ctx context.Context, agentID domain.ID) (int, error)
	RecomputeAgentReady(ctx context.Context, agentID domain.ID, nowMs int64) error
	ListCandidateAgents(ctx context.Context, nowMs int64, engineMask, limit int) ([]CandidateAgent, error)
	PeekTopDueWakeup(ctx context.Context, agentID domain.ID, nowMs int64) (WakeupRecord, bool, error)
	DrainDueWakeupsForConv(ctx context.Context, agentID domain.ID, origConvID *domain.ID, targetConvID, runID domain.ID, nowMs int64, limit int) ([]WakeupRecord, error)
	FinishWakeupsForRun(ctx context.Context, runID domain.ID, status string) error
	RequeueWakeupsForRun(ctx context.Context, runID domain.ID, refundAttempt bool) error
	UpsertQuotaLimit(ctx context.Context, engine domain.EngineName, limitedUntil, nowMs int64) error
	AddQuotaLedgerUsage(ctx context.Context, engine domain.EngineName, window string, windowStartMs, tokens int64) error
	AddDailyUsage(ctx context.Context, day string, agentID, teamID domain.ID, engine domain.EngineName, tokens int64) error
	SaveLearnedQuotaState(ctx context.Context, qs QuotaStateRecord) error
	InsertInboxItem(ctx context.Context, item InboxItemRecord) error
	InsertOutbox(ctx context.Context, rec OutboxRecord) error
	InsertApproval(ctx context.Context, ap ApprovalRecord) error
	UpdateApprovalDecision(ctx context.Context, id domain.ID, status string, decisionKey *string, decidedAt *int64) error
	ConsumeAllowedApproval(ctx context.Context, agentID domain.ID, toolName, inputHash string, nowMs int64) (bool, error)
	ListExpiredPendingApprovals(ctx context.Context, nowMs int64) ([]ApprovalRecord, error)
	ResolveInboxByRef(ctx context.Context, refKind, refID string, resolvedAt int64) error
	PromoteDueWakeups(ctx context.Context, nowMs int64) (int, error)
	ReconcileTeamRunning(ctx context.Context) error
	PruneTerminalWakeupsBefore(ctx context.Context, cutoffMs int64, limit int) (int, error)
	SetTeamRunningRaw(ctx context.Context, teamID domain.ID, running int) error
	UpsertOwnerUser(ctx context.Context, u UserRecord) error
	InsertAuthSession(ctx context.Context, s AuthSessionRecord) error
	DeleteAuthSession(ctx context.Context, tokenHash string) error
	InsertThread(ctx context.Context, th ThreadRecord) error
	TouchThread(ctx context.Context, threadID domain.ID, msgAt int64) error
	EnsureThreadConversation(ctx context.Context, agentID, threadID, newConvID domain.ID) (domain.ID, error)
	ResetThreadConversation(ctx context.Context, agentID, threadID domain.ID) error
	InsertMessage(ctx context.Context, m MessageRecord) error
	MarkInboxItemRead(ctx context.Context, id domain.ID, readAt int64) error
	ResolveInboxItem(ctx context.Context, id domain.ID, resolvedAt int64) error
	AppendAuditLog(ctx context.Context, rec AuditLogRecord) error
	UpsertEngineBinary(ctx context.Context, rec EngineBinaryRecord) error
	RecoverCrashState(ctx context.Context, nowMs int64, graceful bool, newID func() domain.ID) (int, error)
	EnsureTaskConversation(ctx context.Context, agentID, taskID, newConvID domain.ID) (domain.ID, error)
	InsertTaskBlocker(ctx context.Context, taskID, blockedBy domain.ID) error
	CheckoutTaskAtomic(ctx context.Context, taskID, agentID, runID domain.ID, updatedAt int64) (bool, error)
	UpdateTaskStatus(ctx context.Context, taskID domain.ID, status domain.TaskStatus, reason *domain.BlockedReason, updatedAt int64) error
	UpdateTaskReviewState(ctx context.Context, taskID domain.ID, status domain.TaskStatus, reviewerID *domain.ID, reviewRounds int, updatedAt int64) error
	UpdateTaskAssignee(ctx context.Context, taskID domain.ID, assigneeID *domain.ID, status domain.TaskStatus, reason *domain.BlockedReason, updatedAt int64) error
	UpdateTaskIdleRuns(ctx context.Context, taskID domain.ID, idleRuns int, updatedAt int64) error
	InsertTaskEvent(ctx context.Context, ev TaskEventRecord) error
	ExpirePendingApprovalsByTasks(ctx context.Context, taskIDs []domain.ID, nowMs int64) (int, error)
	UpsertRoutineState(ctx context.Context, st RoutineStateRecord) error
	SetAgentHeartbeatSchedule(ctx context.Context, agentID domain.ID, cronExpr string, nextAtMs *int64) error
	MarkOutboxSent(ctx context.Context, id int64, sentAt int64) error
	DeferOutboxUntil(ctx context.Context, id int64, nextAttemptAt int64) error
	RecordOutboxFailure(ctx context.Context, id int64, attempts int, nextAttemptAt int64, dropSentAt *int64) error
	SetThreadLearnedThrough(ctx context.Context, threadID, msgID domain.ID) error
	InsertProposal(ctx context.Context, p ProposalRecord) error
	UpdateProposalStatus(ctx context.Context, id domain.ID, status string, decidedAt int64) error
	InsertGrant(ctx context.Context, g GrantRecord) error
	RevokeGrant(ctx context.Context, id domain.ID, revokedAt int64) error
	IncrementGrantUse(ctx context.Context, id domain.ID, day string) (allowed bool, err error)
	UpsertConnection(ctx context.Context, c ConnectionRecord) error
	DeleteConnection(ctx context.Context, name string) error
	InsertToolCallStarted(ctx context.Context, tc ToolCallRecord) error
	TransitionToolCallStatus(ctx context.Context, runID domain.ID, toolUseID, fromStatus, toStatus string, result *string, endedAt *int64) (bool, error)
	SetOwnerTOTPPending(ctx context.Context, userID domain.ID, pending []byte, updatedAt int64) error
	ConfirmOwnerTOTP(ctx context.Context, userID domain.ID, secret []byte, updatedAt int64) error
	RecordOwnerTOTPFailure(ctx context.Context, userID domain.ID, attempts int, lockedUntil *int64, updatedAt int64) error
	ResetOwnerTOTPFailures(ctx context.Context, userID domain.ID, updatedAt int64) error
}

// Store owns transactional writes (single writer) and concurrent read queries.
type Store interface {
	WithTx(ctx context.Context, fn func(tx Tx) error) error
	Read() Reader
}

// PromptBuilder assembles bounded system and per-run user prompts (implemented in P1.12).
type PromptBuilder interface {
	System(ctx context.Context, agentID domain.ID) (string, error)
	RunMessage(ctx context.Context, c Claimed) (string, error)
}

// Vault abstracts the git-backed content vault (implemented in P1.16).
type Vault interface {
	EnsureInstalled(ctx context.Context) error
	CommitPaths(ctx context.Context, paths []string, msg string) (sha string, err error)
	PullIfIdle(ctx context.Context, runsActive int) error
	Push(ctx context.Context) error
	Read(ctx context.Context, rel string) ([]byte, error)
	Write(ctx context.Context, rel string, data []byte) error
	Remove(ctx context.Context, rel string) error
	ListDir(ctx context.Context, rel string) ([]string, error)
	HeadSHA(ctx context.Context) (string, error)
	RevertLastByPrefix(ctx context.Context, prefix string) (string, error)
	MirrorSkills(ctx context.Context) error
	RefuseIfProjectSettings(ctx context.Context) error
}

// EngineCaps describes security and protocol capabilities of a CLI driver (Spec 8.4).
type EngineCaps struct {
	PreToolGate, ToolAllowlist, Sandbox, PerRunMCP, Resume, StructuredOut bool
}

// EngineHealth reports binary installation, login state, version, and foreign MCP servers.
type EngineHealth struct {
	Installed, LoggedIn bool
	Version             string
	ForeignMCP          []string
}

// RunSpec configures a single non-interactive CLI run.
type RunSpec struct {
	RunID, AgentID domain.ID
	AgentSlug      string
	Cwd            string
	AddDirs        []string
	SystemPrompt   string
	Prompt         string
	Model          string
	SessionID      string
	RunToken       string
	Autonomy       domain.Autonomy
	JSONSchema     string
	ExtraMCP       []MCPServerSpec
}

// MCPServerSpec describes an additional per-session MCP server attached to a CLI run (e.g. browser).
type MCPServerSpec struct {
	Name, Command string
	Args, Env     []string
}

// Browser manages seat-limited stealth browser leases and MCP specs (Spec 8.10 & S1).
type Browser interface {
	Acquire(ctx context.Context, runID domain.ID, prio domain.Priority, wait time.Duration) (endpoint string, err error)
	Release(ctx context.Context, runID domain.ID) error
	MCPSpec(endpoint string) MCPServerSpec
}

// BrowserHandoff starts a loopback human takeover session and parks the conversation waiting on owner (Spec 8.10 & P4.02).
type BrowserHandoff interface {
	Handoff(ctx context.Context, agentID, runID, convID domain.ID, reason, origin string) (string, error)
}

// OutboundInvoker invokes a registered external MCP connection tool outside DB transactions.
type OutboundInvoker interface {
	Invoke(ctx context.Context, kind string, decryptedConfig []byte, tool string, input json.RawMessage) (string, error)
}

// Notification represents a redacted owner alert dispatched from the transactional outbox.
type Notification struct {
	ID      string
	Topic   string
	Kind    string
	Title   string
	Body    string
	Urgency string
	LinkURL string
}

// Notifier delivers owner notifications to external notify-only channels such as Telegram (Spec 8.5.3 & 8.15).
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// EngineEvent is a normalized event emitted by an Engine driver during a run.
type EngineEvent struct {
	Type                      string // session | text_delta | tool_call | tool_result | usage | limit | final | error
	SessionID                 string
	Text                      string
	ToolUseID, ToolName       string
	InputPreview              string
	ToolOK                    bool
	InputTokens, OutputTokens int64
	LimitResetAt              *time.Time
	ErrKind, ErrMessage       string
}

// ToolAsk represents an engine pre-tool permission inquiry.
type ToolAsk struct {
	RunToken, ToolName string
	Input              json.RawMessage
	Path, SearchRoot   string
}

// PermissionFunc evaluates a pre-tool inquiry synchronously without blocking on human input.
type PermissionFunc func(ctx context.Context, ask ToolAsk) domain.Decision

// Engine abstracts a headless CLI subscription driver (agy, claude, codex).
type Engine interface {
	Name() domain.EngineName
	Caps() EngineCaps
	Run(ctx context.Context, spec RunSpec, perm PermissionFunc) (<-chan EngineEvent, error)
	Health(ctx context.Context) EngineHealth
}

// MemoryHit represents a single episodic recall item returned by EpisodicMemory.Search.
type MemoryHit struct {
	ID             domain.ID
	AuthorAgentID  domain.ID
	ConversationID domain.ID
	Scope          string
	Content        string
	Score          float64
	CreatedAt      int64
}

// EpisodicMemory stores and searches agent/team episodic memories (Spec 8.9.4 & S2).
type EpisodicMemory interface {
	Search(ctx context.Context, agentID domain.ID, query string, limit int) ([]MemoryHit, error)
	Remember(ctx context.Context, agentID domain.ID, content string, concepts []string) error
	Healthy() bool
}

// TxMemoryWriter is an optional extension allowing SQLite-backed EpisodicMemory to write inside Finalize's transaction.
type TxMemoryWriter interface {
	RememberInTx(ctx context.Context, tx Tx, agentID, convID domain.ID, content string) error
}

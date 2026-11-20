package app

import "max/internal/domain"

// GrantRecord represents a row in the grants table.
type GrantRecord struct {
	ID                     domain.ID
	AgentID                domain.ID
	Connection             string
	Tool                   string
	ConstraintsJSON        string
	AllowedRootOriginsJSON string
	MaxPerDay              *int
	UsedToday              int
	UsedDay                string
	ExpiresAt              *int64
	RevokedAt              *int64
	CreatedAt              int64
}

// ConnectionRecord represents an encrypted row in the connections table.
type ConnectionRecord struct {
	Name             string
	Kind             string
	ConfigCiphertext []byte
	ToolsJSON        string
	Enabled          bool
	CreatedAt        int64
	UpdatedAt        int64
}

// ToolCallRecord represents a row in the tool_calls idempotency ledger.
type ToolCallRecord struct {
	RunID          domain.ID
	ToolUseID      string
	ConversationID *domain.ID
	ToolName       string
	Class          string
	TargetValue    *string
	InputJSON      *string
	IdempotencyKey *string
	Status         string
	Result         *string
	StartedAt      int64
	EndedAt        *int64
}

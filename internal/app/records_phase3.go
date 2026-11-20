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

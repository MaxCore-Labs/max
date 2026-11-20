package grants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/domain"
)

// NewGrant describes owner-defined standing permission parameters for Service.Create.
type NewGrant struct {
	AgentID            domain.ID
	Connection         string
	Tool               string
	Constraints        domain.Constraints
	AllowedRootOrigins []domain.RootOrigin
	MaxPerDay          *int
	ExpiresAt          *time.Time
}

// Service manages owner-issued standing Grants, daily usage caps, and revocations (Spec 8.6).
type Service struct {
	store app.Store
	clock app.Clock
	ids   app.IDGen
}

// NewService constructs a Grants Service.
func NewService(store app.Store, clock app.Clock, ids app.IDGen) *Service {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Service{store: store, clock: clock, ids: ids}
}

// Create validates and persists a new constrained Grant (never blank-check: requires >= 1 constraint or max_per_day).
func (s *Service) Create(ctx context.Context, g NewGrant) (domain.ID, error) {
	if strings.TrimSpace(g.AgentID) == "" || strings.TrimSpace(g.Connection) == "" || strings.TrimSpace(g.Tool) == "" {
		return "", errors.New("grant requires agent_id, connection, and tool")
	}
	if !g.Constraints.HasAny() && (g.MaxPerDay == nil || *g.MaxPerDay <= 0) {
		return "", errors.New("grant must specify at least one parameter constraint or max_per_day")
	}
	if g.MaxPerDay != nil && *g.MaxPerDay <= 0 {
		return "", errors.New("max_per_day must be positive")
	}
	origins := g.AllowedRootOrigins
	if len(origins) == 0 {
		origins = []domain.RootOrigin{domain.OriginOwner, domain.OriginRoutine}
	}
	cJSON, _ := json.Marshal(g.Constraints)
	oJSON, _ := json.Marshal(origins)

	var expMs *int64
	if g.ExpiresAt != nil {
		ms := g.ExpiresAt.UnixMilli()
		expMs = &ms
	}
	id := s.ids.New()
	nowMs := s.clock.Now().UnixMilli()

	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertGrant(ctx, app.GrantRecord{
			ID:                     id,
			AgentID:                g.AgentID,
			Connection:             strings.TrimSpace(g.Connection),
			Tool:                   strings.TrimSpace(g.Tool),
			ConstraintsJSON:        string(cJSON),
			AllowedRootOriginsJSON: string(oJSON),
			MaxPerDay:              g.MaxPerDay,
			ExpiresAt:              expMs,
			CreatedAt:              nowMs,
		}); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{
			"agent_id":   g.AgentID,
			"connection": g.Connection,
			"tool":       g.Tool,
		})
		return tx.AppendAuditLog(ctx, app.AuditLogRecord{
			TS:         nowMs,
			Actor:      "owner",
			Action:     "grant.create",
			Ref:        id,
			DetailJSON: string(detail),
		})
	})
	if err != nil {
		return "", fmt.Errorf("create grant: %w", err)
	}
	return id, nil
}

// Revoke immediately revokes an existing grant and appends an audit log entry.
func (s *Service) Revoke(ctx context.Context, id domain.ID) error {
	nowMs := s.clock.Now().UnixMilli()
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.RevokeGrant(ctx, id, nowMs); err != nil {
			return err
		}
		return tx.AppendAuditLog(ctx, app.AuditLogRecord{
			TS:         nowMs,
			Actor:      "owner",
			Action:     "grant.revoke",
			Ref:        id,
			DetailJSON: `{}`,
		})
	})
}

// Active returns all unrevoked, unexpired grants for agentID that still have remaining daily quota on now's UTC date.
func (s *Service) Active(ctx context.Context, agentID domain.ID, now time.Time) ([]domain.Grant, error) {
	rows, err := s.store.Read().ListGrantsByAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	day := now.UTC().Format("2006-01-02")
	var out []domain.Grant
	for _, r := range rows {
		if r.RevokedAt != nil {
			continue
		}
		var exp *time.Time
		if r.ExpiresAt != nil {
			t := time.UnixMilli(*r.ExpiresAt)
			if !now.Before(t) {
				continue
			}
			exp = &t
		}
		if r.MaxPerDay != nil && r.UsedDay == day && r.UsedToday >= *r.MaxPerDay {
			continue
		}
		var c domain.Constraints
		_ = json.Unmarshal([]byte(r.ConstraintsJSON), &c)
		var origins []domain.RootOrigin
		_ = json.Unmarshal([]byte(r.AllowedRootOriginsJSON), &origins)
		out = append(out, domain.Grant{
			ID:                 r.ID,
			Connection:         r.Connection,
			Tool:               r.Tool,
			Constraints:        c,
			AllowedRootOrigins: origins,
			ExpiresAt:          exp,
		})
	}
	return out, nil
}

// CountUse atomically increments the daily counter for id inside tx and reports whether max_per_day permits execution.
func (s *Service) CountUse(ctx context.Context, tx app.Tx, id domain.ID, now time.Time) (bool, error) {
	return tx.IncrementGrantUse(ctx, id, now.UTC().Format("2006-01-02"))
}

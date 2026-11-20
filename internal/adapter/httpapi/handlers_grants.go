package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/grants"
	"max/internal/domain"
)

func (s *Server) ListAgentGrants(ctx context.Context, req gen.ListAgentGrantsRequestObject) (gen.ListAgentGrantsResponseObject, error) {
	rows, err := s.deps.Store.Read().ListGrantsByAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	items := make([]gen.GrantItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, toGenGrant(r))
	}
	return gen.ListAgentGrants200JSONResponse{Items: items}, nil
}

func (s *Server) CreateAgentGrant(ctx context.Context, req gen.CreateAgentGrantRequestObject) (gen.CreateAgentGrantResponseObject, error) {
	if s.deps.Grants == nil || req.Body == nil {
		return gen.CreateAgentGrant400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "invalid grant request"},
		}, nil
	}
	var c domain.Constraints
	if req.Body.Constraints != nil {
		if req.Body.Constraints.Equals != nil {
			c.Equals = *req.Body.Constraints.Equals
		}
		if req.Body.Constraints.OneOf != nil {
			c.OneOf = *req.Body.Constraints.OneOf
		}
		if req.Body.Constraints.MaxNumber != nil {
			c.MaxNumber = *req.Body.Constraints.MaxNumber
		}
	}
	var origins []domain.RootOrigin
	if req.Body.AllowedRootOrigins != nil {
		for _, o := range *req.Body.AllowedRootOrigins {
			origins = append(origins, domain.RootOrigin(o))
		}
	}
	var exp *time.Time
	if req.Body.ExpiresAt != nil {
		t := time.UnixMilli(*req.Body.ExpiresAt)
		exp = &t
	}

	id, err := s.deps.Grants.Create(ctx, grants.NewGrant{
		AgentID:            req.AgentId,
		Connection:         req.Body.Connection,
		Tool:               req.Body.Tool,
		Constraints:        c,
		AllowedRootOrigins: origins,
		MaxPerDay:          req.Body.MaxPerDay,
		ExpiresAt:          exp,
	})
	if err != nil {
		return gen.CreateAgentGrant400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()},
		}, nil
	}
	rec, _, err := s.deps.Store.Read().GetGrant(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.CreateAgentGrant201JSONResponse(toGenGrant(rec)), nil
}

func (s *Server) RevokeGrant(ctx context.Context, req gen.RevokeGrantRequestObject) (gen.RevokeGrantResponseObject, error) {
	if s.deps.Grants != nil {
		if err := s.deps.Grants.Revoke(ctx, req.GrantId); err != nil {
			return nil, err
		}
	}
	return gen.RevokeGrant204Response{}, nil
}

func toGenGrant(r app.GrantRecord) gen.GrantItem {
	var c domain.Constraints
	_ = json.Unmarshal([]byte(r.ConstraintsJSON), &c)
	var gc gen.GrantConstraints
	if len(c.Equals) > 0 {
		eq := c.Equals
		gc.Equals = &eq
	}
	if len(c.OneOf) > 0 {
		oo := c.OneOf
		gc.OneOf = &oo
	}
	if len(c.MaxNumber) > 0 {
		mn := c.MaxNumber
		gc.MaxNumber = &mn
	}
	var origins []string
	_ = json.Unmarshal([]byte(r.AllowedRootOriginsJSON), &origins)
	if origins == nil {
		origins = []string{"owner", "routine"}
	}
	return gen.GrantItem{
		Id:                 r.ID,
		AgentId:            r.AgentID,
		Connection:         r.Connection,
		Tool:               r.Tool,
		Constraints:        gc,
		AllowedRootOrigins: origins,
		MaxPerDay:          r.MaxPerDay,
		UsedToday:          r.UsedToday,
		ExpiresAt:          r.ExpiresAt,
		RevokedAt:          r.RevokedAt,
		CreatedAt:          r.CreatedAt,
	}
}

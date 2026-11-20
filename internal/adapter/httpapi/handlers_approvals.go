package httpapi

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/approvals"
)

func (s *Server) ListApprovals(ctx context.Context, req gen.ListApprovalsRequestObject) (gen.ListApprovalsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListApprovals400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	status := ""
	if req.Params.Status != nil {
		status = strings.TrimSpace(*req.Params.Status)
	}
	ts, id := parsePageCursor(req.Params.Cursor)
	rows, err := s.deps.Store.Read().ListApprovalsPage(ctx, status, limit+1, ts, id)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		next = makePageCursor(last.RequestedAt, last.ID)
		rows = rows[:limit]
	}
	items := make([]gen.Approval, 0, len(rows))
	for _, ap := range rows {
		items = append(items, toGenApproval(ap))
	}
	return gen.ListApprovals200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) DecideApproval(ctx context.Context, req gen.DecideApprovalRequestObject) (gen.DecideApprovalResponseObject, error) {
	idemKey := ""
	if req.Params.IdempotencyKey != nil {
		idemKey = strings.TrimSpace(*req.Params.IdempotencyKey)
	}
	if idemKey == "" {
		return gen.DecideApproval400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "Idempotency-Key header is required"},
		}, nil
	}
	if req.Body == nil || s.deps.Approvals == nil {
		return gen.DecideApproval400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing request body"},
		}, nil
	}

	before, existed, _ := s.deps.Store.Read().GetApproval(ctx, req.ApprovalId)
	if !existed {
		return gen.DecideApproval400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "approval not found"},
		}, nil
	}
	wasPending := before.Status == "pending"

	if err := s.deps.Approvals.Decide(ctx, req.ApprovalId, req.Body.Allow, idemKey); err != nil {
		if errors.Is(err, approvals.ErrAlreadyDecided) {
			return gen.DecideApproval409JSONResponse{Error: err.Error()}, nil
		}
		return nil, err
	}

	if wasPending {
		_ = s.appendAudit(ctx, "approval.decide", req.ApprovalId, map[string]any{
			"allow":   req.Body.Allow,
			"preview": before.Preview,
		})
	}

	after, _, err := s.deps.Store.Read().GetApproval(ctx, req.ApprovalId)
	if err != nil {
		return nil, err
	}
	return gen.DecideApproval200JSONResponse(toGenApproval(after)), nil
}

func (s *Server) StreamEvents(context.Context, gen.StreamEventsRequestObject) (gen.StreamEventsResponseObject, error) {
	return gen.StreamEvents200TexteventStreamResponse{Body: bytes.NewReader(nil)}, nil
}

func toGenApproval(ap app.ApprovalRecord) gen.Approval {
	return gen.Approval{
		Id:             ap.ID,
		RunId:          ap.RunID,
		AgentId:        ap.AgentID,
		ConversationId: ap.ConversationID,
		ToolName:       ap.ToolName,
		Preview:        ap.Preview,
		Reason:         ap.Reason,
		Status:         ap.Status,
		RequestedAt:    ap.RequestedAt,
		ExpiresAt:      ap.ExpiresAt,
		DecidedAt:      ap.DecidedAt,
	}
}

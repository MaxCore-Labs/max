package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/platform"
)

func (s *Server) ListInbox(ctx context.Context, req gen.ListInboxRequestObject) (gen.ListInboxResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListInbox400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	unreadOnly := req.Params.UnreadOnly != nil && *req.Params.UnreadOnly
	ts, id := parsePageCursor(req.Params.Cursor)
	rows, err := s.deps.Store.Read().ListInboxPage(ctx, unreadOnly, limit+1, ts, id)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		next = makePageCursor(last.CreatedAt, last.ID)
		rows = rows[:limit]
	}
	items := make([]gen.InboxItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, toGenInboxItem(r))
	}
	return gen.ListInbox200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) MarkInboxRead(ctx context.Context, req gen.MarkInboxReadRequestObject) (gen.MarkInboxReadResponseObject, error) {
	nowMs := s.deps.Clock.Now().UnixMilli()
	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.MarkInboxItemRead(ctx, req.ItemId, nowMs)
	})
	if err != nil {
		return nil, err
	}
	return gen.MarkInboxRead204Response{}, nil
}

func (s *Server) AnswerOwnerQuestion(ctx context.Context, req gen.AnswerOwnerQuestionRequestObject) (gen.AnswerOwnerQuestionResponseObject, error) {
	if req.Body == nil {
		return nil, fmt.Errorf("missing answer body")
	}
	answer := platform.Redact(strings.TrimSpace(req.Body.Answer))
	nowMs := s.deps.Clock.Now().UnixMilli()

	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		it, ok, err := tx.GetInboxItem(ctx, req.ItemId)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("inbox item %q not found", req.ItemId)
		}
		if it.ResolvedAt != nil {
			return nil
		}
		if err := tx.ResolveInboxItem(ctx, req.ItemId, nowMs); err != nil {
			return err
		}
		if it.ConversationID != nil && *it.ConversationID != "" {
			if err := tx.SetConversationWaitingFor(ctx, *it.ConversationID, nil); err != nil {
				return err
			}
		}
		if it.AgentID != nil && *it.AgentID != "" && s.deps.Actors != nil {
			payload, _ := json.Marshal(map[string]string{
				"inbox_id": req.ItemId,
				"answer":   answer,
			})
			var idemPtr *string
			if req.Params.IdempotencyKey != nil && strings.TrimSpace(*req.Params.IdempotencyKey) != "" {
				cp := "owner-ans:" + strings.TrimSpace(*req.Params.IdempotencyKey)
				idemPtr = &cp
			}
			if _, _, err := s.deps.Actors.Wake(ctx, tx, actor.WakeInput{
				AgentID:        *it.AgentID,
				Kind:           domain.WakeOwnerReply,
				Priority:       domain.PrioInteractive,
				ConversationID: it.ConversationID,
				Payload:        payload,
				IdempotencyKey: idemPtr,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	updated, _, err := s.deps.Store.Read().GetInboxItem(ctx, req.ItemId)
	if err != nil {
		return nil, err
	}
	return gen.AnswerOwnerQuestion200JSONResponse(toGenInboxItem(updated)), nil
}

func toGenInboxItem(it app.InboxItemRecord) gen.InboxItem {
	var choices []string
	_ = json.Unmarshal([]byte(it.ChoicesJSON), &choices)
	if choices == nil {
		choices = []string{}
	}
	return gen.InboxItem{
		Id:             it.ID,
		Kind:           it.Kind,
		Title:          it.Title,
		Body:           it.Body,
		RefKind:        it.RefKind,
		RefId:          it.RefID,
		ConversationId: it.ConversationID,
		AgentId:        it.AgentID,
		Choices:        choices,
		Urgency:        it.Urgency,
		ReadAt:         it.ReadAt,
		ResolvedAt:     it.ResolvedAt,
		CreatedAt:      it.CreatedAt,
	}
}

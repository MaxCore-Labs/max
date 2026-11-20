package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/chat"
)

const maxPageLimit = 100

func validateLimit(limit *int) (int, error) {
	if limit == nil {
		return 50, nil
	}
	if *limit < 1 || *limit > maxPageLimit {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return *limit, nil
}

func (s *Server) ListThreads(ctx context.Context, req gen.ListThreadsRequestObject) (gen.ListThreadsResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListThreads400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	if s.deps.Chat == nil {
		return gen.ListThreads200JSONResponse{Items: []gen.Thread{}}, nil
	}
	cursor := ""
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	rows, next, err := s.deps.Chat.ListThreads(ctx, limit, cursor)
	if err != nil {
		return nil, err
	}
	items := make([]gen.Thread, 0, len(rows))
	for _, r := range rows {
		items = append(items, toGenThread(r))
	}
	return gen.ListThreads200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) CreateThread(ctx context.Context, req gen.CreateThreadRequestObject) (gen.CreateThreadResponseObject, error) {
	if s.deps.Chat == nil {
		return gen.CreateThread201JSONResponse{}, nil
	}
	var agentID, title string
	if req.Body != nil {
		if req.Body.AgentId != nil {
			agentID = *req.Body.AgentId
		}
		if req.Body.Title != nil {
			title = *req.Body.Title
		}
	}
	id, err := s.deps.Chat.CreateThread(ctx, agentID, title)
	if err != nil {
		return nil, err
	}
	th, _, _ := s.deps.Store.Read().GetThread(ctx, id)
	return gen.CreateThread201JSONResponse(toGenThread(th)), nil
}

func (s *Server) GetThread(ctx context.Context, req gen.GetThreadRequestObject) (gen.GetThreadResponseObject, error) {
	if s.deps.Chat == nil {
		return gen.GetThread404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "not found"}}, nil
	}
	snap, err := s.deps.Chat.Snapshot(ctx, req.ThreadId)
	if errors.Is(err, chat.ErrNotFound) {
		return gen.GetThread404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "thread not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	msgs := make([]gen.Message, 0, len(snap.Messages))
	for _, m := range snap.Messages {
		msgs = append(msgs, toGenMessage(m))
	}
	out := gen.ThreadSnapshot{
		Thread:      toGenThread(snap.Thread),
		Messages:    msgs,
		ActiveRunId: snap.ActiveRunID,
	}
	if snap.LiveText != "" {
		lt := snap.LiveText
		out.LiveText = &lt
	}
	if snap.BusyWith != nil {
		out.BusyWith = &gen.BusyInfo{
			RunId:          snap.BusyWith.RunID,
			Title:          snap.BusyWith.Title,
			MinutesRunning: snap.BusyWith.MinutesRunning,
		}
	}
	return gen.GetThread200JSONResponse(out), nil
}

func (s *Server) ListMessages(ctx context.Context, req gen.ListMessagesRequestObject) (gen.ListMessagesResponseObject, error) {
	limit, err := validateLimit(req.Params.Limit)
	if err != nil {
		return gen.ListMessages400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
	}
	if s.deps.Chat == nil {
		return gen.ListMessages200JSONResponse{Items: []gen.Message{}}, nil
	}
	before := ""
	if req.Params.Before != nil {
		before = *req.Params.Before
	}
	rows, next, err := s.deps.Chat.ListMessages(ctx, req.ThreadId, limit, before)
	if err != nil {
		return nil, err
	}
	items := make([]gen.Message, 0, len(rows))
	for _, m := range rows {
		items = append(items, toGenMessage(m))
	}
	return gen.ListMessages200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) SendMessage(ctx context.Context, req gen.SendMessageRequestObject) (gen.SendMessageResponseObject, error) {
	if s.deps.Chat == nil || req.Body == nil {
		return gen.SendMessage202JSONResponse{}, nil
	}
	var attachments []string
	if req.Body.Attachments != nil {
		attachments = *req.Body.Attachments
	}
	idemKey := ""
	if req.Params.IdempotencyKey != nil {
		idemKey = *req.Params.IdempotencyKey
	}
	msgID, wakeID, err := s.deps.Chat.Send(ctx, req.ThreadId, req.Body.Content, attachments, idemKey)
	if err != nil {
		return nil, err
	}
	return gen.SendMessage202JSONResponse{
		MessageId: msgID,
		WakeupId:  wakeID,
	}, nil
}

func (s *Server) ResetThread(ctx context.Context, req gen.ResetThreadRequestObject) (gen.ResetThreadResponseObject, error) {
	if s.deps.Chat != nil {
		if err := s.deps.Chat.Reset(ctx, req.ThreadId); err != nil {
			return nil, err
		}
	}
	return gen.ResetThread204Response{}, nil
}

func toGenThread(th app.ThreadRecord) gen.Thread {
	return gen.Thread{
		Id:        th.ID,
		AgentId:   th.AgentID,
		Title:     th.Title,
		LastMsgAt: th.LastMsgAt,
		CreatedAt: th.CreatedAt,
		UpdatedAt: th.UpdatedAt,
	}
}

func toGenMessage(m app.MessageRecord) gen.Message {
	var att []string
	_ = json.Unmarshal([]byte(m.AttachmentsJSON), &att)
	if att == nil {
		att = []string{}
	}
	return gen.Message{
		Id:          m.ID,
		ThreadId:    m.ThreadID,
		Role:        gen.MessageRole(m.Role),
		Content:     m.Content,
		Attachments: att,
		RunId:       m.RunID,
		CreatedAt:   m.CreatedAt,
	}
}

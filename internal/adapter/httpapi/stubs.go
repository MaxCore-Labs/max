package httpapi

import (
	"bytes"
	"context"

	"max/internal/adapter/httpapi/gen"
)

// Stubs below satisfy gen.StrictServerInterface in P1.21 and are replaced with real handlers in P1.22–P1.24.

func (s *Server) ListThreads(context.Context, gen.ListThreadsRequestObject) (gen.ListThreadsResponseObject, error) {
	return gen.ListThreads200JSONResponse{Items: []gen.Thread{}}, nil
}

func (s *Server) CreateThread(context.Context, gen.CreateThreadRequestObject) (gen.CreateThreadResponseObject, error) {
	return gen.CreateThread201JSONResponse{}, nil
}

func (s *Server) GetThread(context.Context, gen.GetThreadRequestObject) (gen.GetThreadResponseObject, error) {
	return gen.GetThread404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "not found"}}, nil
}

func (s *Server) ListMessages(context.Context, gen.ListMessagesRequestObject) (gen.ListMessagesResponseObject, error) {
	return gen.ListMessages200JSONResponse{Items: []gen.Message{}}, nil
}

func (s *Server) SendMessage(context.Context, gen.SendMessageRequestObject) (gen.SendMessageResponseObject, error) {
	return gen.SendMessage202JSONResponse{}, nil
}

func (s *Server) ResetThread(context.Context, gen.ResetThreadRequestObject) (gen.ResetThreadResponseObject, error) {
	return gen.ResetThread204Response{}, nil
}

func (s *Server) ListRuns(context.Context, gen.ListRunsRequestObject) (gen.ListRunsResponseObject, error) {
	return gen.ListRuns200JSONResponse{Items: []gen.Run{}}, nil
}

func (s *Server) GetRun(context.Context, gen.GetRunRequestObject) (gen.GetRunResponseObject, error) {
	return gen.GetRun404JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "not found"}}, nil
}

func (s *Server) CancelRun(context.Context, gen.CancelRunRequestObject) (gen.CancelRunResponseObject, error) {
	return gen.CancelRun200JSONResponse{}, nil
}

func (s *Server) ListRunEvents(context.Context, gen.ListRunEventsRequestObject) (gen.ListRunEventsResponseObject, error) {
	return gen.ListRunEvents200JSONResponse{Items: []gen.RunEvent{}}, nil
}

func (s *Server) StreamRun(context.Context, gen.StreamRunRequestObject) (gen.StreamRunResponseObject, error) {
	return gen.StreamRun200TexteventStreamResponse{Body: bytes.NewReader(nil)}, nil
}

func (s *Server) ListApprovals(context.Context, gen.ListApprovalsRequestObject) (gen.ListApprovalsResponseObject, error) {
	return gen.ListApprovals200JSONResponse{Items: []gen.Approval{}}, nil
}

func (s *Server) DecideApproval(context.Context, gen.DecideApprovalRequestObject) (gen.DecideApprovalResponseObject, error) {
	return gen.DecideApproval400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "not implemented"}}, nil
}

func (s *Server) ListInbox(context.Context, gen.ListInboxRequestObject) (gen.ListInboxResponseObject, error) {
	return gen.ListInbox200JSONResponse{Items: []gen.InboxItem{}}, nil
}

func (s *Server) MarkInboxRead(context.Context, gen.MarkInboxReadRequestObject) (gen.MarkInboxReadResponseObject, error) {
	return gen.MarkInboxRead204Response{}, nil
}

func (s *Server) AnswerOwnerQuestion(context.Context, gen.AnswerOwnerQuestionRequestObject) (gen.AnswerOwnerQuestionResponseObject, error) {
	return gen.AnswerOwnerQuestion200JSONResponse{}, nil
}

func (s *Server) GetUsage(context.Context, gen.GetUsageRequestObject) (gen.GetUsageResponseObject, error) {
	return gen.GetUsage200JSONResponse{Items: []gen.DailyUsageRow{}}, nil
}

func (s *Server) GetQuota(context.Context, gen.GetQuotaRequestObject) (gen.GetQuotaResponseObject, error) {
	return gen.GetQuota200JSONResponse{Engines: []gen.EngineQuotaInfo{}}, nil
}

func (s *Server) UpdateQuotaSettings(context.Context, gen.UpdateQuotaSettingsRequestObject) (gen.UpdateQuotaSettingsResponseObject, error) {
	return gen.UpdateQuotaSettings200JSONResponse{Engines: []gen.EngineQuotaInfo{}}, nil
}

func (s *Server) GetStatus(context.Context, gen.GetStatusRequestObject) (gen.GetStatusResponseObject, error) {
	return gen.GetStatus200JSONResponse{
		Engines: []gen.EngineStatusItem{},
		Queue:   gen.QueueSummary{ByPriority: map[string]int{}},
		Deps:    map[string]string{},
	}, nil
}

func (s *Server) GetSettings(context.Context, gen.GetSettingsRequestObject) (gen.GetSettingsResponseObject, error) {
	return gen.GetSettings200JSONResponse{Engines: []gen.EngineSettingItem{}}, nil
}

func (s *Server) UpdateSettings(context.Context, gen.UpdateSettingsRequestObject) (gen.UpdateSettingsResponseObject, error) {
	return gen.UpdateSettings200JSONResponse{Engines: []gen.EngineSettingItem{}}, nil
}

func (s *Server) StreamEvents(context.Context, gen.StreamEventsRequestObject) (gen.StreamEventsResponseObject, error) {
	return gen.StreamEvents200TexteventStreamResponse{Body: bytes.NewReader(nil)}, nil
}

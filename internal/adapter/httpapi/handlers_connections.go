package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app/connections"
)

func (s *Server) ListConnections(ctx context.Context, _ gen.ListConnectionsRequestObject) (gen.ListConnectionsResponseObject, error) {
	if s.deps.Connections == nil {
		return gen.ListConnections200JSONResponse{Items: []gen.ConnectionItem{}}, nil
	}
	list, err := s.deps.Connections.List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]gen.ConnectionItem, 0, len(list))
	for _, c := range list {
		items = append(items, toGenConnection(c))
	}
	return gen.ListConnections200JSONResponse{Items: items}, nil
}

func (s *Server) UpsertConnection(ctx context.Context, req gen.UpsertConnectionRequestObject) (gen.UpsertConnectionResponseObject, error) {
	if s.deps.Connections == nil || req.Body == nil {
		return gen.UpsertConnection400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "invalid connection request"},
		}, nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return gen.UpsertConnection400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "connection name required"},
		}, nil
	}
	var cfgBytes []byte
	if req.Body.Config != nil && len(*req.Body.Config) > 0 {
		cfgBytes, _ = json.Marshal(*req.Body.Config)
	}
	tools := make([]connections.ConnTool, 0, len(req.Body.Tools))
	for _, t := range req.Body.Tools {
		tf := ""
		if t.TargetField != nil {
			tf = strings.TrimSpace(*t.TargetField)
		}
		tools = append(tools, connections.ConnTool{
			Name:        strings.TrimSpace(t.Name),
			Write:       t.Write,
			TargetField: tf,
		})
	}
	if err := s.deps.Connections.Upsert(ctx, connections.UpsertInput{
		Name:    name,
		Kind:    req.Body.Kind,
		Config:  cfgBytes,
		Tools:   tools,
		Enabled: req.Body.Enabled,
	}); err != nil {
		return gen.UpsertConnection400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()},
		}, nil
	}
	_ = s.appendAudit(ctx, "connection.upsert", name, map[string]any{"kind": req.Body.Kind, "enabled": req.Body.Enabled})
	saved, _, err := s.deps.Connections.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	return gen.UpsertConnection200JSONResponse(toGenConnection(saved)), nil
}

func (s *Server) DeleteConnection(ctx context.Context, req gen.DeleteConnectionRequestObject) (gen.DeleteConnectionResponseObject, error) {
	if s.deps.Connections != nil {
		if err := s.deps.Connections.Delete(ctx, req.Name); err != nil {
			return nil, err
		}
		_ = s.appendAudit(ctx, "connection.delete", req.Name, map[string]string{"name": req.Name})
	}
	return gen.DeleteConnection204Response{}, nil
}

func toGenConnection(c connections.Connection) gen.ConnectionItem {
	tools := make([]gen.ConnToolItem, 0, len(c.Tools))
	for _, t := range c.Tools {
		var tf *string
		if t.TargetField != "" {
			v := t.TargetField
			tf = &v
		}
		tools = append(tools, gen.ConnToolItem{
			Name:        t.Name,
			Write:       t.Write,
			TargetField: tf,
		})
	}
	return gen.ConnectionItem{
		Name:       c.Name,
		Kind:       c.Kind,
		Configured: c.Configured,
		Enabled:    c.Enabled,
		Tools:      tools,
	}
}

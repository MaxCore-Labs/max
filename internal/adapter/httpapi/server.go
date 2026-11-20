package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/approvals"
	"max/internal/app/chat"
	"max/internal/app/limits"
	"max/internal/domain"
	"max/internal/platform"
)

// Deps bundles dependencies injected into the HTTP API server.
type Deps struct {
	Store     app.Store
	Actors    *actor.Service
	Approvals *approvals.Service
	Chat      *chat.Service
	Slots     *limits.Slots
	Quota     *limits.Quota
	Engines   map[domain.EngineName]app.Engine
	CancelRun func(runID domain.ID)
	Bus       *platform.Bus
	Hub       *Hub
	Config    platform.Config
	Clock     app.Clock
	IDs       app.IDGen
}

// Server implements gen.StrictServerInterface and owns HTTP/SSE routing.
type Server struct {
	deps         Deps
	secureCookie bool
}

var _ gen.StrictServerInterface = (*Server)(nil)

// New builds the hardened HTTP handler.
func New(deps Deps) (http.Handler, error) {
	if deps.Clock == nil {
		deps.Clock = app.SystemClock{}
	}
	if deps.IDs == nil {
		deps.IDs = app.UUIDv7Gen{}
	}
	if deps.Bus == nil {
		deps.Bus = platform.NewBus()
	}
	if deps.Hub == nil {
		deps.Hub = NewHub(deps.Bus, deps.Clock)
	}

	if deps.Config.AdminPassword != "" {
		ctx := context.Background()
		_, hasOwner, err := deps.Store.Read().GetOwnerUser(ctx)
		if err != nil {
			return nil, fmt.Errorf("check owner user: %w", err)
		}
		if !hasOwner {
			if err := SetAdminPassword(ctx, deps.Store, deps.Clock, deps.IDs, deps.Config.AdminPassword); err != nil {
				return nil, fmt.Errorf("bootstrap MAX_ADMIN_PASSWORD: %w", err)
			}
		}
	}

	pubOrigin, secure, allowedHosts := parsePublicOriginAndHosts(deps.Config.Listen, deps.Config.PublicURL)
	srv := &Server{
		deps:         deps,
		secureCookie: secure,
	}

	strict := gen.NewStrictHandler(srv, []gen.StrictMiddlewareFunc{
		cookieStrictMiddleware(),
	})

	apiMux := http.NewServeMux()
	gen.HandlerFromMux(strict, apiMux)

	rootMux := http.NewServeMux()
	rootMux.HandleFunc("GET /api/v1/events", srv.handleStreamEvents)
	rootMux.HandleFunc("GET /api/v1/runs/{runId}/stream", srv.handleStreamRun)
	rootMux.Handle("/", apiMux)

	var h http.Handler = rootMux
	h = RequireSession(deps.Store, deps.Clock)(h)
	h = RateLimitLogin(deps.Clock)(h)
	h = CheckOrigin(pubOrigin)(h)
	h = CheckHost(allowedHosts)(h)
	h = SecurityHeaders(h)
	return h, nil
}

func (s *Server) appendAudit(ctx context.Context, action, ref string, detail any) error {
	actorName := "owner"
	if u, ok := currentUser(ctx); ok && u.Username != "" {
		actorName = u.Username
	}
	rawBytes, _ := json.Marshal(detail)
	redacted := platform.Redact(string(rawBytes))
	nowMs := s.deps.Clock.Now().UnixMilli()
	return s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.AppendAuditLog(ctx, app.AuditLogRecord{
			TS:         nowMs,
			Actor:      actorName,
			Action:     action,
			Ref:        ref,
			DetailJSON: redacted,
		})
	})
}

func parsePageCursor(ptr *string) (*int64, string) {
	if ptr == nil {
		return nil, ""
	}
	raw := strings.TrimSpace(*ptr)
	if raw == "" {
		return nil, ""
	}
	tsStr, id, ok := strings.Cut(raw, "_")
	if !ok {
		return nil, ""
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return nil, ""
	}
	return &ts, id
}

func makePageCursor(ts int64, id string) *string {
	c := strconv.FormatInt(ts, 10) + "_" + id
	return &c
}

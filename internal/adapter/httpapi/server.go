package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/chat"
	"max/internal/platform"
)

// Deps bundles dependencies injected into the HTTP API server.
type Deps struct {
	Store  app.Store
	Chat   *chat.Service
	Config platform.Config
	Clock  app.Clock
	IDs    app.IDGen
}

// Server implements gen.StrictServerInterface and owns HTTP/SSE routing.
type Server struct {
	deps         Deps
	secureCookie bool
}

var _ gen.StrictServerInterface = (*Server)(nil)

// New builds the hardened HTTP handler (SecurityHeaders -> CheckHost -> CheckOrigin -> RateLimitLogin -> RequireSession -> OpenAPI routes).
func New(deps Deps) (http.Handler, error) {
	if deps.Clock == nil {
		deps.Clock = app.SystemClock{}
	}
	if deps.IDs == nil {
		deps.IDs = app.UUIDv7Gen{}
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

	var h http.Handler = apiMux
	h = RequireSession(deps.Store, deps.Clock)(h)
	h = RateLimitLogin(deps.Clock)(h)
	h = CheckOrigin(pubOrigin)(h)
	h = CheckHost(allowedHosts)(h)
	h = SecurityHeaders(h)
	return h, nil
}

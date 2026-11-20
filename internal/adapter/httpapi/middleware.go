package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
)

const cspHeaderValue = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'"

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(gen.ErrorResponse{Error: msg})
}

// SecurityHeaders attaches strict CSP (no unsafe-inline), nosniff, and clickjacking headers to all responses.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", cspHeaderValue)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// CheckHost rejects HTTP requests whose Host header is not in allowedHosts (400 Bad Request).
func CheckHost(allowedHosts map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := strings.ToLower(strings.TrimSpace(r.Host))
			hostOnly := host
			if h, _, err := net.SplitHostPort(host); err == nil {
				hostOnly = h
			}
			if !allowedHosts[host] && !allowedHosts[hostOnly] {
				writeJSONError(w, http.StatusBadRequest, "disallowed Host header")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CheckOrigin enforces exact Origin matching against publicOrigin on state-changing requests (403 Forbidden).
func CheckOrigin(publicOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" && !strings.EqualFold(origin, publicOrigin) {
				writeJSONError(w, http.StatusForbidden, "cross-origin write request forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitLogin caps POST /api/v1/auth/login attempts at 5 per minute per client IP (429 Too Many Requests).
func RateLimitLogin(clock app.Clock) func(http.Handler) http.Handler {
	type bucket struct {
		hits []time.Time
	}
	var (
		mu      sync.Mutex
		clients = make(map[string]*bucket)
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/login" {
				next.ServeHTTP(w, r)
				return
			}
			ip := clientIP(r.RemoteAddr)
			now := clock.Now()
			cutoff := now.Add(-time.Minute)

			mu.Lock()
			b := clients[ip]
			if b == nil {
				b = &bucket{}
				clients[ip] = b
			}
			kept := b.hits[:0]
			for _, ts := range b.hits {
				if ts.After(cutoff) {
					kept = append(kept, ts)
				}
			}
			b.hits = kept
			if len(b.hits) >= 5 {
				mu.Unlock()
				writeJSONError(w, http.StatusTooManyRequests, "too many login attempts; retry in 1 minute")
				return
			}
			b.hits = append(b.hits, now)
			mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
}

// RequireSession enforces valid cookie session authentication on all /api/ routes except healthz and login.
func RequireSession(store app.Store, clock app.Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}
			if (r.URL.Path == "/api/v1/healthz" && r.Method == http.MethodGet) ||
				(r.URL.Path == "/api/v1/auth/login" && r.Method == http.MethodPost) {
				next.ServeHTTP(w, r)
				return
			}

			c, err := r.Cookie(sessionCookieName)
			if err != nil || strings.TrimSpace(c.Value) == "" {
				writeJSONError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			tokHash := hashSessionToken(c.Value)
			sess, ok, err := store.Read().GetAuthSession(r.Context(), tokHash)
			if err != nil || !ok || sess.ExpiresAt <= clock.Now().UnixMilli() {
				writeJSONError(w, http.StatusUnauthorized, "session expired or invalid")
				return
			}
			user, uok, err := store.Read().GetUserByID(r.Context(), sess.UserID)
			if err != nil || !uok {
				writeJSONError(w, http.StatusUnauthorized, "session owner not found")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyUser, user)
			ctx = context.WithValue(ctx, ctxKeySessionHash, tokHash)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func cookieStrictMiddleware() gen.StrictMiddlewareFunc {
	return func(f gen.StrictHandlerFunc, _ string) gen.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			pc := &pendingCookies{}
			ctx = context.WithValue(ctx, ctxKeyCookies, pc)
			resp, err := f(ctx, w, r, request)
			for _, ck := range pc.list {
				http.SetCookie(w, ck)
			}
			return resp, err
		}
	}
}

func parsePublicOriginAndHosts(listenAddr, publicURL string) (origin string, secure bool, hosts map[string]bool) {
	hosts = map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
	}
	if listenAddr != "" {
		hosts[strings.ToLower(listenAddr)] = true
		if h, _, err := net.SplitHostPort(listenAddr); err == nil && h != "" && h != "0.0.0.0" {
			hosts[strings.ToLower(h)] = true
		}
	}
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		secure = strings.EqualFold(u.Scheme, "https")
		origin = strings.ToLower(u.Scheme + "://" + u.Host)
		hosts[strings.ToLower(u.Host)] = true
		if h, _, err := net.SplitHostPort(u.Host); err == nil && h != "" {
			hosts[strings.ToLower(h)] = true
		}
	}
	return origin, secure, hosts
}

func clientIP(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil && h != "" {
		return h
	}
	return remoteAddr
}

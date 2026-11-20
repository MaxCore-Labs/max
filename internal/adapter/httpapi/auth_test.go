package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"max/internal/adapter/httpapi"
	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/platform"
	"max/internal/testutil/testdb"
)

const testAdminPassword = "correct-horse-battery-12"

func newTestHandler(t *testing.T, publicURL, adminPass string) (app.Store, http.Handler) {
	t.Helper()
	st := testdb.New(t)
	if publicURL == "" {
		publicURL = "http://127.0.0.1:8787"
	}
	cfg := platform.Config{
		Listen:        "127.0.0.1:8787",
		PublicURL:     publicURL,
		AdminPassword: adminPass,
	}
	h, err := httpapi.New(httpapi.Deps{
		Store:  st,
		Config: cfg,
		Clock:  app.SystemClock{},
		IDs:    app.UUIDv7Gen{},
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	return st, h
}

func doLogin(h http.Handler, host, password string) *httptest.ResponseRecorder {
	body := bytes.NewBufferString(`{"password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.10:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginSetsCookie(t *testing.T) {
	_, hHTTP := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	rec1 := doLogin(hHTTP, "127.0.0.1:8787", testAdminPassword)
	if rec1.Code != http.StatusOK {
		t.Fatalf("HTTP login status = %d, body = %s", rec1.Code, rec1.Body.String())
	}
	cookies1 := rec1.Result().Cookies()
	if len(cookies1) == 0 {
		t.Fatal("expected session cookie on login")
	}
	c1 := cookies1[0]
	if !c1.HttpOnly || c1.SameSite != http.SameSiteStrictMode || c1.Secure {
		t.Fatalf("http cookie flags = HttpOnly:%v SameSite:%v Secure:%v", c1.HttpOnly, c1.SameSite, c1.Secure)
	}

	_, hHTTPS := newTestHandler(t, "https://max.tailnet.ts.net", testAdminPassword)
	rec2 := doLogin(hHTTPS, "max.tailnet.ts.net", testAdminPassword)
	if rec2.Code != http.StatusOK {
		t.Fatalf("HTTPS login status = %d", rec2.Code)
	}
	c2 := rec2.Result().Cookies()[0]
	if !c2.HttpOnly || c2.SameSite != http.SameSiteStrictMode || !c2.Secure {
		t.Fatalf("https cookie flags = HttpOnly:%v SameSite:%v Secure:%v, want Secure=true", c2.HttpOnly, c2.SameSite, c2.Secure)
	}
}

func TestSessionStoredHashed(t *testing.T) {
	st, h := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	rec := doLogin(h, "127.0.0.1:8787", testAdminPassword)
	rawToken := rec.Result().Cookies()[0].Value
	if rawToken == "" {
		t.Fatal("empty cookie value")
	}

	hashes, err := st.Read().ListAllAuthSessionHashes(context.Background())
	if err != nil || len(hashes) != 1 {
		t.Fatalf("hashes = %v (err=%v), want 1", hashes, err)
	}
	if hashes[0] == rawToken {
		t.Fatal("auth_sessions stored raw token instead of SHA-256 hash")
	}
	if len(hashes[0]) != 64 {
		t.Fatalf("expected 64-hex SHA-256 hash, got %q", hashes[0])
	}
}

func TestWrongPassword401(t *testing.T) {
	_, h := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	rec := doLogin(h, "127.0.0.1:8787", "wrong-password-999")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	_, h := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	for i := range 5 {
		rec := doLogin(h, "127.0.0.1:8787", "wrong-password-999")
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d prematurely rate-limited (429)", i+1)
		}
	}
	rec6 := doLogin(h, "127.0.0.1:8787", "wrong-password-999")
	if rec6.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt in 1 min status = %d, want 429", rec6.Code)
	}
}

var pathParamRe = regexp.MustCompile(`\{[^}]+\}`)

func TestProtectedRouteNeedsSession(t *testing.T) {
	_, h := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	swagger, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}

	checked := 0
	for rawPath, pathItem := range swagger.Paths.Map() {
		concretePath := pathParamRe.ReplaceAllString(rawPath, "sample-id")
		for method := range pathItem.Operations() {
			checked++
			req := httptest.NewRequest(method, concretePath+"?topics=inbox", bytes.NewBufferString(`{"password":"`+testAdminPassword+`"}`))
			req.Host = "127.0.0.1:8787"
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			isPublic := (rawPath == "/api/v1/healthz" && method == http.MethodGet) ||
				(rawPath == "/api/v1/auth/login" && method == http.MethodPost)
			if isPublic {
				if rec.Code == http.StatusUnauthorized {
					t.Errorf("public route %s %s unexpectedly returned 401", method, rawPath)
				}
				continue
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("protected route %s %s without cookie returned %d, want 401", method, rawPath, rec.Code)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("expected >= 20 spec operations checked, got %d", checked)
	}
}

func TestOriginCheck(t *testing.T) {
	_, h := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	loginRec := doLogin(h, "127.0.0.1:8787", testAdminPassword)
	cookie := loginRec.Result().Cookies()[0]

	// Cross-origin POST -> 403.
	reqBad := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	reqBad.Host = "127.0.0.1:8787"
	reqBad.Header.Set("Origin", "https://evil.example")
	reqBad.AddCookie(cookie)
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusForbidden {
		t.Fatalf("mismatched Origin status = %d, want 403", recBad.Code)
	}

	// Missing Origin header -> allowed.
	reqNoOrigin := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	reqNoOrigin.Host = "127.0.0.1:8787"
	reqNoOrigin.AddCookie(cookie)
	recOK := httptest.NewRecorder()
	h.ServeHTTP(recOK, reqNoOrigin)
	if recOK.Code == http.StatusForbidden {
		t.Fatal("missing Origin header should be allowed")
	}
}

func TestHostAllowlist(t *testing.T) {
	_, h := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Host: evil.example status = %d, want 400", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, h := newTestHandler(t, "http://127.0.0.1:8787", testAdminPassword)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	req.Host = "127.0.0.1:8787"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP = %q, want default-src 'self' and no unsafe-inline", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestNoPublicSetup(t *testing.T) {
	_, h := newTestHandler(t, "http://127.0.0.1:8787", "")

	// No setup/register route exists.
	for _, p := range []string{"/api/v1/auth/setup", "/api/v1/auth/register", "/api/v1/setup"} {
		req := httptest.NewRequest(http.MethodPost, p, bytes.NewBufferString(`{"password":"longpassword123"}`))
		req.Host = "127.0.0.1:8787"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
			t.Fatalf("unexpected public setup route %s succeeded (%d)", p, rec.Code)
		}
	}

	// Login when no user exists and AdminPassword is empty -> 503 mentioning `max admin set-password`.
	rec := doLogin(h, "127.0.0.1:8787", "longpassword123")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("login without configured admin status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "max admin set-password") {
		t.Fatalf("503 body missing 'max admin set-password' instruction: %s", rec.Body.String())
	}
}

func TestPasswordMinLength(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)

	if err := httpapi.SetAdminPassword(ctx, st, app.SystemClock{}, app.UUIDv7Gen{}, "11charsonly"); err == nil {
		t.Fatal("expected 11-char password to be rejected")
	}
	if err := httpapi.SetAdminPassword(ctx, st, app.SystemClock{}, app.UUIDv7Gen{}, "12charsvalid"); err != nil {
		t.Fatalf("expected 12-char password to succeed, got %v", err)
	}
}

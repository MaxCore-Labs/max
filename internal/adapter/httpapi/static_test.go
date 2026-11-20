package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"max/internal/adapter/httpapi"
	"max/internal/app"
	"max/internal/platform"
	"max/internal/testutil/testdb"
)

func gzipBytes(t *testing.T, raw string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestSPAFallback(t *testing.T) {
	st := testdb.New(t)
	jsBody := `console.log("max-app");`
	mockDist := fstest.MapFS{
		"200.html":                            {Data: []byte("<!doctype html><html><body><div id=\"max-spa\">Max SPA</div></body></html>")},
		"_app/immutable/chunks/app.123.js":    {Data: []byte(jsBody)},
		"_app/immutable/chunks/app.123.js.gz": {Data: gzipBytes(t, jsBody)},
	}

	h, err := httpapi.New(httpapi.Deps{
		Store:    st,
		StaticFS: mockDist,
		Config: platform.Config{
			Listen:        "127.0.0.1:8787",
			PublicURL:     "http://127.0.0.1:8787",
			AdminPassword: testAdminPassword,
		},
		Clock: app.SystemClock{},
		IDs:   app.UUIDv7Gen{},
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}

	// 1. Deep-link SPA navigation GET /activity returns SPA shell HTML (no-cache).
	reqAct := httptest.NewRequest(http.MethodGet, "/activity", nil)
	reqAct.Host = "127.0.0.1:8787"
	recAct := httptest.NewRecorder()
	h.ServeHTTP(recAct, reqAct)
	if recAct.Code != http.StatusOK {
		t.Fatalf("GET /activity status = %d, want 200", recAct.Code)
	}
	if ct := recAct.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /activity Content-Type = %q, want text/html", ct)
	}
	if cc := recAct.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("GET /activity Cache-Control = %q, want no-cache", cc)
	}
	if !strings.Contains(recAct.Body.String(), "max-spa") {
		t.Fatalf("GET /activity body did not contain SPA shell: %s", recAct.Body.String())
	}

	// 2. Authenticated GET /api/v1/nope returns 404 JSON, never HTML.
	loginRec := doLogin(h, "127.0.0.1:8787", testAdminPassword)
	cookie := loginRec.Result().Cookies()[0]

	reqNope := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	reqNope.Host = "127.0.0.1:8787"
	reqNope.AddCookie(cookie)
	recNope := httptest.NewRecorder()
	h.ServeHTTP(recNope, reqNope)
	if recNope.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/nope status = %d, want 404", recNope.Code)
	}
	if ct := recNope.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("GET /api/v1/nope Content-Type = %q, want application/json", ct)
	}
	var errPayload map[string]any
	if err := json.Unmarshal(recNope.Body.Bytes(), &errPayload); err != nil {
		t.Fatalf("GET /api/v1/nope body is not valid JSON: %q", recNope.Body.String())
	}

	// 3. Immutable asset with Accept-Encoding: gzip serves precompressed .gz with immutable Cache-Control.
	reqAsset := httptest.NewRequest(http.MethodGet, "/_app/immutable/chunks/app.123.js", nil)
	reqAsset.Host = "127.0.0.1:8787"
	reqAsset.Header.Set("Accept-Encoding", "gzip")
	recAsset := httptest.NewRecorder()
	h.ServeHTTP(recAsset, reqAsset)
	if recAsset.Code != http.StatusOK {
		t.Fatalf("GET immutable asset status = %d, want 200", recAsset.Code)
	}
	if ce := recAsset.Header().Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", ce)
	}
	if cc := recAsset.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q, want immutable", cc)
	}
	zr, err := gzip.NewReader(bytes.NewReader(recAsset.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, _ := io.ReadAll(zr)
	_ = zr.Close()
	if string(decoded) != jsBody {
		t.Fatalf("decoded asset = %q, want %q", string(decoded), jsBody)
	}

	// 4. Missing asset under /_app/ or with file extension returns 404, never SPA HTML.
	for _, missing := range []string{"/_app/immutable/chunks/missing.js", "/favicon-missing.ico"} {
		reqMiss := httptest.NewRequest(http.MethodGet, missing, nil)
		reqMiss.Host = "127.0.0.1:8787"
		recMiss := httptest.NewRecorder()
		h.ServeHTTP(recMiss, reqMiss)
		if recMiss.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", missing, recMiss.Code)
		}
		if strings.Contains(recMiss.Body.String(), "max-spa") {
			t.Fatalf("GET %s unexpectedly returned SPA HTML", missing)
		}
	}
}

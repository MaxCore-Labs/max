package mcpclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"max/internal/adapter/mcpclient"
)

func TestHTTPClientInvoke(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"dispatched ok"}]}}`))
	}))
	defer srv.Close()

	cfg, _ := json.Marshal(map[string]string{
		"url":    srv.URL,
		"bearer": "secret-123",
	})
	cli := mcpclient.New()
	res, err := cli.Invoke(context.Background(), "http", cfg, "send_email", json.RawMessage(`{"to":"a@b.com"}`))
	if err != nil || res != "dispatched ok" {
		t.Fatalf("Invoke = (%q, %v), want ('dispatched ok', nil)", res, err)
	}
	if gotAuth != "Bearer secret-123" {
		t.Fatalf("gotAuth = %q, want Bearer secret-123", gotAuth)
	}
}

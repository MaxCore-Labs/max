package agentmemory_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/adapter/agentmemory"
	"max/internal/app"
	"max/internal/domain"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func TestSearchSendsAgentID(t *testing.T) {
	var gotAgentID, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			AgentID string `json:"agentId"`
			Query   string `json:"query"`
			Limit   int    `json:"limit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotAgentID = body.AgentID
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"m1","agentId":"ag-42","content":"hit one","score":0.9}]}`))
	}))
	defer srv.Close()

	cli := agentmemory.NewHTTPClient(srv.URL, "sec-tok", &fakeClock{now: time.Now()})
	hits, err := cli.Search(context.Background(), domain.ID("ag-42"), "hello", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAgentID != "ag-42" {
		t.Fatalf("sent agentId = %q, want ag-42", gotAgentID)
	}
	if gotAuth != "Bearer sec-tok" {
		t.Fatalf("Authorization = %q, want Bearer sec-tok", gotAuth)
	}
	if len(hits) != 1 || hits[0].Content != "hit one" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestSearchTimeout2s(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := agentmemory.NewHTTPClient(srv.URL, "", &fakeClock{now: time.Now()})
	start := time.Now()
	_, err := cli.Search(context.Background(), "ag-1", "slow query", 5)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error from slow server")
	}
	if elapsed < 1800*time.Millisecond || elapsed > 3200*time.Millisecond {
		t.Fatalf("elapsed = %v, want ~2s timeout", elapsed)
	}
}

func TestBreakerOpensAfter5(t *testing.T) {
	var networkHits atomic.Int32
	var failMode atomic.Bool
	failMode.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		networkHits.Add(1)
		if failMode.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	clk := &fakeClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	cli := agentmemory.NewHTTPClient(srv.URL, "", clk)
	var _ app.EpisodicMemory = cli

	for i := 1; i <= 5; i++ {
		if !cli.Healthy() {
			t.Fatalf("Healthy() became false early before attempt %d", i)
		}
		if _, err := cli.Search(context.Background(), "ag-1", "q", 5); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", i)
		}
	}

	if cli.Healthy() {
		t.Fatal("expected Healthy()=false after 5 consecutive failures")
	}
	beforeOpenHits := networkHits.Load()

	// Calls during 60s open window must fail fast without hitting network.
	clk.now = clk.now.Add(30 * time.Second)
	if _, err := cli.Search(context.Background(), "ag-1", "q", 5); err == nil {
		t.Fatal("expected fast breaker error while open")
	}
	if networkHits.Load() != beforeOpenHits {
		t.Fatalf("network was called while circuit breaker open (%d != %d)", networkHits.Load(), beforeOpenHits)
	}

	// After 60s, breaker enters half-open and recovers on success.
	clk.now = clk.now.Add(31 * time.Second)
	failMode.Store(false)
	if !cli.Healthy() {
		t.Fatal("expected Healthy()=true after 60s cool-off")
	}
	if _, err := cli.Search(context.Background(), "ag-1", "q", 5); err != nil {
		t.Fatalf("Search after recovery failed: %v", err)
	}
}

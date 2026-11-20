package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"max/internal/app"
)

const defaultTakeoverIdleTimeout = 15 * time.Minute

// TakeoverConfig configures the loopback-only human browser takeover endpoint (Spec 8.10 & P4.02).
type TakeoverConfig struct {
	Clock        app.Clock
	IdleTimeout  time.Duration
	UpstreamAddr string // optional upstream noVNC HTTP endpoint, e.g. http://127.0.0.1:6080
}

// Takeover manages a temporary loopback-only noVNC proxy/listener that auto-stops after 15 minutes idle.
type Takeover struct {
	clock       app.Clock
	idleTimeout time.Duration
	upstream    string

	mu          sync.Mutex
	running     bool
	ln          net.Listener
	srv         *http.Server
	url         string
	lastActive  time.Time
	activeConns int
}

// NewTakeover constructs a Takeover controller.
func NewTakeover(cfg TakeoverConfig) *Takeover {
	clk := cfg.Clock
	if clk == nil {
		clk = app.SystemClock{}
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = defaultTakeoverIdleTimeout
	}
	return &Takeover{
		clock:       clk,
		idleTimeout: idle,
		upstream:    strings.TrimSpace(cfg.UpstreamAddr),
	}
}

// Start binds strictly to 127.0.0.1:0 and returns the loopback takeover URL.
func (t *Takeover) Start(_ context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running && t.url != "" {
		t.lastActive = t.clock.Now()
		return t.url, nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("bind loopback takeover listener: %w", err)
	}

	handler := t.buildHandler()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			t.mu.Lock()
			defer t.mu.Unlock()
			switch state {
			case http.StateNew:
				t.activeConns++
				t.lastActive = t.clock.Now()
			case http.StateActive:
				t.lastActive = t.clock.Now()
			case http.StateClosed, http.StateHijacked:
				if t.activeConns > 0 {
					t.activeConns--
				}
				t.lastActive = t.clock.Now()
			}
		},
	}

	t.ln = ln
	t.srv = srv
	t.running = true
	t.lastActive = t.clock.Now()
	t.url = fmt.Sprintf("http://%s/vnc.html", ln.Addr().String())

	go func() {
		_ = srv.Serve(ln)
	}()

	return t.url, nil
}

// Stop closes the loopback takeover listener immediately.
func (t *Takeover) Stop(ctx context.Context) error {
	t.mu.Lock()
	srv := t.srv
	ln := t.ln
	t.running = false
	t.srv = nil
	t.ln = nil
	t.url = ""
	t.activeConns = 0
	t.mu.Unlock()

	if srv != nil {
		shutCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}
	if ln != nil {
		_ = ln.Close()
	}
	return nil
}

// Running reports whether the loopback takeover listener is currently active.
func (t *Takeover) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// URL returns the active loopback takeover URL or empty string when stopped.
func (t *Takeover) URL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return ""
	}
	return t.url
}

// CheckIdle stops the takeover session if no client is connected and IdleTimeout (default 15m) has elapsed.
func (t *Takeover) CheckIdle(ctx context.Context) {
	t.mu.Lock()
	shouldStop := t.running && t.activeConns == 0 && t.clock.Now().Sub(t.lastActive) >= t.idleTimeout
	t.mu.Unlock()
	if shouldStop {
		_ = t.Stop(ctx)
	}
}

func (t *Takeover) buildHandler() http.Handler {
	if t.upstream != "" {
		raw := t.upstream
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			proxy := httputil.NewSingleHostReverseProxy(u)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.touchActive()
				proxy.ServeHTTP(w, r)
			})
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.touchActive()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><title>Max Browser Takeover</title><p>Loopback noVNC session active.</p>"))
	})
}

func (t *Takeover) touchActive() {
	t.mu.Lock()
	t.lastActive = t.clock.Now()
	t.mu.Unlock()
}

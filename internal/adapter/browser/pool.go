package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/domain"
)

const (
	defaultMaxLeaseDur = 10 * time.Minute
	defaultAcquireWait = 20 * time.Second
)

// ErrBusy is returned when no browser seat frees up within the Acquire wait window.
var ErrBusy = errors.New("trình duyệt bận, hãy làm việc khác hoặc kết thúc lượt")

// ErrLeaseExpired is returned when a run's browser lease exceeded 10 minutes or was released.
var ErrLeaseExpired = errors.New("chỗ trình duyệt đã hết hạn 10 phút hoặc bị thu hồi")

var safeSeedRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Config configures the seat-limited stealth Browser Pool.
type Config struct {
	Seats         int
	CDPHostURL    string
	Seed          string // default "anon"
	BrowserTZ     string
	BrowserLocale string
	OwnerTZ       string // ignored intentionally; browser uses BrowserTZ
	ProxyURL      string
	MaxLease      time.Duration
	Clock         app.Clock
	ClockWait     func() <-chan struct{}
}

type lease struct {
	runID      domain.ID
	seed       string
	endpoint   string
	outputDir  string
	acquiredAt time.Time
	expiresAt  time.Time
}

type waiter struct {
	runID   domain.ID
	prio    domain.Priority
	readyCh chan struct{}
}

// Pool implements app.Browser with priority queueing, 10-minute max lease, temp output isolation, and graceful close.
type Pool struct {
	cfg        Config
	httpClient *http.Client

	mu        sync.Mutex
	leases    map[domain.ID]*lease
	dirByEP   map[string]string
	waitQueue []*waiter
}

var _ app.Browser = (*Pool)(nil)

// NewPool constructs a Browser Pool.
func NewPool(cfg Config) *Pool {
	if cfg.Seats <= 0 {
		cfg.Seats = 1
	}
	if cfg.Seed == "" || !safeSeedRe.MatchString(cfg.Seed) {
		cfg.Seed = "anon"
	}
	if cfg.BrowserLocale == "" {
		cfg.BrowserLocale = "en-US"
	}
	if cfg.MaxLease <= 0 {
		cfg.MaxLease = defaultMaxLeaseDur
	}
	if cfg.Clock == nil {
		cfg.Clock = app.SystemClock{}
	}
	return &Pool{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		leases:     make(map[domain.ID]*lease),
		dirByEP:    make(map[string]string),
	}
}

// ActiveSeats returns the number of currently held browser seats.
func (p *Pool) ActiveSeats() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.leases)
}

// Acquire grants a browser CDP endpoint to runID, waiting up to wait ordered by priority (PrioInteractive first).
func (p *Pool) Acquire(ctx context.Context, runID domain.ID, prio domain.Priority, wait time.Duration) (string, error) {
	if wait <= 0 {
		wait = defaultAcquireWait
	}
	p.ReapExpired(ctx)

	p.mu.Lock()
	if cur, ok := p.leases[runID]; ok {
		ep := cur.endpoint
		p.mu.Unlock()
		return ep, nil
	}
	if len(p.leases) < p.cfg.Seats && len(p.waitQueue) == 0 {
		l, err := p.grantLocked(ctx, runID)
		p.mu.Unlock()
		if err != nil {
			return "", err
		}
		return l.endpoint, nil
	}

	w := &waiter{runID: runID, prio: prio, readyCh: make(chan struct{})}
	p.enqueueWaiterLocked(w)
	deadline := p.cfg.Clock.Now().Add(wait)
	p.mu.Unlock()

	if err := p.waitUntilReady(ctx, w, deadline, wait); err != nil {
		p.removeWaiter(w)
		return "", err
	}

	p.mu.Lock()
	l, err := p.grantLocked(ctx, runID)
	p.mu.Unlock()
	if err != nil {
		p.notifyNextWaiter()
		return "", err
	}
	return l.endpoint, nil
}

func (p *Pool) waitUntilReady(ctx context.Context, w *waiter, deadline time.Time, wait time.Duration) error {
	if p.cfg.ClockWait != nil {
		for {
			if !p.cfg.Clock.Now().Before(deadline) {
				return ErrBusy
			}
			tick := p.cfg.ClockWait()
			select {
			case <-w.readyCh:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-tick:
			}
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-w.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrBusy
	}
}

// ValidateLease verifies runID still holds an unexpired browser seat.
func (p *Pool) ValidateLease(runID domain.ID) error {
	now := p.cfg.Clock.Now()
	p.mu.Lock()
	l, ok := p.leases[runID]
	if !ok {
		p.mu.Unlock()
		return ErrLeaseExpired
	}
	expired := !now.Before(l.expiresAt)
	p.mu.Unlock()
	if expired {
		_ = p.Release(context.Background(), runID)
		return ErrLeaseExpired
	}
	return nil
}

// ReapExpired revokes all leases past their 10-minute TTL.
func (p *Pool) ReapExpired(ctx context.Context) {
	now := p.cfg.Clock.Now()
	p.mu.Lock()
	var expiredIDs []domain.ID
	for id, l := range p.leases {
		if !now.Before(l.expiresAt) {
			expiredIDs = append(expiredIDs, id)
		}
	}
	p.mu.Unlock()
	for _, id := range expiredIDs {
		_ = p.Release(ctx, id)
	}
}

// Release frees runID's seat, sends POST /fingerprint/{seed}/close, deletes isolated temp output dir, and wakes next waiter.
func (p *Pool) Release(ctx context.Context, runID domain.ID) error {
	p.mu.Lock()
	l, ok := p.leases[runID]
	if !ok {
		p.mu.Unlock()
		return nil
	}
	delete(p.leases, runID)
	delete(p.dirByEP, l.endpoint)
	p.mu.Unlock()

	if l.outputDir != "" {
		_ = os.RemoveAll(l.outputDir)
	}
	p.closeFingerprintSession(ctx, l.seed)
	p.notifyNextWaiter()
	return nil
}

// MCPSpec builds a pinned @playwright/mcp@0.0.85 server spec using server-location TZ/locale (never MAX_TZ).
func (p *Pool) MCPSpec(endpoint string) app.MCPServerSpec {
	p.mu.Lock()
	outDir := p.dirByEP[endpoint]
	p.mu.Unlock()
	if outDir == "" {
		outDir, _ = os.MkdirTemp("", "max-pw-mcp-*")
	}
	args := []string{
		"x", "@playwright/mcp@0.0.85",
		"--caps=core",
		"--cdp-endpoint", endpoint,
		"--output-dir", outDir,
	}
	env := []string{
		"LANG=" + p.cfg.BrowserLocale,
		"LC_ALL=" + p.cfg.BrowserLocale,
		"MAX_BROWSER_LOCALE=" + p.cfg.BrowserLocale,
	}
	if strings.TrimSpace(p.cfg.BrowserTZ) != "" {
		env = append(env, "TZ="+strings.TrimSpace(p.cfg.BrowserTZ), "MAX_BROWSER_TZ="+strings.TrimSpace(p.cfg.BrowserTZ))
	}
	if strings.TrimSpace(p.cfg.ProxyURL) != "" {
		env = append(env, "HTTP_PROXY="+p.cfg.ProxyURL, "HTTPS_PROXY="+p.cfg.ProxyURL)
	}
	return app.MCPServerSpec{
		Name:    "browser",
		Command: "bun",
		Args:    args,
		Env:     env,
	}
}

func (p *Pool) grantLocked(ctx context.Context, runID domain.ID) (*lease, error) {
	now := p.cfg.Clock.Now()
	ep := p.resolveCDPEndpoint(ctx, p.cfg.Seed)
	outDir, err := os.MkdirTemp("", "max-browser-out-*")
	if err != nil {
		return nil, err
	}
	l := &lease{
		runID:      runID,
		seed:       p.cfg.Seed,
		endpoint:   ep,
		outputDir:  outDir,
		acquiredAt: now,
		expiresAt:  now.Add(p.cfg.MaxLease),
	}
	p.leases[runID] = l
	p.dirByEP[ep] = outDir
	return l, nil
}

func (p *Pool) resolveCDPEndpoint(ctx context.Context, seed string) string {
	base := strings.TrimRight(p.cfg.CDPHostURL, "/")
	if base == "" {
		return fmt.Sprintf("ws://127.0.0.1:9222/fingerprint/%s/devtools/browser/default", seed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version?fingerprint="+seed, nil)
	if err == nil {
		if resp, rErr := p.httpClient.Do(req); rErr == nil {
			defer func() { _ = resp.Body.Close() }()
			var v struct {
				WS string `json:"webSocketDebuggerUrl"`
			}
			if json.NewDecoder(resp.Body).Decode(&v) == nil && v.WS != "" {
				return v.WS
			}
		}
	}
	wsBase := strings.Replace(base, "http://", "ws://", 1)
	wsBase = strings.Replace(wsBase, "https://", "wss://", 1)
	return fmt.Sprintf("%s/fingerprint/%s/devtools/browser/%s", wsBase, seed, seed)
}

func (p *Pool) closeFingerprintSession(ctx context.Context, seed string) {
	base := strings.TrimRight(p.cfg.CDPHostURL, "/")
	if base == "" || !strings.HasPrefix(base, "http") {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, fmt.Sprintf("%s/fingerprint/%s/close", base, seed), nil)
	if err != nil {
		return
	}
	if resp, rErr := p.httpClient.Do(req); rErr == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func (p *Pool) enqueueWaiterLocked(w *waiter) {
	idx := len(p.waitQueue)
	for i, cur := range p.waitQueue {
		if w.prio > cur.prio {
			idx = i
			break
		}
	}
	p.waitQueue = append(p.waitQueue, nil)
	copy(p.waitQueue[idx+1:], p.waitQueue[idx:])
	p.waitQueue[idx] = w
}

func (p *Pool) removeWaiter(target *waiter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, w := range p.waitQueue {
		if w == target {
			p.waitQueue = append(p.waitQueue[:i], p.waitQueue[i+1:]...)
			return
		}
	}
}

func (p *Pool) notifyNextWaiter() {
	p.mu.Lock()
	if len(p.leases) >= p.cfg.Seats || len(p.waitQueue) == 0 {
		p.mu.Unlock()
		return
	}
	next := p.waitQueue[0]
	p.waitQueue = p.waitQueue[1:]
	p.mu.Unlock()
	close(next.readyCh)
}

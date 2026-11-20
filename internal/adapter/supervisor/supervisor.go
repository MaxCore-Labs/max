package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"max/internal/platform"
)

const (
	defaultBaseBackoff    = 1 * time.Second
	defaultMaxBackoff     = 60 * time.Second
	defaultHealthInterval = 5 * time.Second
	defaultKillGrace      = 2 * time.Second
)

// Child describes a long-running helper process supervised by Max.
type Child struct {
	Name, Bin string
	Args, Env []string
	HealthURL string
}

// ChildStatus reports runtime health and crash statistics for a supervised child.
type ChildStatus struct {
	Running  bool
	Restarts int
	LastExit string
	Healthy  bool
}

// Options customizes backoff, health probing, clock, and event notifications.
type Options struct {
	Bus            *platform.Bus
	Now            func() time.Time
	Sleep          func(ctx context.Context, d time.Duration) error
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	HealthInterval time.Duration
	KillGrace      time.Duration
}

type childEntry struct {
	child        Child
	status       ChildStatus
	crashesInWin []time.Time
	noticeWindow time.Time
}

// Supervisor manages long-lived child process groups with exponential restart backoff and health polling.
type Supervisor struct {
	opts       Options
	httpClient *http.Client

	mu       sync.RWMutex
	children map[string]*childEntry
}

// New constructs a Supervisor with sensible defaults.
func New(opts Options) *Supervisor {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = defaultSleep
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = defaultBaseBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = defaultMaxBackoff
	}
	if opts.HealthInterval <= 0 {
		opts.HealthInterval = defaultHealthInterval
	}
	if opts.KillGrace <= 0 {
		opts.KillGrace = defaultKillGrace
	}
	return &Supervisor{
		opts:       opts,
		httpClient: &http.Client{Timeout: 2 * time.Second},
		children:   make(map[string]*childEntry),
	}
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// BackoffDelay computes base * 2^(attempt-1) capped at maxDelay (default 60s).
func BackoffDelay(attempt int, base, maxDelay time.Duration) time.Duration {
	if base <= 0 {
		base = defaultBaseBackoff
	}
	if maxDelay <= 0 {
		maxDelay = defaultMaxBackoff
	}
	if attempt <= 1 {
		return min(base, maxDelay)
	}
	d := base
	for i := 1; i < attempt; i++ {
		if d >= maxDelay/2 {
			return maxDelay
		}
		d *= 2
	}
	return min(d, maxDelay)
}

// Start registers c and supervises it in background goroutines until ctx is cancelled.
func (s *Supervisor) Start(ctx context.Context, c Child) error {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return errors.New("supervisor: child name is required")
	}
	if strings.TrimSpace(c.Bin) == "" {
		return fmt.Errorf("supervisor: child %q binary is required", name)
	}

	s.mu.Lock()
	if _, exists := s.children[name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("supervisor: child %q already started", name)
	}
	entry := &childEntry{child: c}
	s.children[name] = entry
	s.mu.Unlock()

	go s.runLoop(ctx, entry)
	if strings.TrimSpace(c.HealthURL) != "" {
		go s.healthLoop(ctx, entry)
	}
	return nil
}

// Status returns a point-in-time snapshot of all supervised child processes.
func (s *Supervisor) Status() map[string]ChildStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]ChildStatus, len(s.children))
	for name, e := range s.children {
		out[name] = e.status
	}
	return out
}

func (s *Supervisor) runLoop(ctx context.Context, entry *childEntry) {
	consecutive := 0
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.Command(entry.child.Bin, entry.child.Args...)
		if entry.child.Env != nil {
			cmd.Env = append([]string(nil), entry.child.Env...)
		}
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		configureGroup(cmd)

		startedAt := s.opts.Now()
		if err := cmd.Start(); err != nil {
			s.mu.Lock()
			entry.status.Running = false
			entry.status.Healthy = false
			entry.status.LastExit = err.Error()
			s.mu.Unlock()
			consecutive++
			if s.recordCrashAndCheckNotice(entry, s.opts.Now()) && s.opts.Bus != nil {
				s.publishNotice(entry.child.Name, err.Error())
			}
			if s.opts.Sleep(ctx, BackoffDelay(consecutive, s.opts.BaseBackoff, s.opts.MaxBackoff)) != nil {
				return
			}
			continue
		}

		pgid, _ := registerGroup(cmd)
		s.mu.Lock()
		entry.status.Running = true
		if strings.TrimSpace(entry.child.HealthURL) == "" {
			entry.status.Healthy = true
		}
		s.mu.Unlock()

		stopWatch := make(chan struct{})
		go func(groupID int) {
			select {
			case <-stopWatch:
			case <-ctx.Done():
				_ = killGroup(groupID, s.opts.KillGrace)
			}
		}(pgid)

		waitErr := cmd.Wait()
		close(stopWatch)
		cleanupGroup(pgid)

		if ctx.Err() != nil {
			_ = killGroup(pgid, s.opts.KillGrace)
			s.mu.Lock()
			entry.status.Running = false
			entry.status.Healthy = false
			entry.status.LastExit = "shutdown"
			s.mu.Unlock()
			return
		}

		now := s.opts.Now()
		if now.Sub(startedAt) >= 5*time.Minute {
			consecutive = 0
		}
		consecutive++

		exitMsg := "exit 0"
		if waitErr != nil {
			exitMsg = waitErr.Error()
		}
		s.mu.Lock()
		entry.status.Running = false
		entry.status.Healthy = false
		entry.status.Restarts++
		entry.status.LastExit = exitMsg
		s.mu.Unlock()

		if s.recordCrashAndCheckNotice(entry, now) && s.opts.Bus != nil {
			s.publishNotice(entry.child.Name, exitMsg)
		}

		delay := BackoffDelay(consecutive, s.opts.BaseBackoff, s.opts.MaxBackoff)
		if s.opts.Sleep(ctx, delay) != nil {
			return
		}
	}
}

func (s *Supervisor) recordCrashAndCheckNotice(entry *childEntry, now time.Time) bool {
	cutoff := now.Add(-1 * time.Hour)
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := entry.crashesInWin[:0]
	for _, ts := range entry.crashesInWin {
		if !ts.Before(cutoff) {
			kept = append(kept, ts)
		}
	}
	entry.crashesInWin = append(kept, now)
	if len(entry.crashesInWin) <= 3 {
		return false
	}
	if !entry.noticeWindow.IsZero() && now.Sub(entry.noticeWindow) < time.Hour {
		return false
	}
	entry.noticeWindow = now
	return true
}

func (s *Supervisor) publishNotice(name, lastExit string) {
	s.opts.Bus.Publish(platform.Event{
		Topic: "system",
		Type:  "system.notice",
		Data: map[string]any{
			"child":     name,
			"last_exit": lastExit,
			"message":   fmt.Sprintf("Tiến trình con %s đã thoát hơn 3 lần trong giờ qua", name),
		},
	})
}

func (s *Supervisor) healthLoop(ctx context.Context, entry *childEntry) {
	ticker := time.NewTicker(s.opts.HealthInterval)
	defer ticker.Stop()

	probe := func() {
		s.mu.RLock()
		running := entry.status.Running
		url := entry.child.HealthURL
		s.mu.RUnlock()
		if !running || url == "" {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		resp, err := s.httpClient.Do(req)
		ok := false
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ok = resp.StatusCode >= 200 && resp.StatusCode < 300
		}
		s.mu.Lock()
		if entry.status.Running {
			entry.status.Healthy = ok
		}
		s.mu.Unlock()
	}

	probe()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

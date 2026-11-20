package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"
	"time"
)

// Proc holds OS process and process-group identifiers for a spawned CLI.
type Proc struct {
	PID, PGID int
}

// ProcSpec configures a non-shell child process execution.
type ProcSpec struct {
	Bin                string
	Args, Env          []string
	Dir                string
	IdleTimeout, Grace time.Duration
}

type activityReader struct {
	r        io.Reader
	lastNano *atomic.Int64
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.lastNano.Store(time.Now().UnixNano())
	}
	return n, err
}

// Start spawns s.Bin directly (never via a shell) in its own process group and streams complete stdout lines.
func Start(ctx context.Context, s ProcSpec) (*Proc, io.WriteCloser, <-chan []byte, <-chan error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("start cancelled: %w", err)
	}
	cmd := exec.Command(s.Bin, s.Args...)
	cmd.Dir = s.Dir
	if s.Env == nil {
		cmd.Env = []string{}
	} else {
		cmd.Env = append([]string(nil), s.Env...)
	}
	cmd.WaitDelay = 2 * time.Second
	configureGroup(cmd)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return nil, nil, nil, nil, fmt.Errorf("start %q: %w", s.Bin, err)
	}

	pgid, err := registerGroup(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, nil, nil, nil, fmt.Errorf("register process group: %w", err)
	}

	grace := s.Grace
	if grace <= 0 {
		grace = 2 * time.Second
	}

	var lastReadNano atomic.Int64
	lastReadNano.Store(time.Now().UnixNano())
	var killedReason atomic.Pointer[string]

	doneWatch := make(chan struct{})
	go watchProcess(ctx, pgid, s.IdleTimeout, grace, &lastReadNano, &killedReason, doneWatch)

	linesCh := make(chan []byte, 32)
	errCh := make(chan error, 1)

	go func() {
		defer close(errCh)
		defer cleanupGroup(pgid)

		reader := bufio.NewReaderSize(&activityReader{r: stdoutPipe, lastNano: &lastReadNano}, 64*1024)
		var scanErr error
		for {
			raw, rerr := reader.ReadBytes('\n')
			if len(raw) > 0 && raw[len(raw)-1] == '\n' {
				trimmed := bytes.TrimSuffix(raw, []byte{'\n'})
				trimmed = bytes.TrimSuffix(trimmed, []byte{'\r'})
				linesCh <- trimmed
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					scanErr = rerr
				}
				break
			}
		}
		close(linesCh)

		waitErr := cmd.Wait()
		close(doneWatch)

		if reason := killedReason.Load(); reason != nil {
			errCh <- fmt.Errorf("process killed: %s", *reason)
			return
		}
		if ctx.Err() != nil {
			errCh <- fmt.Errorf("process killed: %w", ctx.Err())
			return
		}
		if waitErr != nil {
			errCh <- fmt.Errorf("process exited: %w", waitErr)
			return
		}
		if scanErr != nil {
			errCh <- fmt.Errorf("read stdout: %w", scanErr)
			return
		}
		errCh <- nil
	}()

	return &Proc{PID: cmd.Process.Pid, PGID: pgid}, stdinPipe, linesCh, errCh, nil
}

func watchProcess(
	ctx context.Context,
	pgid int,
	idleTimeout, grace time.Duration,
	lastReadNano *atomic.Int64,
	killedReason *atomic.Pointer[string],
	done <-chan struct{},
) {
	var ticker *time.Ticker
	var tickCh <-chan time.Time
	if idleTimeout > 0 {
		step := min(idleTimeout/4, 250*time.Millisecond)
		if step < 10*time.Millisecond {
			step = 10 * time.Millisecond
		}
		ticker = time.NewTicker(step)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			msg := ctx.Err().Error()
			killedReason.CompareAndSwap(nil, &msg)
			_ = KillGroup(pgid, grace)
			return
		case <-tickCh:
			elapsed := time.Duration(time.Now().UnixNano() - lastReadNano.Load())
			if elapsed >= idleTimeout {
				msg := fmt.Sprintf("idle timeout (%s) exceeded", idleTimeout)
				killedReason.CompareAndSwap(nil, &msg)
				_ = KillGroup(pgid, grace)
				return
			}
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type mcpEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type mcpServerSpec struct {
	Name    string      `json:"name"`
	Command string      `json:"command"`
	Args    []string    `json:"args"`
	Env     []mcpEnvVar `json:"env"`
}

type sessionNewResp struct {
	SessionID string `json:"sessionId"`
}

type pingLogRecord struct {
	PID         int    `json:"pid"`
	PPID        int    `json:"ppid"`
	MaxRunToken string `json:"maxRunToken"`
}

// peakSampler samples the full descendant process-tree RSS every 100ms during an active turn.
type peakSampler struct {
	peakKB atomic.Int64
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func startPeakSampler(rootPID int) (*peakSampler, error) {
	initialKB, err := measureProcessTreeKB(rootPID)
	if err != nil {
		return nil, err
	}
	ps := &peakSampler{stopCh: make(chan struct{})}
	ps.peakKB.Store(initialKB)
	ps.wg.Add(1)
	go func() {
		defer ps.wg.Done()
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ps.stopCh:
				return
			case <-tk.C:
				if kb, merr := measureProcessTreeKB(rootPID); merr == nil {
					for {
						cur := ps.peakKB.Load()
						if kb <= cur || ps.peakKB.CompareAndSwap(cur, kb) {
							break
						}
					}
				}
			}
		}
	}()
	return ps, nil
}

func (ps *peakSampler) stop() int64 {
	close(ps.stopCh)
	ps.wg.Wait()
	return ps.peakKB.Load()
}

func initACP(ctx context.Context, c *acpClient) (time.Duration, error) {
	t0 := time.Now()
	_, err := c.call(ctx, 20*time.Second, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "max-s0c", "version": "0.2.0"},
	})
	return time.Since(t0), err
}

// runCheckUnauth verifies that inside a clean OS user/container (no host keyring),
// an unauthenticated HOME yields RPC error -32000 ("Authentication required").
func runCheckUnauth(ctx context.Context, agyBin, baseDir string, tw *traceWriter) error {
	fmt.Println("=== [Step 1a] Verifying Unauthenticated Clean User/Container Returns -32000 ===")
	emptyHome := filepath.Join(baseDir, "tmp", "unauth-home")
	_ = os.RemoveAll(emptyHome)
	if err := os.MkdirAll(emptyHome, 0o700); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(emptyHome) }()

	cmd := exec.CommandContext(ctx, agyBin, "mcp", "list")
	cmd.Env = isolatedEnv(emptyHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agy mcp list failed: %w (%s)", err, string(out))
	}
	if !strings.Contains(string(out), "No MCP servers configured") {
		return fmt.Errorf("expected empty MCP list, got: %s", strings.TrimSpace(string(out)))
	}

	c, err := startACP(ctx, agyBin, emptyHome, "s1-unauth", "deny", tw)
	if err != nil {
		return err
	}
	defer c.close()
	if _, err := initACP(ctx, c); err != nil {
		return err
	}
	workDir := filepath.Join(emptyHome, "cwd")
	_ = os.MkdirAll(workDir, 0o700)
	_, err = c.call(ctx, 15*time.Second, "session/new", map[string]any{
		"cwd":        workDir,
		"mcpServers": []mcpServerSpec{},
	})
	if err == nil || !strings.Contains(err.Error(), "-32000") {
		return fmt.Errorf("expected -32000 Authentication required (possible OS keyring leak!), got: %v", err)
	}
	fmt.Printf("  Clean unauthenticated user/container returned expected error: %v\n", err)
	return nil
}

func runCheckHome(ctx context.Context, agyBin, homeDir string, tw *traceWriter) error {
	fmt.Println("=== [Step 1b] Checking Isolated Authenticated HOME & Empty MCP List ===")
	cmd := exec.CommandContext(ctx, agyBin, "mcp", "list")
	cmd.Env = isolatedEnv(homeDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agy mcp list failed: %w (%s)", err, string(out))
	}
	trimmed := strings.TrimSpace(string(out))
	fmt.Printf("  agy mcp list -> %q\n", trimmed)
	if !strings.Contains(trimmed, "No MCP servers configured") {
		return fmt.Errorf("expected empty MCP list, got: %s", trimmed)
	}

	c, err := startACP(ctx, agyBin, homeDir, "s1-authcheck", "deny", tw)
	if err != nil {
		return err
	}
	defer c.close()

	initDur, err := initACP(ctx, c)
	if err != nil {
		return err
	}
	idleKB, err := measureProcessTreeKB(c.cmd.Process.Pid)
	if err != nil {
		return err
	}
	fmt.Printf("  ACP initialize OK in %dms | Idle Tree RSS: %.1f MB (PID %d)\n",
		initDur.Milliseconds(), float64(idleKB)/1024.0, c.cmd.Process.Pid)

	tmpDir := filepath.Join(filepath.Dir(homeDir), "tmp", "authcheck")
	_ = os.MkdirAll(tmpDir, 0o700)
	if _, err := c.call(ctx, 20*time.Second, "session/new", map[string]any{
		"cwd":        tmpDir,
		"mcpServers": []mcpServerSpec{},
	}); err != nil {
		return fmt.Errorf("auth check via session/new failed (need `agy acp --login`): %w", err)
	}
	fmt.Println("  Authenticated isolated HOME verified")
	return nil
}

func runStep2And4(ctx context.Context, agyBin, homeDir, mcpPingBin, baseDir string, tw *traceWriter) error {
	fmt.Println("\n=== [Step 2] ACP session/new + Per-Session mcpServers (probe-1) ===")
	workDir := filepath.Join(baseDir, "tmp", "s2")
	_ = os.RemoveAll(workDir)
	_ = os.MkdirAll(workDir, 0o700)
	pingLog := filepath.Join(workDir, "mcpping.ndjson")

	c, err := startACP(ctx, agyBin, homeDir, "s2-ping", "allow", tw)
	if err != nil {
		return err
	}
	initDur, err := initACP(ctx, c)
	if err != nil {
		c.close()
		return err
	}

	newRes, err := c.call(ctx, 30*time.Second, "session/new", map[string]any{
		"cwd": workDir,
		"mcpServers": []mcpServerSpec{{
			Name:    "mcpping",
			Command: mcpPingBin,
			Args:    []string{},
			Env: []mcpEnvVar{
				{Name: "MAX_RUN_TOKEN", Value: "probe-1"},
				{Name: "MCPPING_LOG", Value: pingLog},
			},
		}},
	})
	if err != nil {
		c.close()
		return fmt.Errorf("session/new: %w", err)
	}
	var sr sessionNewResp
	if err := json.Unmarshal(newRes, &sr); err != nil || sr.SessionID == "" {
		c.close()
		return fmt.Errorf("decode sessionId: %w (%s)", err, string(newRes))
	}
	fmt.Printf("  Created sessionId=%s (init=%dms, agyPID=%d)\n", sr.SessionID, initDur.Milliseconds(), c.cmd.Process.Pid)

	sampler, err := startPeakSampler(c.cmd.Process.Pid)
	if err != nil {
		c.close()
		return err
	}
	promptRes, err := c.call(ctx, 90*time.Second, "session/prompt", map[string]any{
		"sessionId": sr.SessionID,
		"prompt": []map[string]any{{
			"type": "text",
			"text": "Hãy gọi tool ping của MCP mcpping đúng 1 lần rồi trả lời ngắn gọn kết quả.",
		}},
	})
	peakKB := sampler.stop()
	c.close()
	if err != nil {
		return fmt.Errorf("session/prompt s2: %w", err)
	}

	lines, err := readPingLines(pingLog)
	if err != nil || len(lines) == 0 || lines[0].MaxRunToken != "probe-1" {
		return fmt.Errorf("mcpping did not record probe-1 (err=%v, lines=%+v)", err, lines)
	}
	time.Sleep(150 * time.Millisecond)
	orphanAlive := processExists(lines[0].PID)
	fmt.Printf("  mcpping #1: pid=%d ppid=%d token=%s | orphanAfterClose=%v\n",
		lines[0].PID, lines[0].PPID, lines[0].MaxRunToken, orphanAlive)
	if orphanAlive {
		return fmt.Errorf("mcpping child PID %d survived parent close()", lines[0].PID)
	}
	fmt.Printf("  Prompt result raw: %s\n", string(promptRes))
	fmt.Printf("  In-flight Peak Tree RSS: %.1f MB\n", float64(peakKB)/1024.0)

	// Step 4: Spawn fresh agy acp, session/load WITH new token probe-2, clear replay buffer, call ping again!
	fmt.Println("\n=== [Step 4] Fresh agy acp + session/load with New Token (probe-2) ===")
	c2, err := startACP(ctx, agyBin, homeDir, "s4-resume", "allow", tw)
	if err != nil {
		return err
	}
	defer c2.close()
	if _, err := initACP(ctx, c2); err != nil {
		return err
	}

	if _, err := c2.call(ctx, 30*time.Second, "session/load", map[string]any{
		"sessionId": sr.SessionID,
		"cwd":       workDir,
		"mcpServers": []mcpServerSpec{{
			Name:    "mcpping",
			Command: mcpPingBin,
			Args:    []string{},
			Env: []mcpEnvVar{
				{Name: "MAX_RUN_TOKEN", Value: "probe-2"},
				{Name: "MCPPING_LOG", Value: pingLog},
			},
		}},
	}); err != nil {
		return fmt.Errorf("session/load with probe-2: %w", err)
	}
	// Clear replayed history chunks emitted during session/load before sending new prompt
	c2.resetText()

	if _, err := c2.call(ctx, 90*time.Second, "session/prompt", map[string]any{
		"sessionId": sr.SessionID,
		"prompt": []map[string]any{{
			"type": "text",
			"text": "Ở lượt trước mã maxRunToken cũ là gì? Bây giờ hãy gọi lại tool ping của mcpping 1 lần nữa và cho biết mã maxRunToken mới vừa nhận được. Trả lời ngắn trong 1–2 câu.",
		}},
	}); err != nil {
		return fmt.Errorf("session/prompt s4: %w", err)
	}
	linesAfter, err := readPingLines(pingLog)
	if err != nil || len(linesAfter) < 2 || linesAfter[len(linesAfter)-1].MaxRunToken != "probe-2" {
		return fmt.Errorf("expected second mcpping call with probe-2, got lines=%+v (err=%v)", linesAfter, err)
	}
	freshReply := strings.TrimSpace(c2.text())
	fmt.Printf("  mcpping #2: pid=%d ppid=%d token=%s\n",
		linesAfter[len(linesAfter)-1].PID, linesAfter[len(linesAfter)-1].PPID, linesAfter[len(linesAfter)-1].MaxRunToken)
	fmt.Printf("  Fresh non-replayed reply: %q\n", freshReply)
	return nil
}

func readPingLines(path string) ([]pingLogRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []pingLogRecord
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var r pingLogRecord
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

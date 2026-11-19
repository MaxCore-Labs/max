package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type amDaemon struct {
	bunExe  string
	dataDir string
	port    int
	secret  string
	baseURL string
	client  *http.Client
	rootPID int
}

func newAMDaemon(bunExe, dataDir string, port int, secret string) *amDaemon {
	tr := &http.Transport{
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     90 * time.Second,
	}
	return &amDaemon{
		bunExe:  bunExe,
		dataDir: dataDir,
		port:    port,
		secret:  secret,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		client:  &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

func (d *amDaemon) cleanEnv() []string {
	keep := []string{
		"PATH", "SYSTEMDRIVE", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
		"TEMP", "TMP", "USERPROFILE", "HOME", "APPDATA", "LOCALAPPDATA",
		"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "BUN_INSTALL",
	}
	env := make([]string, 0, len(keep)+4)
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"AGENTMEMORY_TOOLS=core",
		"AGENTMEMORY_SECRET="+d.secret,
		"AGENTMEMORY_DATA_DIR="+d.dataDir,
		"III_TELEMETRY_ENABLED=false",
	)
	return env
}

func (d *amDaemon) start(ctx context.Context) (time.Duration, error) {
	t0 := time.Now()
	cmd := exec.CommandContext(ctx, d.bunExe,
		"x", "@agentmemory/agentmemory@0.9.33",
		"--data-dir", d.dataDir,
		"--port", strconv.Itoa(d.port),
		"--tools", "core",
	)
	cmd.Env = d.cleanEnv()
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start agentmemory: %w", err)
	}
	d.rootPID = cmd.Process.Pid
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/agentmemory/livez", nil)
		if err == nil {
			resp, reqErr := d.client.Do(req)
			if reqErr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return time.Since(t0), nil
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0, fmt.Errorf("agentmemory livez timeout on port %d", d.port)
}

func (d *amDaemon) forceKill() {
	killDaemonTree(d.rootPID, d.dataDir)
	d.rootPID = 0
}

func (d *amDaemon) stopCleanOrKill(ctx context.Context) {
	stopCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := []string{"x", "@agentmemory/agentmemory@0.9.33", "stop", "--force", "--data-dir", d.dataDir, "--port", strconv.Itoa(d.port)}
	cmd := exec.CommandContext(stopCtx, d.bunExe, args...)
	cmd.Env = d.cleanEnv()
	_ = cmd.Run()
	d.forceKill()
}

func (d *amDaemon) remember(ctx context.Context, agentID, content string) error {
	payload := map[string]any{
		"content": content,
		"agentId": agentID,
		"type":    "fact",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/agentmemory/remember", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remember HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (d *amDaemon) smartSearch(ctx context.Context, agentID, query string, limit int) (int, string, time.Duration, error) {
	payload := map[string]any{
		"query":   query,
		"agentId": agentID,
		"limit":   limit,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, "", 0, err
	}
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/agentmemory/smart-search", bytes.NewReader(b))
	if err != nil {
		return 0, "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+d.secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	dur := time.Since(t0)
	if err != nil {
		return 0, "", dur, err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", dur, fmt.Errorf("smart-search HTTP %d: %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, "", dur, err
	}
	return len(parsed.Results), string(body), dur, nil
}

func runAgentMemoryBenchmark(ctx context.Context, bunExe string, targetMemories, numAgents, concurrency, maxWriteSeconds int) error {
	fmt.Printf("=== [Steps 1–4] @agentmemory/agentmemory@0.9.33 Load Test (target=%d, agents=%d, workers=%d, maxWriteSec=%d) ===\n",
		targetMemories, numAgents, concurrency, maxWriteSeconds)

	dataDir, err := os.MkdirTemp("", "max-s2-am-*")
	if err != nil {
		return err
	}
	d := newAMDaemon(bunExe, dataDir, 3315, "s2-bench-secret")
	defer func() {
		d.stopCleanOrKill(context.Background())
		time.Sleep(400 * time.Millisecond)
		_ = os.RemoveAll(dataDir)
	}()

	bootDur, err := d.start(ctx)
	if err != nil {
		return err
	}
	pssBefore, rssBefore, procsBefore := d.measureMemory()
	fmt.Printf("  [Step 1] Booted in %.2fs | Daemon Tree (%d procs): PSS=%.1f MiB, RSS=%.1f MiB\n",
		bootDur.Seconds(), procsBefore, pssBefore, rssBefore)

	canary := buildSynthMemory(0, numAgents)
	if err := d.remember(ctx, canary.AgentID, canary.Content); err != nil {
		return fmt.Errorf("seed canary: %w", err)
	}

	writeCtx, cancelWrite := context.WithTimeout(ctx, time.Duration(maxWriteSeconds)*time.Second)
	defer cancelWrite()

	var nextIdx atomic.Int64
	nextIdx.Store(1)
	var completed atomic.Int64
	completed.Store(1)
	var errCount atomic.Int64

	tWriteStart := time.Now()
	lastCheckTime := tWriteStart
	lastCheckCount := int64(1)

	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if writeCtx.Err() != nil {
					return
				}
				idx := int(nextIdx.Add(1) - 1)
				if idx >= targetMemories {
					return
				}
				m := buildSynthMemory(idx, numAgents)
				if err := d.remember(writeCtx, m.AgentID, m.Content); err != nil {
					if writeCtx.Err() == nil {
						errCount.Add(1)
					}
					continue
				}
				done := completed.Add(1)
				if done%1000 == 0 {
					now := time.Now()
					windowSec := now.Sub(lastCheckTime).Seconds()
					windowRate := float64(done-lastCheckCount) / max(windowSec, 0.001)
					lastCheckTime = now
					lastCheckCount = done
					fmt.Printf("  [Step 2 Progress] stored=%d/%d | elapsed=%.1fs | windowRate=%.1f req/s | errors=%d\n",
						done, targetMemories, now.Sub(tWriteStart).Seconds(), windowRate, errCount.Load())
				}
			}
		}()
	}
	wg.Wait()
	writeElapsed := time.Since(tWriteStart)
	actualStored := int(completed.Load())
	fmt.Printf("  [Step 2 Summary] Stored %d/%d memories in %.2fs (avg %.1f req/s, errors=%d)\n",
		actualStored, targetMemories, writeElapsed.Seconds(), float64(actualStored)/writeElapsed.Seconds(), errCount.Load())

	// Step 3: 1,000 smart-searches split into 500 agent-matched topic queries + 500 ground-truth unique token queries
	const numSearches = 1000
	half := numSearches / 2
	rng := rand.New(rand.NewPCG(77, 20261119))
	// Only sample agents guaranteed to have round-0 stored synchronously without gap
	safeSeededAgents := max(1, min(numAgents, actualStored-concurrency*2))

	latencies := make([]float64, 0, numSearches)
	topicHits := 0
	recall5Hits := 0
	searchErrors := 0

	for i := 0; i < numSearches; i++ {
		aIdx := rng.IntN(safeSeededAgents)
		agentID := fmt.Sprintf("agent-%05d", aIdx+1)
		var q, wantTok string
		if i < half {
			q = topicQueryWords[aIdx%len(topicQueryWords)]
			if aIdx == 0 {
				q = canaryToken
			}
		} else {
			wantTok = fmt.Sprintf("utoka%05dr001", aIdx+1)
			q = wantTok
		}
		cnt, rawBody, dur, err := d.smartSearch(ctx, agentID, q, 5)
		latencies = append(latencies, float64(dur.Microseconds())/1000.0)
		if err != nil {
			searchErrors++
			continue
		}
		if i < half {
			if cnt > 0 {
				topicHits++
			}
		} else if cnt > 0 && strings.Contains(rawBody, wantTok) {
			recall5Hits++
		}
	}

	slices.Sort(latencies)
	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[len(latencies)*95/100]
	p99 := latencies[len(latencies)*99/100]
	pssAfter, rssAfter, procsAfter := d.measureMemory()
	fmt.Printf("  [Step 3] 1,000 smart-search (limit=5, N=%d): p50=%.2f ms | p95=%.2f ms | p99=%.2f ms | TopicHalf=%d/%d (%.1f%%) | UniqueRecall@5=%d/%d (%.1f%%) | Errors=%d\n",
		actualStored, p50, p95, p99, topicHits, half, 100*float64(topicHits)/float64(half), recall5Hits, half, 100*float64(recall5Hits)/float64(half), searchErrors)
	fmt.Printf("  [Step 4a] Daemon Tree (%d procs) After Load: PSS=%.1f MiB, RSS=%.1f MiB\n",
		procsAfter, pssAfter, rssAfter)

	// Step 4b: Abrupt SIGKILL (same as FTS5 crash check), restart on same data-dir, and poll canary up to 60s
	d.forceKill()
	time.Sleep(1 * time.Second)
	restartDur, err := d.start(ctx)
	if err != nil {
		return fmt.Errorf("restart agentmemory: %w", err)
	}
	firstCnt, _, firstLat, firstErr := d.smartSearch(ctx, "agent-00001", canaryToken, 5)
	canaryCnt := firstCnt
	canaryLat := firstLat
	waitStart := time.Now()
	for canaryCnt == 0 && time.Since(waitStart) < 60*time.Second {
		time.Sleep(500 * time.Millisecond)
		canaryCnt, _, canaryLat, _ = d.smartSearch(ctx, "agent-00001", canaryToken, 5)
	}
	visibleAfter := time.Since(waitStart)
	pssRestart, rssRestart, procsRestart := d.measureMemory()
	fmt.Printf("  [Step 4b] Restart livez=%.2fs | ImmediateCanary=%d (err=%v) | FinalCanary=%d visibleAfter=%.2fs (query=%.1f ms) | Tree (%d procs) PSS=%.1f MiB, RSS=%.1f MiB\n",
		restartDur.Seconds(), firstCnt, firstErr, canaryCnt, visibleAfter.Seconds(),
		float64(canaryLat.Microseconds())/1000.0, procsRestart, pssRestart, rssRestart)
	return nil
}

func (d *amDaemon) measureMemory() (pssMB, rssMB float64, procs int) {
	return measureDaemonTreeMB(d.rootPID, d.dataDir)
}

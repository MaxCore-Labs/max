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
	"path/filepath"
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
		"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
		"TEMP", "TMP", "USERPROFILE", "HOME", "APPDATA", "LOCALAPPDATA",
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
	// Start asynchronously because CLI launcher may stay attached or spawn detached compose daemon
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start agentmemory: %w", err)
	}
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/agentmemory/livez", nil)
		resp, err := d.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return time.Since(t0), nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0, fmt.Errorf("agentmemory livez timeout on port %d", d.port)
}

func (d *amDaemon) stop(ctx context.Context, gracefulOnly bool) {
	args := []string{"x", "@agentmemory/agentmemory@0.9.33", "stop", "--data-dir", d.dataDir, "--port", strconv.Itoa(d.port)}
	cmd := exec.CommandContext(ctx, d.bunExe, args...)
	cmd.Env = d.cleanEnv()
	_ = cmd.Run()
	if gracefulOnly {
		return
	}

	forceArgs := append(args, "--force")
	cmdForce := exec.CommandContext(ctx, d.bunExe, forceArgs...)
	cmdForce.Env = d.cleanEnv()
	_ = cmdForce.Run()

	enginePort := d.port + 46023
	psKill := fmt.Sprintf(
		"Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -match '%d|%d|%s' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }",
		d.port, enginePort, strings.ReplaceAll(filepath.Base(d.dataDir), "'", ""),
	)
	_ = exec.Command("powershell", "-NoProfile", "-Command", psKill).Run()
}

func (d *amDaemon) measureDaemonRSSMB() (float64, int) {
	enginePort := d.port + 46023
	marker := filepath.Base(d.dataDir)
	ps := fmt.Sprintf(`
$all = Get-CimInstance Win32_Process
$roots = $all | Where-Object { $_.Name -ne 'powershell.exe' -and $_.Name -ne 's2.exe' -and ($_.CommandLine -match '%d|%d|%s') }
$ids = [System.Collections.Generic.HashSet[uint32]]::new()
foreach ($r in $roots) { [void]$ids.Add($r.ProcessId) }
$added = $true
while ($added) {
  $added = $false
  foreach ($p in $all) {
    if ($ids.Contains($p.ParentProcessId) -and -not $ids.Contains($p.ProcessId)) {
      [void]$ids.Add($p.ProcessId)
      $added = $true
    }
  }
}
$matched = $all | Where-Object { $ids.Contains($_.ProcessId) }
$sum = ($matched | Measure-Object -Property WorkingSetSize -Sum).Sum
if (-not $sum) { $sum = 0 }
Write-Output "$($matched.Count),$sum"
`, d.port, enginePort, marker)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 2 {
		return 0, 0
	}
	cnt, _ := strconv.Atoi(parts[0])
	bytesSum, _ := strconv.ParseInt(parts[1], 10, 64)
	return float64(bytesSum) / (1024 * 1024), cnt
}

func (d *amDaemon) remember(ctx context.Context, agentID, content string) error {
	payload := map[string]any{
		"content": content,
		"agentId": agentID,
		"type":    "fact",
	}
	b, _ := json.Marshal(payload)
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

func (d *amDaemon) smartSearch(ctx context.Context, agentID, query string, limit int) (int, time.Duration, error) {
	payload := map[string]any{
		"query":   query,
		"agentId": agentID,
		"limit":   limit,
	}
	b, _ := json.Marshal(payload)
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/agentmemory/smart-search", bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+d.secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	dur := time.Since(t0)
	if err != nil {
		return 0, dur, err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, dur, fmt.Errorf("smart-search HTTP %d: %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Results []json.RawMessage `json:"results"`
	}
	_ = json.Unmarshal(body, &parsed)
	return len(parsed.Results), dur, nil
}

func runAgentMemoryBenchmark(ctx context.Context, bunExe, baseDir string, targetMemories, numAgents, concurrency, maxWriteSeconds int) error {
	fmt.Printf("=== [Steps 1–4] @agentmemory/agentmemory@0.9.33 Load Test (target=%d, agents=%d, workers=%d) ===\n",
		targetMemories, numAgents, concurrency)

	dataDir := filepath.Join(baseDir, "tmp", "am_s2_data")
	_ = os.RemoveAll(dataDir)
	_ = os.MkdirAll(dataDir, 0o700)
	d := newAMDaemon(bunExe, dataDir, 3315, "s2-bench-secret")
	defer func() {
		d.stop(context.Background(), false)
		time.Sleep(500 * time.Millisecond)
		_ = os.RemoveAll(dataDir)
	}()

	bootDur, err := d.start(ctx)
	if err != nil {
		return err
	}
	rssBeforeMB, procCountBefore := d.measureDaemonRSSMB()
	fmt.Printf("  [Step 1] Booted in %.2fs | Daemon Tree (%d procs) RSS Before: %.1f MB\n",
		bootDur.Seconds(), procCountBefore, rssBeforeMB)

	// Seed canary memory on agent-00001 first
	const canaryText = "Ký ức mẫu bất biến của agent-00001: khóa đối chiếu CANARY_ALPHA_9981 cho kiểm thử khởi động lại"
	if err := d.remember(ctx, "agent-00001", canaryText); err != nil {
		return fmt.Errorf("seed canary: %w", err)
	}

	topics := []string{
		"triển khai cổng duyệt quyền ACP cho tiến trình con",
		"tối ưu hóa truy vấn báo cáo doanh thu quý ba",
		"khắc phục lỗi rò rỉ kết nối mạng trên máy chủ biên",
		"đối soát hóa đơn thanh toán nhà cung cấp nội địa",
		"cấu hình lịch chạy định kỳ tổng hợp tin tức sáng",
	}

	writeCtx, cancelWrite := context.WithTimeout(ctx, time.Duration(maxWriteSeconds)*time.Second)
	defer cancelWrite()

	var nextIdx atomic.Int64
	nextIdx.Store(1) // 0 was canary
	var completed atomic.Int64
	completed.Store(1)
	var errCount atomic.Int64

	tWriteStart := time.Now()
	lastCheckTime := tWriteStart
	lastCheckCount := int64(1)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
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
				aIdx := idx % numAgents
				agentID := fmt.Sprintf("agent-%05d", aIdx+1)
				topic := topics[(aIdx+(idx/numAgents))%len(topics)]
				content := fmt.Sprintf("Tóm tắt run #%d của %s: %s mã kiểm chứng tok_%d_%d",
					idx+1, agentID, topic, aIdx+1, (idx/numAgents)+1)

				if err := d.remember(writeCtx, agentID, content); err != nil {
					if writeCtx.Err() == nil {
						errCount.Add(1)
					}
					continue
				}
				done := completed.Add(1)
				if done%2000 == 0 {
					now := time.Now()
					windowSec := now.Sub(lastCheckTime).Seconds()
					windowRate := float64(done-lastCheckCount) / max(windowSec, 0.001)
					lastCheckTime = now
					lastCheckCount = done
					fmt.Printf("  [Step 2 Progress] stored=%d/%d | elapsed=%.1fs | windowRate=%.0f req/s | errors=%d\n",
						done, targetMemories, now.Sub(tWriteStart).Seconds(), windowRate, errCount.Load())
				}
			}
		}()
	}
	wg.Wait()
	writeElapsed := time.Since(tWriteStart)
	actualStored := completed.Load()
	fmt.Printf("  [Step 2 Summary] Stored %d/%d memories in %.2fs (avg %.1f req/s, errors=%d)\n",
		actualStored, targetMemories, writeElapsed.Seconds(), float64(actualStored)/writeElapsed.Seconds(), errCount.Load())

	// Step 3: 1,000 POST /agentmemory/smart-search with agentId & limit: 5
	const numSearches = 1000
	rng := rand.New(rand.NewPCG(77, 20261119))
	queryWords := []string{"triển khai cổng", "doanh thu quý", "rò rỉ kết nối", "đối soát hóa đơn", "lịch chạy định kỳ"}
	maxSeededAgents := min(numAgents, int(actualStored))

	latencies := make([]float64, 0, numSearches)
	nonEmptyHits := 0
	searchErrors := 0
	for i := 0; i < numSearches; i++ {
		aIdx := rng.IntN(maxSeededAgents)
		agentID := fmt.Sprintf("agent-%05d", aIdx+1)
		// Query both common shared topic words (first 500) and agent-unique token (second 500) to measure recall starvation!
		var q string
		if i < 500 {
			q = queryWords[i%len(queryWords)]
		} else {
			q = fmt.Sprintf("tok_%d_1", aIdx+1)
		}
		cnt, dur, err := d.smartSearch(ctx, agentID, q, 5)
		latencies = append(latencies, float64(dur.Microseconds())/1000.0)
		if err != nil {
			searchErrors++
			continue
		}
		if cnt > 0 {
			nonEmptyHits++
		}
	}

	slices.Sort(latencies)
	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[len(latencies)*95/100]
	p99 := latencies[len(latencies)*99/100]
	rssAfterMB, procCountAfter := d.measureDaemonRSSMB()
	fmt.Printf("  [Step 3] 1,000 smart-search (limit=5): p50=%.2f ms | p95=%.2f ms | p99=%.2f ms | NonEmpty=%d/1000 (%.1f%%) | Errors=%d\n",
		p50, p95, p99, nonEmptyHits, float64(nonEmptyHits)/10.0, searchErrors)
	fmt.Printf("  [Step 4a] Daemon Tree (%d procs) RSS After Load: %.1f MB\n", procCountAfter, rssAfterMB)

	// Step 4b: Stop daemon, restart on same data-dir, verify canary memory persists
	d.stop(ctx, false)
	time.Sleep(1 * time.Second)
	restartDur, err := d.start(ctx)
	if err != nil {
		return fmt.Errorf("restart agentmemory: %w", err)
	}
	canaryCnt, canaryLat, err := d.smartSearch(ctx, "agent-00001", "CANARY_ALPHA_9981", 5)
	rssRestartMB, _ := d.measureDaemonRSSMB()
	fmt.Printf("  [Step 4b] Restarted in %.2fs | Canary hits=%d (%.1f ms, err=%v) | RSS After Restart=%.1f MB\n",
		restartDur.Seconds(), canaryCnt, float64(canaryLat.Microseconds())/1000.0, err, rssRestartMB)
	return nil
}

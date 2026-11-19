package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type runMeasurement struct {
	Phase       string       `json:"phase"`
	Worker      int          `json:"worker"`
	Iteration   int          `json:"iteration"`
	DurationMs  int64        `json:"durationMs"`
	Usage       usageMetrics `json:"usage"`
	SummaryByte int          `json:"summaryBytes"`
	LimitHit    bool         `json:"limitHit,omitempty"`
	ErrorText   string       `json:"errorText,omitempty"`
}

func findAgyBinary() (string, error) {
	if p := os.Getenv("AGY_BIN"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("agy"); err == nil {
		return p, nil
	}
	local := filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local", "agy", "bin", "agy.EXE")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	return "", fmt.Errorf("agy binary not found")
}

func seedThreeDocs(workDir string) error {
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return err
	}
	docs := map[string]string{
		"doc1_actor.md": `# Mô hình Actor của Max
Mỗi agent trong Max là một actor đơn luồng có hộp thư bền lưu trong SQLite ở chế độ WAL.
Mọi sự kiện đánh thức đều đi qua hàm Wake trong cùng giao dịch ghi với tác vụ sinh ra nó.
Tại mỗi thời điểm một agent có tối đa một run đang chạy để tránh xung đột trạng thái ghi file trong vault.
Khi tiến trình chính khởi động lại, các run đang dở chuyển sang interrupted và hộp thư được trả lại hàng đợi an toàn.`,
		"doc2_quota.md": `# Bộ điều tiết hạn mức gói đăng ký
Gói đăng ký giới hạn mức tiêu thụ token theo cửa sổ ngắn 5 giờ và cửa sổ tuần.
Vì nhà cung cấp không công bố con số trần cố định, Max vận hành bộ điều tiết tự học:
khi chưa biết trần thì để trống cấu hình và tự ghi nhận tổng token lúc chạm thông báo giới hạn đầu tiên.
Ở ngưỡng 50% cửa sổ ngắn các việc bảo trì dừng trước; ở ngưỡng 70% toàn bộ việc nền nhường chỗ cho tương tác trực tiếp của chủ nhân.`,
		"doc3_security.md": `# Ba lớp bảo vệ thực thi CLI
Lớp thứ nhất là cổng quyền trước khi chạy tool qua giao thức ACP cho phép chặn sửa file và lệnh shell trái phép.
Lớp thứ hai là kiểm toán sau mỗi run bằng git status trên vault để hoàn tác tức thì mọi thao tác ghi vượt phạm vi cho phép.
Lớp thứ ba là quét bí mật bằng bộ lọc redact trước khi bất kỳ thay đổi nào được commit hoặc đưa vào trí nhớ dài hạn.`,
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(body+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func isQuotaLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "quota") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "resource_exhausted") ||
		strings.Contains(s, "429") ||
		strings.Contains(s, "limit reached")
}

// executeSampleTask runs one fixed benchmark unit: read 3 files, write ~300-word summary.md.
func executeSampleTask(ctx context.Context, agyBin, homeDir, workDir, phase string, worker, iter int) runMeasurement {
	t0 := time.Now()
	m := runMeasurement{Phase: phase, Worker: worker, Iteration: iter}

	_ = os.RemoveAll(workDir)
	if err := seedThreeDocs(workDir); err != nil {
		m.ErrorText = err.Error()
		return m
	}
	summaryPath := filepath.Join(workDir, "summary.md")

	r, err := startACPRunner(ctx, agyBin, homeDir)
	if err != nil {
		m.ErrorText = err.Error()
		return m
	}
	defer r.close()

	if _, err := r.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "max-s5", "version": "0.1.0"},
	}); err != nil {
		m.DurationMs = time.Since(t0).Milliseconds()
		m.LimitHit = isQuotaLimitError(err)
		m.ErrorText = err.Error()
		return m
	}

	newRes, err := r.call(ctx, "session/new", map[string]any{
		"cwd":        workDir,
		"mcpServers": []any{},
	})
	if err != nil {
		m.DurationMs = time.Since(t0).Milliseconds()
		m.LimitHit = isQuotaLimitError(err)
		m.ErrorText = err.Error()
		return m
	}
	var sr struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(newRes, &sr)

	prompt := fmt.Sprintf(
		"Trong thư mục %s: hãy đọc cả 3 file doc1_actor.md, doc2_quota.md, doc3_security.md rồi viết một bản tổng hợp khoảng 250–300 từ tiếng Việt vào file summary.md, sau đó trả lời ngắn 'XONG'.",
		workDir,
	)
	pRes, err := r.call(ctx, "session/prompt", map[string]any{
		"sessionId": sr.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": prompt}},
	})
	m.DurationMs = time.Since(t0).Milliseconds()
	if err != nil {
		m.LimitHit = isQuotaLimitError(err)
		m.ErrorText = err.Error()
		return m
	}

	var pr promptResponse
	_ = json.Unmarshal(pRes, &pr)
	m.Usage = pr.Usage
	if st, statErr := os.Stat(summaryPath); statErr == nil {
		m.SummaryByte = int(st.Size())
	}
	return m
}

func appendMeasurementLog(logPath string, m runMeasurement) {
	if b, err := json.Marshal(m); err == nil {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
}

func main() {
	homeFlag := flag.String("home", "", "isolated agy agent-home directory")
	seqRuns := flag.Int("seq-runs", 4, "max sequential runs in Step 1-2 (0 = unlimited until max-duration or quota limit)")
	seqMaxDur := flag.Duration("seq-duration", 4*time.Hour, "max duration for sequential phase (Step 2)")
	parWorkers := flag.Int("par-workers", 3, "parallel agy processes in Step 3")
	parRounds := flag.Int("par-rounds", 2, "rounds per worker in Step 3 (0 = unlimited until par-duration)")
	parMaxDur := flag.Duration("par-duration", 30*time.Minute, "max duration for parallel phase (Step 3)")
	flag.Parse()

	baseDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	homeDir := *homeFlag
	if homeDir == "" {
		homeDir = filepath.Join(baseDir, "home")
	}
	agyBin, err := findAgyBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "find agy: %v\n", err)
		os.Exit(1)
	}

	logPath := filepath.Join(baseDir, "measurements.ndjson")
	_ = os.Remove(logPath)
	defer func() { _ = os.RemoveAll(filepath.Join(baseDir, "tmp")) }()

	// Phase A (Steps 1–2): 1 process sequential
	fmt.Printf("=== [Steps 1–2] Single-Process Baseline (maxRuns=%d, maxDur=%s) ===\n", *seqRuns, *seqMaxDur)
	seqCtx, cancelSeq := context.WithTimeout(context.Background(), *seqMaxDur)
	tSeqStart := time.Now()
	var seqTotalTokens int64
	seqCompleted := 0

	for i := 1; *seqRuns == 0 || i <= *seqRuns; i++ {
		if seqCtx.Err() != nil {
			break
		}
		workDir := filepath.Join(baseDir, "tmp", fmt.Sprintf("seq-%d", i))
		m := executeSampleTask(seqCtx, agyBin, homeDir, workDir, "sequential-1x", 1, i)
		appendMeasurementLog(logPath, m)
		fmt.Printf("  [1x #%d] dur=%.2fs | in=%d out=%d cached=%d thought=%d total=%d | summary=%dB | err=%s\n",
			i, float64(m.DurationMs)/1000.0,
			m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.CachedReadTokens, m.Usage.ThoughtTokens, m.Usage.TotalTokens,
			m.SummaryByte, m.ErrorText)
		if m.LimitHit || m.ErrorText != "" {
			break
		}
		seqCompleted++
		seqTotalTokens += m.Usage.TotalTokens
	}
	seqWallSec := time.Since(tSeqStart).Seconds()
	cancelSeq()

	seqTokPerMin := float64(seqTotalTokens) / max(seqWallSec/60.0, 0.001)
	seqRunsPerMin := float64(seqCompleted) / max(seqWallSec/60.0, 0.001)
	fmt.Printf("  [1x Summary] runs=%d | wall=%.1fs | totalTokens=%d | rate=%.0f tok/min (%.2f runs/min)\n",
		seqCompleted, seqWallSec, seqTotalTokens, seqTokPerMin, seqRunsPerMin)

	// Phase B (Step 3): 3 processes in parallel
	fmt.Printf("\n=== [Step 3] %d Parallel Processes Linearity Check (rounds=%d, maxDur=%s) ===\n",
		*parWorkers, *parRounds, *parMaxDur)
	parCtx, cancelPar := context.WithTimeout(context.Background(), *parMaxDur)
	tParStart := time.Now()

	var mu sync.Mutex
	var parTotalTokens int64
	parCompleted := 0
	var wg sync.WaitGroup

	for w := 1; w <= *parWorkers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 1; *parRounds == 0 || r <= *parRounds; r++ {
				if parCtx.Err() != nil {
					return
				}
				workDir := filepath.Join(baseDir, "tmp", fmt.Sprintf("par-w%d-r%d", w, r))
				m := executeSampleTask(parCtx, agyBin, homeDir, workDir, "parallel-3x", w, r)
				appendMeasurementLog(logPath, m)

				mu.Lock()
				fmt.Printf("  [3x W%d #%d] dur=%.2fs | in=%d out=%d cached=%d thought=%d total=%d | summary=%dB | err=%s\n",
					w, r, float64(m.DurationMs)/1000.0,
					m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.CachedReadTokens, m.Usage.ThoughtTokens, m.Usage.TotalTokens,
					m.SummaryByte, m.ErrorText)
				if m.ErrorText == "" {
					parCompleted++
					parTotalTokens += m.Usage.TotalTokens
				}
				mu.Unlock()
				if m.LimitHit || m.ErrorText != "" {
					return
				}
			}
		}()
	}
	wg.Wait()
	parWallSec := time.Since(tParStart).Seconds()
	cancelPar()

	parTokPerMin := float64(parTotalTokens) / max(parWallSec/60.0, 0.001)
	parRunsPerMin := float64(parCompleted) / max(parWallSec/60.0, 0.001)
	speedup := parTokPerMin / max(seqTokPerMin, 1.0)
	fmt.Printf("  [3x Summary] runs=%d | wall=%.1fs | totalTokens=%d | rate=%.0f tok/min (%.2f runs/min) | Scaling vs 1x = %.2fx\n",
		parCompleted, parWallSec, parTotalTokens, parTokPerMin, parRunsPerMin, speedup)
}

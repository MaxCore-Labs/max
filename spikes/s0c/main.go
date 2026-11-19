package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

func runStep5SysPrompt(ctx context.Context, agyBin, homeDir, baseDir string, tw *traceWriter) error {
	fmt.Println("\n=== [Step 5] _meta.antigravity.systemPrompt.append ===")
	workDir := filepath.Join(baseDir, "tmp", "s5")
	_ = os.MkdirAll(workDir, 0o700)

	c, err := startACP(ctx, agyBin, homeDir, "s5-sysprompt", "allow", tw)
	if err != nil {
		return err
	}
	defer c.close()
	if _, err := initACP(ctx, c); err != nil {
		return err
	}

	newRes, err := c.call(ctx, 25*time.Second, "session/new", map[string]any{
		"cwd":        workDir,
		"mcpServers": []mcpServerSpec{},
		"_meta": map[string]any{
			"antigravity": map[string]any{
				"systemPrompt": map[string]any{
					"append": "QUY TẮC BẮT BUỘC: Luôn kết thúc câu trả lời bằng đúng từ MAXOK (viết hoa, ở cuối cùng).",
				},
			},
		},
	})
	if err != nil {
		return err
	}
	var sr sessionNewResp
	if err := json.Unmarshal(newRes, &sr); err != nil {
		return err
	}
	if _, err := c.call(ctx, 60*time.Second, "session/prompt", map[string]any{
		"sessionId": sr.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "3 + 4 bằng mấy? Trả lời cực ngắn."}},
	}); err != nil {
		return err
	}
	reply := strings.TrimSpace(c.text())
	fmt.Printf("  Reply: %q | EndsWith(MAXOK)=%v\n",
		reply, strings.HasSuffix(strings.TrimRight(reply, ".! \n\r"), "MAXOK"))
	return nil
}

func runStep6Parallel(ctx context.Context, agyBin, homeDir, mcpPingBin, baseDir string, tw *traceWriter) error {
	fmt.Println("\n=== [Step 6] 3 Parallel agy acp Processes on Shared Isolated HOME ===")
	var wg sync.WaitGroup
	errs := make([]error, 3)

	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token := fmt.Sprintf("parallel-%d", i+1)
			workDir := filepath.Join(baseDir, "tmp", fmt.Sprintf("s6-%d", i+1))
			_ = os.RemoveAll(workDir)
			_ = os.MkdirAll(workDir, 0o700)
			pingLog := filepath.Join(workDir, "mcpping.ndjson")

			c, err := startACP(ctx, agyBin, homeDir, "s6-"+token, "allow", tw)
			if err != nil {
				errs[i] = err
				return
			}
			defer c.close()
			if _, err := initACP(ctx, c); err != nil {
				errs[i] = err
				return
			}
			newRes, err := c.call(ctx, 30*time.Second, "session/new", map[string]any{
				"cwd": workDir,
				"mcpServers": []mcpServerSpec{{
					Name:    "mcpping",
					Command: mcpPingBin,
					Args:    []string{},
					Env: []mcpEnvVar{
						{Name: "MAX_RUN_TOKEN", Value: token},
						{Name: "MCPPING_LOG", Value: pingLog},
					},
				}},
			})
			if err != nil {
				errs[i] = err
				return
			}
			var sr sessionNewResp
			if err := json.Unmarshal(newRes, &sr); err != nil {
				errs[i] = err
				return
			}
			if _, err := c.call(ctx, 90*time.Second, "session/prompt", map[string]any{
				"sessionId": sr.SessionID,
				"prompt":    []map[string]any{{"type": "text", "text": "Gọi tool ping của mcpping 1 lần rồi trả lời OK."}},
			}); err != nil {
				errs[i] = err
				return
			}
			lines, rerr := readPingLines(pingLog)
			if rerr != nil || len(lines) == 0 || lines[0].MaxRunToken != token {
				errs[i] = fmt.Errorf("worker %d token mismatch: %+v (err=%v)", i+1, lines, rerr)
				return
			}
			fmt.Printf("  Worker %d OK -> pid=%d ppid=%d token=%s\n", i+1, lines[0].PID, lines[0].PPID, lines[0].MaxRunToken)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func findAgyBinary() (string, error) {
	if p := os.Getenv("AGY_BIN"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("agy"); err == nil {
		return p, nil
	}
	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = append(candidates, filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local", "agy", "bin", "agy.EXE"))
	} else {
		if h, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(h, ".local", "bin", "agy"))
		}
		candidates = append(candidates, "/usr/local/bin/agy", "/usr/bin/agy")
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("agy binary not found in PATH or standard locations")
}

func resolveSpikeBaseDir() (string, error) {
	if _, err := os.Stat("mcpping/main.go"); err == nil {
		return os.Getwd()
	}
	if _, err := os.Stat(binaryName("mcpping")); err == nil {
		return os.Getwd()
	}
	return filepath.Abs("spikes/s0c")
}

func binaryName(stem string) string {
	if runtime.GOOS == "windows" {
		return stem + ".exe"
	}
	return stem
}

func main() {
	step := flag.String("step", "all", "check-unauth | check-home | s2-s4 | s3 | s5 | s6 | all")
	homeFlag := flag.String("home", "", "override isolated home directory")
	caseFlag := flag.String("case", "", "optional substring filter for Step 3 cases")
	flag.Parse()

	baseDir, err := resolveSpikeBaseDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve baseDir: %v\n", err)
		os.Exit(1)
	}

	homeDir := *homeFlag
	if homeDir == "" {
		homeDir = filepath.Join(baseDir, "home")
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir homeDir: %v\n", err)
		os.Exit(1)
	}
	tracePath := filepath.Join(baseDir, "trace.ndjson")
	mcpPingBin := filepath.Join(baseDir, binaryName("mcpping"))

	agyBin, err := findAgyBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	tw, err := newTraceWriter(tracePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = tw.close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if *step == "check-unauth" {
		if err := runCheckUnauth(ctx, agyBin, baseDir, tw); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL check-unauth: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *step == "check-home" {
		if err := runCheckHome(ctx, agyBin, homeDir, tw); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL check-home: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *step == "all" {
		if err := runCheckUnauth(ctx, agyBin, baseDir, tw); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL check-unauth: %v\n", err)
			os.Exit(1)
		}
	}
	if err := runCheckHome(ctx, agyBin, homeDir, tw); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL check-home: %v\n", err)
		os.Exit(1)
	}
	if *step == "s2-s4" || *step == "all" {
		if err := runStep2And4(ctx, agyBin, homeDir, mcpPingBin, baseDir, tw); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL s2-s4: %v\n", err)
			os.Exit(1)
		}
	}
	if *step == "s3" || *step == "all" {
		if err := runStep3PermissionsAndTools(ctx, agyBin, homeDir, baseDir, *caseFlag, tw); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL s3: %v\n", err)
			os.Exit(1)
		}
	}
	if *step == "s5" || *step == "all" {
		if err := runStep5SysPrompt(ctx, agyBin, homeDir, baseDir, tw); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL s5: %v\n", err)
			os.Exit(1)
		}
	}
	if *step == "s6" || *step == "all" {
		if err := runStep6Parallel(ctx, agyBin, homeDir, mcpPingBin, baseDir, tw); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL s6: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Println("\n=== ALL REQUESTED S0c PROBES FINISHED ===")
}

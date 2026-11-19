package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// classifyPlaywrightTool maps only tool names (R vs Ba). Argument-level guards
// (AllowedURL, stripping `filename`, egress proxy) must sit in the `max mcp` proxy.
func classifyPlaywrightTool(name string) string {
	readOnly := map[string]bool{
		"browser_navigate":         true,
		"browser_navigate_back":    true,
		"browser_snapshot":         true,
		"browser_find":             true,
		"browser_wait_for":         true,
		"browser_console_messages": true,
		"browser_network_requests": true,
		"browser_network_request":  true,
		"browser_take_screenshot":  true,
	}
	if readOnly[name] {
		return "R (read-only; proxy strips filename & enforces egress filter)"
	}
	return "Ba (browser action / mutating)"
}

func findBunBinary() (string, error) {
	if p := os.Getenv("BUN_BIN"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("bun"); err == nil {
		return p, nil
	}
	if home := os.Getenv("HOME"); home != "" {
		cand := filepath.Join(home, ".bun", "bin", "bun")
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("bun not found in PATH")
}

func connectPlaywrightMCP(ctx context.Context, bunExe, wsURL, workDir string) (*mcp.ClientSession, *exec.Cmd, error) {
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(bunExe, "x", "@playwright/mcp@0.0.85", "--cdp-endpoint", wsURL, "--output-dir", workDir)
	cmd.Dir = workDir
	setProcessGroup(cmd)
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "max-s1-probe",
		Version: "0.1.0",
	}, nil)
	sess, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, nil, err
	}
	return sess, cmd, nil
}

func extractText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func callTool(ctx context.Context, sess *mcp.ClientSession, name string, args map[string]any) (string, error) {
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("call %s: %w", name, err)
	}
	txt := extractText(res)
	if res.IsError {
		return txt, fmt.Errorf("tool %s returned error: %s", name, txt)
	}
	return txt, nil
}

func startHost(ctx context.Context, dataDir string) (browserHost, string, error) {
	if strings.TrimSpace(os.Getenv("CLOAKBROWSER_LICENSE_KEY")) != "" {
		h, err := startDockerCloakHost(ctx, dataDir, 19222)
		if err != nil {
			return nil, "", err
		}
		return h, "docker (cloakhq/cloakbrowser cloakserve)", nil
	}
	h, err := startLocalCDPShim(dataDir)
	if err != nil {
		return nil, "", err
	}
	return h, fmt.Sprintf("local-chromium-cdp-shim (%s)", h.chromeVersion), nil
}

func samplePeakHostMemory(h browserHost, samples int) (peakPSS, peakRSS float64, peakProcs int, err error) {
	for i := 0; i < samples; i++ {
		if i > 0 {
			time.Sleep(250 * time.Millisecond)
		}
		pss, rss, procs, mErr := h.MeasureMemoryMB()
		if mErr != nil {
			err = mErr
			continue
		}
		if pss > peakPSS {
			peakPSS = pss
		}
		if rss > peakRSS {
			peakRSS = rss
		}
		if procs > peakProcs {
			peakProcs = procs
		}
	}
	if peakProcs == 0 && err != nil {
		return 0, 0, 0, err
	}
	return peakPSS, peakRSS, peakProcs, nil
}

var linkRefRe = regexp.MustCompile(`link\s+"([^"]+)"[^\n]*\[ref=([A-Za-z0-9_-]+)\]`)

func run(ctx context.Context) error {
	bunExe, err := findBunBinary()
	if err != nil {
		return err
	}
	tmpRoot, err := os.MkdirTemp("", "max-s1-*")
	if err != nil {
		return fmt.Errorf("mkdir temp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpRoot) }()

	dataDir := filepath.Join(tmpRoot, "profiles")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("mkdir profiles: %w", err)
	}

	host1, modeName, err := startHost(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("start browser host: %w", err)
	}
	host1Closed := false
	defer func() {
		if !host1Closed {
			_ = host1.Close()
		}
	}()

	wsURL1, err := fetchWSDebuggerURL(ctx, host1.VersionURL("anon"))
	if err != nil {
		return fmt.Errorf("fetch ws debugger url: %w", err)
	}
	fmt.Printf("=== [Step 2] Mode: %s ===\n  CDP webSocketDebuggerUrl (fingerprint=anon): %s\n", modeName, wsURL1)

	sess1, mcpCmd1, err := connectPlaywrightMCP(ctx, bunExe, wsURL1, filepath.Join(tmpRoot, "mcp-sess1"))
	if err != nil {
		return fmt.Errorf("connect playwright mcp: %w", err)
	}
	sess1Closed := false
	defer func() {
		if !sess1Closed {
			_ = sess1.Close()
		}
	}()

	toolsRes, err := sess1.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	names := make([]string, 0, len(toolsRes.Tools))
	for _, t := range toolsRes.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	fmt.Printf("\n=== [Step 3a & Step 5] Playwright MCP v0.0.85 Default Tools (%d total) ===\n", len(names))
	for _, n := range names {
		fmt.Printf("  %-28s -> %s\n", n, classifyPlaywrightTool(n))
	}

	publicURLs := []string{
		"https://www.rfc-editor.org/",
		"https://www.iana.org/domains/reserved",
		"https://example.com/",
	}
	fmt.Println("\n=== [Step 3b] Navigating to 3 Public Pages ===")
	for i, u := range publicURLs {
		out, err := callTool(ctx, sess1, "browser_navigate", map[string]any{"url": u})
		if err != nil {
			return fmt.Errorf("navigate %s: %w", u, err)
		}
		fmt.Printf("  Page %d (%s) -> %s\n", i+1, u, strings.Split(strings.TrimSpace(out), "\n")[0])
	}

	pss1, rss1, procs1, err := samplePeakHostMemory(host1, 5)
	if err != nil {
		return fmt.Errorf("measure 1-tab memory: %w", err)
	}
	var bridgePSS, bridgeRSS float64
	var bridgeProcs int
	if mcpCmd1.Process != nil {
		bridgePSS, bridgeRSS, bridgeProcs = measureProcessTreeMB(mcpCmd1.Process.Pid)
	}
	fmt.Printf("  [Step 5a] Browser 1 Tab (5 samples, %d procs): Peak PSS=%.1f MiB, Peak RSS=%.1f MiB\n", procs1, pss1, rss1)
	fmt.Printf("  [Step 5a] @playwright/mcp Bridge (%d procs): PSS=%.1f MiB, RSS=%.1f MiB\n", bridgeProcs, bridgePSS, bridgeRSS)

	cookieSetOut, err := callTool(ctx, sess1, "browser_evaluate", map[string]any{
		"function": "() => { document.cookie = 'max_s1_persist=persisted_ok_42; path=/; max-age=86400'; return document.cookie; }",
	})
	if err != nil {
		return fmt.Errorf("set cookie: %w", err)
	}
	fmt.Printf("  [Step 3c] Cookie set on example.com: %s\n", strings.TrimSpace(cookieSetOut))

	snapText, err := callTool(ctx, sess1, "browser_snapshot", map[string]any{})
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	m := linkRefRe.FindStringSubmatch(snapText)
	if len(m) < 3 {
		return fmt.Errorf("no link ref found in snapshot:\n%s", snapText)
	}
	clickOut, err := callTool(ctx, sess1, "browser_click", map[string]any{"target": m[2]})
	if err != nil {
		return fmt.Errorf("click ref %s: %w", m[2], err)
	}
	fmt.Printf("  [Step 3d] Clicked link %q [ref=%s] -> %s\n", m[1], m[2], strings.Split(strings.TrimSpace(clickOut), "\n")[0])

	if _, err := callTool(ctx, sess1, "browser_tabs", map[string]any{"action": "new"}); err != nil {
		return fmt.Errorf("open 2nd tab: %w", err)
	}
	if _, err := callTool(ctx, sess1, "browser_navigate", map[string]any{"url": "https://example.com/"}); err != nil {
		return fmt.Errorf("navigate 2nd tab: %w", err)
	}
	pss2, rss2, procs2, err := samplePeakHostMemory(host1, 5)
	if err != nil {
		return fmt.Errorf("measure 2-tab memory: %w", err)
	}
	fmt.Printf("  [Step 5a] Browser 2 Tabs (5 samples, %d procs): Peak PSS=%.1f MiB, Peak RSS=%.1f MiB\n", procs2, pss2, rss2)

	sess1Closed = true
	_ = sess1.Close()
	host1Closed = true
	if err := host1.Close(); err != nil {
		return fmt.Errorf("graceful close host1: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 4a: Restart cleanly & verify cookie survived; also set a fresh SIGKILL test cookie before ForceKill
	fmt.Println("\n=== [Step 4] Restarting Browser Host & Verifying Cookie (Clean Close vs SIGKILL) ===")
	host2, _, err := startHost(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("restart host2: %w", err)
	}
	host2Closed := false
	defer func() {
		if !host2Closed {
			_ = host2.Close()
		}
	}()
	wsURL2, err := fetchWSDebuggerURL(ctx, host2.VersionURL("anon"))
	if err != nil {
		return fmt.Errorf("fetch ws2: %w", err)
	}
	sess2, _, err := connectPlaywrightMCP(ctx, bunExe, wsURL2, filepath.Join(tmpRoot, "mcp-sess2"))
	if err != nil {
		return fmt.Errorf("connect mcp2: %w", err)
	}
	sess2Closed := false
	defer func() {
		if !sess2Closed {
			_ = sess2.Close()
		}
	}()
	if _, err := callTool(ctx, sess2, "browser_navigate", map[string]any{"url": "https://example.com/"}); err != nil {
		return fmt.Errorf("navigate example.com after restart: %w", err)
	}
	cookieAfter, err := callTool(ctx, sess2, "browser_evaluate", map[string]any{"function": "() => document.cookie"})
	if err != nil {
		return fmt.Errorf("read cookie after restart: %w", err)
	}
	fmt.Printf("  [Step 4a] Cookie after clean shutdown: %s\n", strings.TrimSpace(cookieAfter))
	if !strings.Contains(cookieAfter, "max_s1_persist=persisted_ok_42") {
		return fmt.Errorf("cookie did not persist across clean restart (got %q)", cookieAfter)
	}

	// Step 4b: Empirical SIGKILL counter-experiment (set fresh cookie and immediately SIGKILL browser)
	_, err = callTool(ctx, sess2, "browser_evaluate", map[string]any{
		"function": "() => { document.cookie = 'max_s1_sigkill=after_kill_99; path=/; max-age=86400'; return document.cookie; }",
	})
	if err != nil {
		return fmt.Errorf("set sigkill cookie: %w", err)
	}
	host2Closed = true
	_ = host2.ForceKill()
	sess2Closed = true
	_ = sess2.Close()
	time.Sleep(400 * time.Millisecond)

	host3, _, err := startHost(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("restart host3 after SIGKILL: %w", err)
	}
	defer func() { _ = host3.Close() }()
	wsURL3, err := fetchWSDebuggerURL(ctx, host3.VersionURL("anon"))
	if err != nil {
		return fmt.Errorf("fetch ws3: %w", err)
	}
	sess3, _, err := connectPlaywrightMCP(ctx, bunExe, wsURL3, filepath.Join(tmpRoot, "mcp-sess3"))
	if err != nil {
		return fmt.Errorf("connect mcp3: %w", err)
	}
	defer func() { _ = sess3.Close() }()
	if _, err := callTool(ctx, sess3, "browser_navigate", map[string]any{"url": "https://example.com/"}); err != nil {
		return fmt.Errorf("navigate host3: %w", err)
	}
	cookieAfterKill, err := callTool(ctx, sess3, "browser_evaluate", map[string]any{"function": "() => document.cookie"})
	if err != nil {
		return fmt.Errorf("read cookie after SIGKILL: %w", err)
	}
	sigkillSurvived := strings.Contains(cookieAfterKill, "max_s1_sigkill=after_kill_99")
	fmt.Printf("  [Step 4b] Cookie after immediate SIGKILL: %s (sigkillCookieSurvived=%v)\n", strings.TrimSpace(cookieAfterKill), sigkillSurvived)

	fmt.Println("\n=== ALL S1 BROWSER MCP PROBES COMPLETED ===")
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
}

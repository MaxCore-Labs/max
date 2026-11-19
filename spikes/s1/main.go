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

func classifyPlaywrightTool(name string) string {
	readOnly := map[string]bool{
		"browser_navigate":         true, // R after AllowedURL(url) SSRF guard
		"browser_navigate_back":    true,
		"browser_snapshot":         true,
		"browser_find":             true,
		"browser_wait_for":         true,
		"browser_console_messages": true,
		"browser_network_requests": true,
		"browser_take_screenshot":  true, // R only when filename arg is omitted (no arbitrary disk write)
	}
	if readOnly[name] {
		return "R (read-only allowlist)"
	}
	return "Ba (browser action / mutating)"
}

func connectPlaywrightMCP(ctx context.Context, bunExe, wsURL string) (*mcp.ClientSession, error) {
	cmd := exec.Command(bunExe, "x", "@playwright/mcp@0.0.85", "--cdp-endpoint", wsURL)
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "max-s1-probe",
		Version: "0.1.0",
	}, nil)
	return client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
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
	if key := strings.TrimSpace(os.Getenv("CLOAKBROWSER_LICENSE_KEY")); key != "" {
		h, err := startDockerCloakHost(ctx, dataDir, key, 19222)
		if err != nil {
			return nil, "", err
		}
		return h, "docker (cloakhq/cloakbrowser cloakserve)", nil
	}
	h, err := startLocalCDPShim(dataDir)
	if err != nil {
		return nil, "", err
	}
	return h, "local-chromium-cdp-shim (--keep-profiles)", nil
}

var linkRefRe = regexp.MustCompile(`link\s+"([^"]+)"[^\n]*\[ref=([A-Za-z0-9_-]+)\]`)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	bunExe, err := exec.LookPath("bun")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bun not found: %v\n", err)
		os.Exit(1)
	}

	baseDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	dataDir := filepath.Join(baseDir, "tmp", "profiles")
	_ = os.RemoveAll(dataDir)
	_ = os.MkdirAll(dataDir, 0o700)
	defer func() { _ = os.RemoveAll(filepath.Join(baseDir, "tmp")) }()

	// Step 2: Start cloakserve (or CDP shim) + @playwright/mcp@0.0.85
	host1, modeName, err := startHost(ctx, dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start browser host: %v\n", err)
		os.Exit(1)
	}
	wsURL1, err := fetchWSDebuggerURL(host1.VersionURL("anon"))
	if err != nil {
		_ = host1.Close()
		fmt.Fprintf(os.Stderr, "fetch ws debugger url: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("=== [Step 2] Mode: %s ===\n  CDP webSocketDebuggerUrl (fingerprint=anon): %s\n", modeName, wsURL1)

	sess1, err := connectPlaywrightMCP(ctx, bunExe, wsURL1)
	if err != nil {
		_ = host1.Close()
		fmt.Fprintf(os.Stderr, "connect playwright mcp: %v\n", err)
		os.Exit(1)
	}

	// Step 3a & 5b: List all tools and classify read-only vs action
	toolsRes, err := sess1.ListTools(ctx, nil)
	if err != nil {
		_ = sess1.Close()
		_ = host1.Close()
		fmt.Fprintf(os.Stderr, "list tools: %v\n", err)
		os.Exit(1)
	}
	names := make([]string, 0, len(toolsRes.Tools))
	for _, t := range toolsRes.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	fmt.Printf("\n=== [Step 3a & Step 5] Playwright MCP v0.0.85 Tools (%d total) ===\n", len(names))
	for _, n := range names {
		fmt.Printf("  %-28s -> %s\n", n, classifyPlaywrightTool(n))
	}

	// Step 3b: Navigate to 3 public pages
	publicURLs := []string{
		"https://www.rfc-editor.org/",
		"https://www.iana.org/domains/reserved",
		"https://example.com/",
	}
	fmt.Println("\n=== [Step 3b] Navigating to 3 Public Pages ===")
	for i, u := range publicURLs {
		out, err := callTool(ctx, sess1, "browser_navigate", map[string]any{"url": u})
		if err != nil {
			_ = sess1.Close()
			_ = host1.Close()
			fmt.Fprintf(os.Stderr, "navigate %s: %v\n", u, err)
			os.Exit(1)
		}
		firstLine := strings.Split(strings.TrimSpace(out), "\n")[0]
		fmt.Printf("  Page %d (%s) -> %s\n", i+1, u, firstLine)
	}

	// Measure RSS with 1 tab open
	time.Sleep(400 * time.Millisecond)
	rss1TabMB, procs1Tab := host1.MeasureRSSMB()
	fmt.Printf("  [Step 5a] Chromium RSS (1 Tab, %d procs): %.1f MB\n", procs1Tab, rss1TabMB)

	// Set persistent cookie on https://example.com before clicking link & restarting
	cookieSetOut, err := callTool(ctx, sess1, "browser_evaluate", map[string]any{
		"function": "() => { document.cookie = 'max_s1_persist=persisted_ok_42; path=/; max-age=86400'; return document.cookie; }",
	})
	if err != nil {
		_ = sess1.Close()
		_ = host1.Close()
		fmt.Fprintf(os.Stderr, "set cookie: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  [Step 3c] Cookie set on example.com: %s\n", strings.TrimSpace(cookieSetOut))

	// Step 3c: Take snapshot and click link by ref
	snapText, err := callTool(ctx, sess1, "browser_snapshot", map[string]any{})
	if err != nil {
		_ = sess1.Close()
		_ = host1.Close()
		fmt.Fprintf(os.Stderr, "snapshot: %v\n", err)
		os.Exit(1)
	}
	m := linkRefRe.FindStringSubmatch(snapText)
	if len(m) < 3 {
		_ = sess1.Close()
		_ = host1.Close()
		fmt.Fprintf(os.Stderr, "no link ref found in snapshot:\n%s\n", snapText)
		os.Exit(1)
	}
	linkLabel, linkRef := m[1], m[2]
	clickOut, err := callTool(ctx, sess1, "browser_click", map[string]any{
		"target": linkRef,
	})
	if err != nil {
		_ = sess1.Close()
		_ = host1.Close()
		fmt.Fprintf(os.Stderr, "click ref %s: %v\n", linkRef, err)
		os.Exit(1)
	}
	fmt.Printf("  [Step 3d] Clicked link %q [ref=%s] -> %s\n",
		linkLabel, linkRef, strings.Split(strings.TrimSpace(clickOut), "\n")[0])

	// Open 2nd tab & measure RSS with 2 tabs
	if _, err := callTool(ctx, sess1, "browser_tabs", map[string]any{"action": "new"}); err == nil {
		_, _ = callTool(ctx, sess1, "browser_navigate", map[string]any{"url": "https://example.com/"})
	}
	time.Sleep(500 * time.Millisecond)
	rss2TabMB, procs2Tab := host1.MeasureRSSMB()
	fmt.Printf("  [Step 5a] Chromium RSS (2 Tabs, %d procs): %.1f MB\n", procs2Tab, rss2TabMB)

	_ = sess1.Close()
	_ = host1.Close()
	time.Sleep(600 * time.Millisecond)

	// Step 4: Restart cloakserve / Chromium on same --data-dir & fingerprint=anon, verify cookie survived!
	fmt.Println("\n=== [Step 4] Restarting Browser Host & Verifying Persistent Cookie ===")
	host2, _, err := startHost(ctx, dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart host: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = host2.Close() }()

	wsURL2, err := fetchWSDebuggerURL(host2.VersionURL("anon"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch ws2: %v\n", err)
		os.Exit(1)
	}
	sess2, err := connectPlaywrightMCP(ctx, bunExe, wsURL2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect mcp2: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = sess2.Close() }()

	if _, err := callTool(ctx, sess2, "browser_navigate", map[string]any{"url": "https://example.com/"}); err != nil {
		fmt.Fprintf(os.Stderr, "navigate example.com after restart: %v\n", err)
		os.Exit(1)
	}
	cookieAfter, err := callTool(ctx, sess2, "browser_evaluate", map[string]any{
		"function": "() => document.cookie",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "read cookie after restart: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Cookie after restart: %s\n", strings.TrimSpace(cookieAfter))
	if !strings.Contains(cookieAfter, "max_s1_persist=persisted_ok_42") {
		fmt.Fprintf(os.Stderr, "FAIL: cookie did not persist across restart (got %q)\n", cookieAfter)
		os.Exit(1)
	}
	fmt.Println("\n=== ALL S1 BROWSER MCP PROBES PASSED ===")
}

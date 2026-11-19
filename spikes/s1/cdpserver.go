package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type browserHost interface {
	VersionURL(fingerprint string) string
	MeasureMemoryMB() (pssMB float64, rssMB float64, procs int, err error)
	Close() error
	ForceKill() error
}

var safeSeedRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// localCDPShim emulates `cloakserve --data-dir=<dir> --keep-profiles` (including
// `/fingerprint/{seed}/devtools/...` URL rewriting and `POST /fingerprint/{seed}/close`)
// over local headless Chromium when CLOAKBROWSER_LICENSE_KEY is absent.
type localCDPShim struct {
	chromeBin     string
	chromeVersion string
	dataDir       string
	ln            net.Listener
	srv           *http.Server

	mu    sync.Mutex
	procs map[string]*shimBrowser
}

type shimBrowser struct {
	cmd     *exec.Cmd
	cdpPort int
}

func findChromiumBinary() (string, string, error) {
	var candidates []string
	if p := strings.TrimSpace(os.Getenv("CHROME_BIN")); p != "" {
		candidates = append(candidates, p)
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	candidates = append(candidates,
		`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	)
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			ver := filepath.Base(c)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if out, vErr := exec.CommandContext(ctx, c, "--version").Output(); vErr == nil {
				if line := strings.TrimSpace(string(out)); line != "" {
					ver = line
				}
			}
			cancel()
			return c, ver, nil
		}
	}
	return "", "", fmt.Errorf("no Chromium/Chrome binary found (set CHROME_BIN)")
}

func startLocalCDPShim(dataDir string) (*localCDPShim, error) {
	chromeBin, chromeVer, err := findChromiumBinary()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &localCDPShim{
		chromeBin:     chromeBin,
		chromeVersion: chromeVer,
		dataDir:       dataDir,
		ln:            ln,
		procs:         make(map[string]*shimBrowser),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /json/version", s.handleVersion)
	mux.HandleFunc("/fingerprint/", s.handleFingerprintRoute)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func waitDevToolsActivePort(profileDir string, timeout time.Duration) (int, error) {
	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(portFile)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 1 {
				if p, pErr := strconv.Atoi(strings.TrimSpace(lines[0])); pErr == nil && p > 0 {
					return p, nil
				}
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	return 0, fmt.Errorf("timed out waiting for DevToolsActivePort in %s", profileDir)
}

func (s *localCDPShim) ensureSeed(ctx context.Context, seed string) (*shimBrowser, error) {
	if !safeSeedRe.MatchString(seed) {
		return nil, fmt.Errorf("invalid fingerprint seed %q", seed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.procs[seed]; ok {
		return b, nil
	}
	profileDir := filepath.Join(s.dataDir, "profile-"+seed)
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(profileDir, "DevToolsActivePort"))

	cmd := exec.Command(s.chromeBin,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--user-data-dir="+profileDir,
		"--remote-debugging-port=0",
		"about:blank",
	)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	port, err := waitDevToolsActivePort(profileDir, 15*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
		if rErr == nil {
			resp, dErr := cdpHTTPClient.Do(req)
			if dErr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					b := &shimBrowser{cmd: cmd, cdpPort: port}
					s.procs[seed] = b
					return b, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil, fmt.Errorf("chromium CDP /json/version timeout on port %d", port)
}

func (s *localCDPShim) handleVersion(w http.ResponseWriter, r *http.Request) {
	seed := r.URL.Query().Get("fingerprint")
	if seed == "" {
		seed = "anon"
	}
	if !safeSeedRe.MatchString(seed) {
		http.Error(w, "invalid fingerprint seed", http.StatusBadRequest)
		return
	}
	b, err := s.ensureSeed(r.Context(), seed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", b.cdpPort), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := cdpHTTPClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var meta map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if origWS, ok := meta["webSocketDebuggerUrl"].(string); ok {
		if u, pErr := url.Parse(origWS); pErr == nil {
			meta["webSocketDebuggerUrl"] = fmt.Sprintf("ws://%s/fingerprint/%s%s", s.ln.Addr().String(), seed, u.RequestURI())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

func (s *localCDPShim) VersionURL(fp string) string {
	return fmt.Sprintf("http://%s/json/version?fingerprint=%s", s.ln.Addr().String(), fp)
}

func (s *localCDPShim) MeasureMemoryMB() (pssMB float64, rssMB float64, procs int, err error) {
	s.mu.Lock()
	var pids []int
	for _, b := range s.procs {
		if b.cmd.Process != nil {
			pids = append(pids, b.cmd.Process.Pid)
		}
	}
	s.mu.Unlock()
	if len(pids) == 0 {
		return 0, 0, 0, fmt.Errorf("no active shim browser process")
	}
	var sumPSS, sumRSS float64
	var sumProcs int
	for _, pid := range pids {
		p, r, c := measureProcessTreeMB(pid)
		sumPSS += p
		sumRSS += r
		sumProcs += c
	}
	if sumProcs == 0 {
		return 0, 0, 0, fmt.Errorf("process tree memory returned 0 processes")
	}
	return sumPSS, sumRSS, sumProcs, nil
}

func (s *localCDPShim) closeSeed(seed string, graceful bool) {
	s.mu.Lock()
	b, ok := s.procs[seed]
	if ok {
		delete(s.procs, seed)
	}
	s.mu.Unlock()
	if !ok || b.cmd.Process == nil {
		return
	}
	if !graceful {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
		return
	}
	if !sendBrowserGracefulStop(b.cmd) {
		gracefulCDPBrowserClose(context.Background(), b.cdpPort)
	}
	done := make(chan struct{})
	go func() {
		_ = b.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = b.cmd.Process.Kill()
		<-done
	}
}

func (s *localCDPShim) Close() error {
	_ = s.srv.Shutdown(context.Background())
	s.mu.Lock()
	seeds := make([]string, 0, len(s.procs))
	for k := range s.procs {
		seeds = append(seeds, k)
	}
	s.mu.Unlock()
	for _, k := range seeds {
		s.closeSeed(k, true)
	}
	return nil
}

func (s *localCDPShim) ForceKill() error {
	_ = s.srv.Shutdown(context.Background())
	s.mu.Lock()
	seeds := make([]string, 0, len(s.procs))
	for k := range s.procs {
		seeds = append(seeds, k)
	}
	s.mu.Unlock()
	for _, k := range seeds {
		s.closeSeed(k, false)
	}
	return nil
}

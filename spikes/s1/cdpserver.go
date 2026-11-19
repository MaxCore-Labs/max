package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type browserHost interface {
	VersionURL(fingerprint string) string
	MeasureRSSMB() (float64, int)
	Close() error
}

// dockerCloakHost drives `cloakhq/cloakbrowser cloakserve` when CLOAKBROWSER_LICENSE_KEY is set.
type dockerCloakHost struct {
	containerName string
	port          int
}

func startDockerCloakHost(ctx context.Context, dataDir, licenseKey string, port int) (*dockerCloakHost, error) {
	name := fmt.Sprintf("max-s1-cloak-%d", port)
	_ = exec.Command("docker", "rm", "-f", name).Run()

	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:9222", port),
		"-v", fmt.Sprintf("%s:/data", absData),
		"-e", "CLOAKBROWSER_LICENSE_KEY="+licenseKey,
		"cloakhq/cloakbrowser",
		"cloakserve", "--data-dir=/data", "--keep-profiles",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run cloakbrowser: %w (%s)", err, string(out))
	}
	h := &dockerCloakHost{containerName: name, port: port}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version?fingerprint=probe", port))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return h, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = h.Close()
	return nil, fmt.Errorf("cloakserve container did not become ready on port %d", port)
}

func (h *dockerCloakHost) VersionURL(fp string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/json/version?fingerprint=%s", h.port, fp)
}

func (h *dockerCloakHost) MeasureRSSMB() (float64, int) {
	out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", h.containerName).Output()
	if err != nil {
		return 0, 1
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, 1
	}
	raw := strings.TrimSuffix(fields[0], "MiB")
	v, _ := strconv.ParseFloat(raw, 64)
	return v, 1
}

func (h *dockerCloakHost) Close() error {
	return exec.Command("docker", "rm", "-f", h.containerName).Run()
}

// localCDPShim emulates `cloakserve --data-dir=<dir> --keep-profiles` over local headless Chromium
// when CLOAKBROWSER_LICENSE_KEY is not configured on the dev machine.
type localCDPShim struct {
	chromeBin string
	dataDir   string
	ln        net.Listener
	srv       *http.Server

	mu    sync.Mutex
	procs map[string]*shimBrowser
}

type shimBrowser struct {
	cmd     *exec.Cmd
	cdpPort int
}

func findChromiumBinary() (string, error) {
	candidates := []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	if p, err := exec.LookPath("chromium"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("google-chrome"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("no Chromium binary found")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p, nil
}

func startLocalCDPShim(dataDir string) (*localCDPShim, error) {
	chromeBin, err := findChromiumBinary()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &localCDPShim{
		chromeBin: chromeBin,
		dataDir:   dataDir,
		ln:        ln,
		procs:     make(map[string]*shimBrowser),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /json/version", s.handleVersion)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *localCDPShim) ensureSeed(seed string) (*shimBrowser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.procs[seed]; ok {
		return b, nil
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	profileDir := filepath.Join(s.dataDir, "profile-"+seed)
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command(s.chromeBin,
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--user-data-dir="+profileDir,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"about:blank",
	)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				b := &shimBrowser{cmd: cmd, cdpPort: port}
				s.procs[seed] = b
				return b, nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("chromium CDP timeout on port %d", port)
}

func (s *localCDPShim) handleVersion(w http.ResponseWriter, r *http.Request) {
	seed := r.URL.Query().Get("fingerprint")
	if seed == "" {
		seed = "anon"
	}
	b, err := s.ensureSeed(seed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", b.cdpPort))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, resp.Body)
}

func (s *localCDPShim) VersionURL(fp string) string {
	return fmt.Sprintf("http://%s/json/version?fingerprint=%s", s.ln.Addr().String(), fp)
}

func (s *localCDPShim) MeasureRSSMB() (float64, int) {
	s.mu.Lock()
	var rootPIDs []int
	for _, b := range s.procs {
		if b.cmd.Process != nil {
			rootPIDs = append(rootPIDs, b.cmd.Process.Pid)
		}
	}
	s.mu.Unlock()
	if len(rootPIDs) == 0 {
		return 0, 0
	}
	rootID := rootPIDs[0]
	ps := fmt.Sprintf(`
$all = Get-CimInstance Win32_Process
$ids = [System.Collections.Generic.HashSet[uint32]]::new()
[void]$ids.Add(%d)
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
`, rootID)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 2 {
		return 0, 0
	}
	cnt, _ := strconv.Atoi(parts[0])
	sum, _ := strconv.ParseInt(parts[1], 10, 64)
	return float64(sum) / (1024 * 1024), cnt
}

func gracefulCDPBrowserClose(cdpPort int) {
	wsURL, err := fetchWSDebuggerURL(fmt.Sprintf("http://127.0.0.1:%d/json/version", cdpPort))
	if err != nil {
		return
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return
	}
	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	var keyBytes [16]byte
	_, _ = rand.Read(keyBytes[:])
	wsKey := base64.StdEncoding.EncodeToString(keyBytes[:])
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host, wsKey)
	if _, err := conn.Write([]byte(req)); err != nil {
		return
	}
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	// Send masked text frame: {"id":1,"method":"Browser.close"}
	payload := []byte(`{"id":1,"method":"Browser.close"}`)
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	frame := make([]byte, 0, 6+len(payload))
	frame = append(frame, 0x81, byte(0x80|len(payload)))
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, _ = conn.Write(frame)
	time.Sleep(250 * time.Millisecond)
}

func (s *localCDPShim) Close() error {
	_ = s.srv.Shutdown(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.procs {
		gracefulCDPBrowserClose(b.cdpPort)
		if b.cmd.Process != nil {
			done := make(chan struct{})
			go func(c *exec.Cmd) {
				_ = c.Wait()
				close(done)
			}(b.cmd)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = b.cmd.Process.Kill()
			}
		}
	}
	s.procs = map[string]*shimBrowser{}
	return nil
}

func fetchWSDebuggerURL(versionURL string) (string, error) {
	resp, err := http.Get(versionURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var data struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.WS == "" {
		return "", fmt.Errorf("empty webSocketDebuggerUrl from %s", versionURL)
	}
	return data.WS, nil
}

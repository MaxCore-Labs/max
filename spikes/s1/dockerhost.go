package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var cdpHTTPClient = &http.Client{Timeout: 5 * time.Second}

// parseByteSizeMB parses human-readable Docker/cgroup byte strings ("850MiB", "1.25GiB", "419430400").
func parseByteSizeMB(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty byte size")
	}
	type unitDef struct {
		suffix string
		multMB float64
	}
	units := []unitDef{
		{"TiB", 1024 * 1024},
		{"TB", 1e12 / (1024 * 1024)},
		{"GiB", 1024},
		{"GB", 1e9 / (1024 * 1024)},
		{"MiB", 1},
		{"MB", 1e6 / (1024 * 1024)},
		{"KiB", 1.0 / 1024.0},
		{"kB", 1e3 / (1024 * 1024)},
		{"KB", 1.0 / 1024.0},
		{"B", 1.0 / (1024 * 1024)},
	}
	for _, u := range units {
		if numStr, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64)
			if err != nil {
				return 0, fmt.Errorf("parse %q: %w", raw, err)
			}
			return v * u.multMB, nil
		}
	}
	b, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse raw bytes %q: %w", raw, err)
	}
	return b / (1024 * 1024), nil
}

// dockerCloakHost drives `cloakhq/cloakbrowser cloakserve` when CLOAKBROWSER_LICENSE_KEY is set in env.
type dockerCloakHost struct {
	containerName string
	port          int
	mu            sync.Mutex
	seeds         map[string]bool
}

func startDockerCloakHost(ctx context.Context, dataDir string, port int) (*dockerCloakHost, error) {
	name := fmt.Sprintf("max-s1-cloak-%d", port)
	_ = exec.Command("docker", "rm", "-f", name).Run()

	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:9222", port),
		"-v", fmt.Sprintf("%s:/data", absData),
		// Pass env var name only (without =value) so the license key never appears in `ps` argv.
		"-e", "CLOAKBROWSER_LICENSE_KEY",
		"cloakhq/cloakbrowser",
		"cloakserve", "--data-dir=/data", "--keep-profiles",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run cloakbrowser: %w (%s)", err, string(out))
	}
	h := &dockerCloakHost{
		containerName: name,
		port:          port,
		seeds:         map[string]bool{"probe": true},
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version?fingerprint=probe", port), nil)
		if err == nil {
			resp, reqErr := cdpHTTPClient.Do(req)
			if reqErr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return h, nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = h.Close()
	return nil, fmt.Errorf("cloakserve container did not become ready on port %d", port)
}

func (h *dockerCloakHost) VersionURL(fp string) string {
	h.mu.Lock()
	h.seeds[fp] = true
	h.mu.Unlock()
	return fmt.Sprintf("http://127.0.0.1:%d/json/version?fingerprint=%s", h.port, fp)
}

func (h *dockerCloakHost) MeasureMemoryMB() (pssMB float64, rssMB float64, procs int, err error) {
	cgOut, cgErr := exec.Command("docker", "exec", h.containerName, "sh", "-c",
		"cat /sys/fs/cgroup/memory.current 2>/dev/null || cat /sys/fs/cgroup/memory/memory.usage_in_bytes").Output()
	if cgErr == nil {
		if mb, pErr := parseByteSizeMB(strings.TrimSpace(string(cgOut))); pErr == nil && mb > 0 {
			return mb, mb, 1, nil
		}
	}
	out, sErr := exec.Command("docker", "stats", "--no-stream", "--format", "{{.MemUsage}}|{{.PIDs}}", h.containerName).Output()
	if sErr != nil {
		return 0, 0, 0, fmt.Errorf("docker stats: %w", sErr)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	usageFields := strings.Fields(parts[0])
	if len(usageFields) == 0 {
		return 0, 0, 0, fmt.Errorf("unexpected docker stats output %q", string(out))
	}
	mb, err := parseByteSizeMB(usageFields[0])
	if err != nil {
		return 0, 0, 0, err
	}
	pids := 1
	if len(parts) > 1 {
		if n, pErr := strconv.Atoi(strings.TrimSpace(parts[1])); pErr == nil && n > 0 {
			pids = n
		}
	}
	return mb, mb, pids, nil
}

func (h *dockerCloakHost) Close() error {
	h.mu.Lock()
	seeds := make([]string, 0, len(h.seeds))
	for s := range h.seeds {
		seeds = append(seeds, s)
	}
	h.mu.Unlock()

	// 1. Release active fingerprint seats while keeping profile directories on disk.
	for _, s := range seeds {
		u := fmt.Sprintf("http://127.0.0.1:%d/fingerprint/%s/close", h.port, s)
		req, err := http.NewRequest(http.MethodPost, u, nil)
		if err == nil {
			if resp, reqErr := cdpHTTPClient.Do(req); reqErr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
	}
	// 2. Allow cloakserve up to 30s (> 10s SIGINT + /session/end window) before removing container.
	_ = exec.Command("docker", "stop", "-t", "30", h.containerName).Run()
	return exec.Command("docker", "rm", "-f", h.containerName).Run()
}

func (h *dockerCloakHost) ForceKill() error {
	return exec.Command("docker", "rm", "-f", h.containerName).Run()
}

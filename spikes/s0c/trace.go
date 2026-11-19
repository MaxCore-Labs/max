package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type traceEntry struct {
	Timestamp string          `json:"ts"`
	Tag       string          `json:"tag"`
	Direction string          `json:"dir"`
	Raw       json.RawMessage `json:"raw,omitempty"`
	Text      string          `json:"text,omitempty"`
}

type traceWriter struct {
	mu sync.Mutex
	f  *os.File
}

func newTraceWriter(path string) (*traceWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open trace %s: %w", path, err)
	}
	return &traceWriter{f: f}, nil
}

func (tw *traceWriter) write(tag, dir string, raw []byte, text string) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	ent := traceEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Tag:       tag,
		Direction: dir,
		Text:      text,
	}
	if len(raw) > 0 && json.Valid(raw) {
		cp := make([]byte, len(raw))
		copy(cp, raw)
		ent.Raw = cp
	} else if len(raw) > 0 {
		ent.Text = string(raw)
	}
	if b, err := json.Marshal(ent); err == nil {
		_, _ = tw.f.Write(append(b, '\n'))
	}
}

func (tw *traceWriter) close() error {
	return tw.f.Close()
}

func isolatedEnv(homeDir string) []string {
	xdgConfig := filepath.Join(homeDir, ".config")
	xdgData := filepath.Join(homeDir, ".local", "share")
	xdgCache := filepath.Join(homeDir, ".cache")
	_ = os.MkdirAll(xdgConfig, 0o700)
	_ = os.MkdirAll(xdgData, 0o700)
	_ = os.MkdirAll(xdgCache, 0o700)

	keepKeys := []string{"PATH", "TZ", "LANG", "TERM"}
	if runtime.GOOS == "windows" {
		keepKeys = append(keepKeys, "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP")
	}
	env := make([]string, 0, len(keepKeys)+8)
	for _, k := range keepKeys {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+xdgConfig,
		"XDG_DATA_HOME="+xdgData,
		"XDG_CACHE_HOME="+xdgCache,
	)
	if runtime.GOOS == "windows" {
		appData := filepath.Join(homeDir, "AppData", "Roaming")
		localAppData := filepath.Join(homeDir, "AppData", "Local")
		_ = os.MkdirAll(appData, 0o700)
		_ = os.MkdirAll(localAppData, 0o700)
		env = append(env,
			"USERPROFILE="+homeDir,
			"APPDATA="+appData,
			"LOCALAPPDATA="+localAppData,
		)
	}
	return env
}

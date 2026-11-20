package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"max/internal/adapter/sqlite"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

type binaryGuard struct {
	store app.Store
	bus   *platform.Bus
	clock app.Clock
	ids   app.IDGen
}

func newBinaryGuard(store app.Store, bus *platform.Bus, clock app.Clock, ids app.IDGen) *binaryGuard {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &binaryGuard{store: store, bus: bus, clock: clock, ids: ids}
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (g *binaryGuard) Confirm(ctx context.Context, engine domain.EngineName, binPath, version string) error {
	digest, err := hashFileSHA256(binPath)
	if err != nil {
		return fmt.Errorf("hash engine binary %q: %w", binPath, err)
	}
	nowMs := g.clock.Now().UnixMilli()
	return g.store.WithTx(ctx, func(tx app.Tx) error {
		return tx.UpsertEngineBinary(ctx, app.EngineBinaryRecord{
			Engine:      engine,
			Path:        binPath,
			SHA256:      digest,
			Version:     version,
			ConfirmedAt: nowMs,
		})
	})
}

func (g *binaryGuard) Verify(ctx context.Context, engine domain.EngineName, binPath string) (bool, error) {
	rec, ok, err := g.store.Read().GetEngineBinary(ctx, engine)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	target := binPath
	if target == "" {
		target = rec.Path
	}
	digest, err := hashFileSHA256(target)
	if err != nil || digest != rec.SHA256 {
		nowMs := g.clock.Now().UnixMilli()
		dedupe := fmt.Sprintf("bin-changed:%s:%s", engine, digest)
		_ = g.store.WithTx(ctx, func(tx app.Tx) error {
			return tx.InsertInboxItem(ctx, app.InboxItemRecord{
				ID:        g.ids.New(),
				Kind:      "system",
				Title:     fmt.Sprintf("Binary %s đã thay đổi", engine),
				Body:      fmt.Sprintf("SHA-256 của %s đã đổi. Chạy `max engine confirm %s` sau khi kiểm tra.", target, engine),
				Urgency:   "high",
				DedupeKey: &dedupe,
				CreatedAt: nowMs,
			})
		})
		if g.bus != nil {
			g.bus.Publish(platform.Event{
				Topic: "status",
				Type:  "system.notice",
				Data: map[string]any{
					"engine": string(engine),
					"reason": "binary_sha256_changed",
				},
			})
		}
		return false, nil
	}
	return true, nil
}

func runDoctor(stdout, stderr io.Writer) int {
	cfg, err := platform.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "doctor config error: %v\n", err)
		return 1
	}
	engines, _ := defaultEngines(cfg)
	return runDoctorWithEngines(context.Background(), cfg, engines, stdout, stderr)
}

func runDoctorWithEngines(
	ctx context.Context,
	cfg platform.Config,
	engines map[domain.EngineName]app.Engine,
	stdout, stderr io.Writer,
) int {
	failed := false

	if _, err := exec.LookPath("git"); err != nil {
		_, _ = fmt.Fprintf(stderr, "[FAIL] git binary not found: %v\n", err)
		failed = true
	} else {
		_, _ = fmt.Fprintln(stdout, "[OK] git binary available")
	}

	_ = os.MkdirAll(cfg.DataDir, 0o700)
	st, err := sqlite.Open(ctx, filepath.Join(cfg.DataDir, "max.sqlite3"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[FAIL] sqlite open: %v\n", err)
		failed = true
	} else {
		_ = st.Close()
		_, _ = fmt.Fprintln(stdout, "[OK] sqlite store ready")
	}

	vault := vaultgit.New(cfg.VaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "[FAIL] vault ensure: %v\n", err)
		failed = true
	} else if err := vault.RefuseIfProjectSettings(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "[FAIL] vault guard: %v\n", err)
		failed = true
	} else {
		_, _ = fmt.Fprintln(stdout, "[OK] vault clean")
	}

	for name, drv := range engines {
		h := drv.Health(ctx)
		if !h.Installed {
			_, _ = fmt.Fprintf(stderr, "[FAIL] engine %s: binary not installed\n", name)
			failed = true
			continue
		}
		if !h.LoggedIn {
			_, _ = fmt.Fprintf(stderr, "[FAIL] engine %s: not logged in\n", name)
			failed = true
			continue
		}
		if len(h.ForeignMCP) > 0 {
			_, _ = fmt.Fprintf(stderr, "[FAIL] engine %s: foreign MCP servers detected: %s\n", name, strings.Join(h.ForeignMCP, ", "))
			failed = true
			continue
		}
		_, _ = fmt.Fprintf(stdout, "[OK] engine %s healthy (%s)\n", name, h.Version)
	}

	if failed {
		return 1
	}
	return 0
}

func runMigrate(stdout, stderr io.Writer) int {
	cfg, err := platform.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	_ = os.MkdirAll(cfg.DataDir, 0o700)
	st, err := sqlite.Open(context.Background(), filepath.Join(cfg.DataDir, "max.sqlite3"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "migrate failed: %v\n", err)
		return 1
	}
	_ = st.Close()
	_, _ = fmt.Fprintln(stdout, "migrations applied")
	return 0
}

func runHealthcheck(stdout, stderr io.Writer) int {
	cfg, err := platform.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	url := "http://" + cfg.Listen + "/api/v1/healthz"
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck request failed: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "healthcheck status %d\n", resp.StatusCode)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

func runEngineCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "confirm" {
		_, _ = fmt.Fprintln(stderr, "usage: max engine confirm <engine>")
		return 2
	}
	engName := domain.EngineName(strings.TrimSpace(args[1]))
	if domain.EngineBit(engName) == 0 {
		_, _ = fmt.Fprintf(stderr, "unknown engine %q\n", args[1])
		return 2
	}

	cfg, err := platform.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}

	envKey := "MAX_" + strings.ToUpper(string(engName)) + "_BIN"
	binPath := strings.TrimSpace(os.Getenv(envKey))
	if binPath == "" {
		resolved, err := exec.LookPath(string(engName))
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cannot locate binary for %s: %v\n", engName, err)
			return 1
		}
		binPath = resolved
	}

	_ = os.MkdirAll(cfg.DataDir, 0o700)
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(cfg.DataDir, "max.sqlite3"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open sqlite: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	guard := newBinaryGuard(st, nil, app.SystemClock{}, app.UUIDv7Gen{})
	if err := guard.Confirm(ctx, engName, binPath, "confirmed"); err != nil {
		_, _ = fmt.Fprintf(stderr, "confirm engine %s: %v\n", engName, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "confirmed engine %s (%s)\n", engName, binPath)
	return 0
}

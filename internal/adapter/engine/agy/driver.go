package agy

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

const (
	defaultIdleTimeout = 15 * time.Minute
	defaultKillGrace   = 10 * time.Second
	maxStderrCap       = 32 * 1024
)

// Mode selects the wire protocol used to drive the agy CLI.
type Mode int

const (
	ModeACP Mode = iota
	ModePrint
)

// Driver implements app.Engine for the Antigravity CLI (agy 1.5.12).
type Driver struct {
	bin           string
	home          string
	mode          Mode
	log           *slog.Logger
	maxBin        string
	mcpSock       string
	extraEnv      []string
	limitPatterns []*regexp.Regexp
	idleTimeout   time.Duration
}

var _ app.Engine = (*Driver)(nil)

// New constructs an agy Driver rooted at an isolated agent HOME directory.
func New(bin, home string, mode Mode, log *slog.Logger) *Driver {
	if log == nil {
		log = slog.Default()
	}
	if filepath.Base(bin) != bin {
		if abs, err := filepath.Abs(bin); err == nil {
			bin = abs
		}
	}
	if home != "" {
		if absHome, err := filepath.Abs(home); err == nil {
			home = absHome
		}
	}
	self, _ := os.Executable()
	if self == "" {
		self = "max"
	}
	return &Driver{
		bin:           bin,
		home:          home,
		mode:          mode,
		log:           log,
		maxBin:        self,
		limitPatterns: defaultLimitPatterns(),
		idleTimeout:   defaultIdleTimeout,
	}
}

// WithMaxBin overrides the binary path and optional unix socket passed to mcpServers in ACP sessions.
func (d *Driver) WithMaxBin(maxBin, mcpSock string) *Driver {
	if maxBin != "" {
		d.maxBin = maxBin
	}
	if mcpSock != "" {
		if absSock, err := filepath.Abs(mcpSock); err == nil {
			mcpSock = absSock
		}
	}
	d.mcpSock = mcpSock
	return d
}

// WithExtraEnv appends explicit KEY=VALUE pairs to the isolated child environment (used in tests).
func (d *Driver) WithExtraEnv(kv ...string) *Driver {
	d.extraEnv = append(d.extraEnv, kv...)
	return d
}

// WithLimitPatterns overrides the regexes used to classify subscription rate/quota limit errors.
func (d *Driver) WithLimitPatterns(patterns []*regexp.Regexp) *Driver {
	d.limitPatterns = append([]*regexp.Regexp(nil), patterns...)
	return d
}

func (d *Driver) Name() domain.EngineName { return domain.EngineAgy }

// Caps returns protocol capabilities verified by Spike S0c (agy remains Level 3: PreToolGate=false, ToolAllowlist=false).
func (d *Driver) Caps() app.EngineCaps {
	if d.mode == ModePrint {
		return app.EngineCaps{
			PreToolGate:   false,
			ToolAllowlist: false,
			Sandbox:       false,
			PerRunMCP:     false,
			Resume:        true,
			StructuredOut: true,
		}
	}
	return app.EngineCaps{
		PreToolGate:   false,
		ToolAllowlist: false,
		Sandbox:       false,
		PerRunMCP:     true,
		Resume:        true,
		StructuredOut: false,
	}
}

func (d *Driver) Run(ctx context.Context, spec app.RunSpec, perm app.PermissionFunc) (<-chan app.EngineEvent, error) {
	if err := d.prepareIsolatedHome(); err != nil {
		return nil, err
	}
	if d.mode == ModePrint {
		return d.runPrint(ctx, spec)
	}
	return d.runACP(ctx, spec, perm)
}

func (d *Driver) prepareIsolatedHome() error {
	for _, sub := range []string{
		"",
		".config",
		".local/share",
		".cache",
		".local/state",
		"tmp",
		"appdata/roaming",
		"appdata/local",
	} {
		p := filepath.Join(d.home, filepath.FromSlash(sub))
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) isolatedEnv() []string {
	env := []string{
		"HOME=" + d.home,
		"USERPROFILE=" + d.home,
		"XDG_CONFIG_HOME=" + filepath.Join(d.home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(d.home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(d.home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(d.home, ".local", "state"),
		"APPDATA=" + filepath.Join(d.home, "appdata", "roaming"),
		"LOCALAPPDATA=" + filepath.Join(d.home, "appdata", "local"),
		"TMPDIR=" + filepath.Join(d.home, "tmp"),
		"TEMP=" + filepath.Join(d.home, "tmp"),
		"TMP=" + filepath.Join(d.home, "tmp"),
		"LANG=C.UTF-8",
	}
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if tz := os.Getenv("TZ"); tz != "" {
		env = append(env, "TZ="+tz)
	}
	if runtime.GOOS == "windows" {
		if sr := os.Getenv("SystemRoot"); sr != "" {
			env = append(env, "SYSTEMROOT="+sr)
		}
	}
	env = append(env, d.extraEnv...)
	return env
}

type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	cap int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rem := b.cap - b.buf.Len()
	if rem > 0 {
		if len(p) <= rem {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:rem])
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return platform.Redact(b.buf.String())
}

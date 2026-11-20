package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"max/internal/adapter/engine/agy"
	"max/internal/adapter/httpapi"
	"max/internal/adapter/mcpserver"
	"max/internal/adapter/scopeaudit"
	"max/internal/adapter/sqlite"
	"max/internal/adapter/telegram"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	"max/internal/app/approvals"
	"max/internal/app/chat"
	"max/internal/app/connections"
	"max/internal/app/coord"
	"max/internal/app/dispatch"
	"max/internal/app/grants"
	"max/internal/app/learn"
	"max/internal/app/limits"
	"max/internal/app/outbox"
	runapp "max/internal/app/run"
	"max/internal/app/scheduler"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/platform"
)

// ServeOptions configures runServeWithOptions (allowing tests to inject deterministic engines).
type ServeOptions struct {
	Config   platform.Config
	Engines  map[domain.EngineName]app.Engine
	BinPaths map[domain.EngineName]string
}

func defaultEngines(cfg platform.Config) (map[domain.EngineName]app.Engine, map[domain.EngineName]string) {
	log := platform.NewLogger(os.Stdout, "info")
	agyBin := os.Getenv("MAX_AGY_BIN")
	if agyBin == "" {
		if resolved, err := exec.LookPath("agy"); err == nil {
			agyBin = resolved
		} else {
			agyBin = "agy"
		}
	}
	agyHome := filepath.Join(cfg.DataDir, "agent-home", "agy")
	_ = os.MkdirAll(agyHome, 0o700)
	drv := agy.New(agyBin, agyHome, agy.ModeACP, log)
	sockPath := filepath.Join(cfg.DataDir, "run", "max.sock")
	drv.WithMaxBin("", sockPath)
	return map[domain.EngineName]app.Engine{domain.EngineAgy: drv},
		map[domain.EngineName]string{domain.EngineAgy: agyBin}
}

func runServe(stdout, stderr io.Writer) int {
	cfg, err := platform.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServeWithOptions(ctx, ServeOptions{Config: cfg}, stdout, stderr)
}

func runServeWithOptions(ctx context.Context, opts ServeOptions, stdout, stderr io.Writer) int {
	cfg := opts.Config
	if cfg.DataDir != "" {
		if absData, err := filepath.Abs(cfg.DataDir); err == nil {
			cfg.DataDir = absData
		}
	}
	if cfg.VaultDir != "" {
		if absVault, err := filepath.Abs(cfg.VaultDir); err == nil {
			cfg.VaultDir = absVault
		}
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8787"
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = "http://" + cfg.Listen
	}
	if cfg.TZ == "" {
		cfg.TZ = "Asia/Ho_Chi_Minh"
	}
	if len(cfg.Slots) == 0 {
		cfg.Slots = map[domain.EngineName]int{domain.EngineAgy: 3}
	}

	_ = os.MkdirAll(cfg.DataDir, 0o700)
	st, err := sqlite.Open(ctx, filepath.Join(cfg.DataDir, "max.sqlite3"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open sqlite: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	vault := vaultgit.New(cfg.VaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "ensure vault: %v\n", err)
		return 1
	}
	if err := vault.RefuseIfProjectSettings(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "vault check: %v\n", err)
		return 1
	}

	clk := app.SystemClock{}
	ids := app.UUIDv7Gen{}

	syncSvc := agentsync.NewService(st, vault, clk, ids)
	_, _, _ = syncSvc.SyncAll(ctx)

	actors := actor.NewService(st, clk, ids)
	if _, err := actors.Recover(ctx, clk.Now()); err != nil {
		_, _ = fmt.Fprintf(stderr, "recover on boot: %v\n", err)
		return 1
	}

	engines := opts.Engines
	binPaths := opts.BinPaths
	if len(engines) == 0 {
		engines, binPaths = defaultEngines(cfg)
	}

	slots := limits.NewSlots(cfg.Slots, nil)
	quota := limits.NewQuota(st, clk, limits.QuotaConfig{
		ShortHours:      cfg.QuotaShortHours,
		BackgroundShare: cfg.QuotaBackgroundShare,
		InitEstShort:    cfg.QuotaShortTokens,
		InitEstWeek:     cfg.QuotaWeekTokens,
		TZ:              cfg.TZ,
	})

	bus := platform.NewBus()
	hub := httpapi.NewHub(bus, clk)
	binGuard := newBinaryGuard(st, bus, clk, ids)

	refreshEngineAvailability := func() {
		for name, drv := range engines {
			h := drv.Health(ctx)
			healthy := h.Installed && h.LoggedIn && len(h.ForeignMCP) == 0
			if healthy && binPaths[name] != "" {
				if _, exists, _ := st.Read().GetEngineBinary(ctx, name); !exists {
					_ = binGuard.Confirm(ctx, name, binPaths[name], h.Version)
				} else if ok, _ := binGuard.Verify(ctx, name, binPaths[name]); !ok {
					healthy = false
				}
			}
			slots.SetHealthAvailable(name, healthy)
		}
	}
	refreshEngineAvailability()

	var (
		cancelMu   sync.Mutex
		runCancels = make(map[domain.ID]context.CancelFunc)
	)
	cancelRunFn := func(runID domain.ID) {
		cancelMu.Lock()
		fn := runCancels[runID]
		cancelMu.Unlock()
		if fn != nil {
			fn()
		}
	}
	coordSvc := coord.NewService(coord.Deps{
		Store:     st,
		Actors:    actors,
		Clock:     clk,
		IDs:       ids,
		CancelRun: cancelRunFn,
		Publish: func(topic, typ string, data any) {
			bus.Publish(platform.Event{Topic: topic, Type: typ, Data: data})
		},
	})
	memSvc := sqlite.NewMemory(st, clk, ids)
	connSvc, _ := connections.NewService(st, cfg.DataDir, clk)
	grantSvc := grants.NewService(st, clk, ids)
	learnSvc := learn.NewService(st, vault, engines[domain.EngineAgy], clk, ids)
	apprSvc := approvals.NewService(st, actors, nil, clk, ids)
	toolSvc := tools.NewService(st, cfg.VaultDir, clk, ids).WithCoord(coordSvc, actors).WithMemory(memSvc).WithVault(vault)
	mcpSrv := mcpserver.NewServer(toolSvc)
	sockPath := filepath.Join(cfg.DataDir, "run", "max.sock")
	if len(sockPath) > 100 {
		sockPath = filepath.Join(os.TempDir(), fmt.Sprintf("max-%d.sock", os.Getpid()))
	}
	_ = mcpSrv.StartUnix(ctx, sockPath)
	quarantineBase := filepath.Join(cfg.DataDir, "quarantine")

	runSvc := runapp.NewService(runapp.Deps{
		Store:    st,
		Actors:   actors,
		Coord:    coordSvc,
		Engines:  engines,
		Prompts:  &vaultPromptBuilder{store: st, vault: vault, clock: clk, tz: cfg.TZ},
		Memory:   memSvc,
		Vault:    vault,
		VaultDir: cfg.VaultDir,
		Clock:    clk,
		IDs:      ids,
		OnEvent: func(c actor.Claimed, ev app.EngineEvent) {
			if ev.Type != "text_delta" || ev.Text == "" {
				return
			}
			hub.AppendRunText(c.RunID, ev.Text)
			if conv, ok, _ := st.Read().GetConversation(ctx, c.ConversationID); ok && conv.ThreadID != nil {
				bus.Publish(platform.Event{
					Topic: "thread:" + *conv.ThreadID,
					Type:  "run.text",
					Data:  map[string]any{"runId": c.RunID, "delta": ev.Text},
				})
			}
		},
		PostRunAuditFull: func(actx context.Context, runID domain.ID, scope domain.AgentScope) ([]string, int) {
			touched, violations, err := scopeaudit.Audit(actx, cfg.VaultDir, filepath.Join(quarantineBase, runID), scope)
			if err != nil {
				return nil, 0
			}
			return touched, len(violations)
		},
	})
	runSvc.SetPermission(runapp.NewPermissionFunc(runapp.PermissionDeps{
		Store:     st,
		Runs:      runSvc,
		Approvals: apprSvc,
		VaultDir:  cfg.VaultDir,
		Clock:     clk,
	}))

	disp := dispatch.New(dispatch.Deps{
		Store:        st,
		Actors:       actors,
		Runs:         runSvc,
		Slots:        slots,
		Quota:        quota,
		Clock:        clk,
		PollInterval: 100 * time.Millisecond,
		OnRunDone: func(c actor.Claimed, out runapp.Outcome) {
			hub.ClearRunText(c.RunID)
			if conv, ok, _ := st.Read().GetConversation(context.Background(), c.ConversationID); ok && conv.ThreadID != nil {
				bus.Publish(platform.Event{
					Topic: "thread:" + *conv.ThreadID,
					Type:  "run.ended",
					Data:  map[string]any{"runId": c.RunID, "status": out.Status},
				})
			}
		},
	})

	tzLoc, _ := time.LoadLocation(cfg.TZ)
	sched := scheduler.New(st, actors, apprSvc, disp.Notify).WithCoord(coordSvc).WithRoutines(vault, tzLoc, ids)
	chatSvc := chat.NewService(st, actors, clk, ids, func(runID domain.ID) string {
		snap, _, cancel := hub.RunText(runID)
		cancel()
		return snap
	})

	tgNotifier := telegram.New(telegram.Config{
		BotToken:  cfg.TelegramBotToken,
		OwnerIDs:  cfg.TelegramOwnerIDs,
		PublicURL: cfg.PublicURL,
		Clock:     clk,
	})
	outboxDisp := outbox.NewDispatcher(outbox.Config{
		Store:      st,
		Notifier:   tgNotifier,
		Clock:      clk,
		TZ:         cfg.TZ,
		QuietHours: cfg.QuietHours,
		PublicURL:  cfg.PublicURL,
	})

	handler, err := httpapi.New(httpapi.Deps{
		Store:       st,
		Vault:       vault,
		AgentSync:   syncSvc,
		Actors:      actors,
		Coord:       coordSvc,
		Scheduler:   sched,
		Learn:       learnSvc,
		Connections: connSvc,
		Grants:      grantSvc,
		Approvals:   apprSvc,
		Chat:        chatSvc,
		Slots:       slots,
		Quota:       quota,
		Engines:     engines,
		CancelRun: func(runID domain.ID) {
			cancelMu.Lock()
			fn := runCancels[runID]
			cancelMu.Unlock()
			if fn != nil {
				fn()
			}
		},
		Bus:    bus,
		Hub:    hub,
		Config: cfg,
		Clock:  clk,
		IDs:    ids,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "init httpapi: %v\n", err)
		return 1
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "listen %s: %v\n", cfg.Listen, err)
		return 1
	}
	g, gctx := errgroup.WithContext(ctx)
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return gctx },
	}
	g.Go(func() error { return disp.Run(gctx) })
	g.Go(func() error { return sched.Run(gctx, clk) })
	g.Go(func() error { return outboxDisp.Run(gctx) })
	g.Go(func() error {
		tk := time.NewTicker(time.Hour)
		defer tk.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-tk.C:
				refreshEngineAvailability()
			}
		}
	})
	g.Go(func() error {
		<-gctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutCtx)
	})
	g.Go(func() error {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	_, _ = fmt.Fprintf(stdout, "max listening on %s\n", ln.Addr().String())
	if err := g.Wait(); err != nil {
		_, _ = fmt.Fprintf(stderr, "serve error: %v\n", err)
		return 1
	}
	if cfg.VaultRemote != "" {
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = vault.Push(pushCtx)
		cancel()
	}
	return 0
}

var _ = json.Marshal

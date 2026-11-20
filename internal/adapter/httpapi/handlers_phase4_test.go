package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/httpapi"
	"max/internal/adapter/sqlite"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/limits"
	"max/internal/domain"
	"max/internal/platform"
)

func TestStatusBacklogAge(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	st, err := sqlite.Open(ctx, filepath.Join(dataDir, "max.sqlite3"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	v := vaultgit.New(vaultDir)
	_ = v.EnsureInstalled(ctx)

	t0 := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	clk := &p3Clock{now: t0}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)

	slots := limits.NewSlots(
		map[domain.EngineName]int{
			domain.EngineAgy:    1,
			domain.EngineClaude: 1,
		},
		nil,
	)
	if !slots.Acquire(domain.EngineAgy) {
		t.Fatal("Acquire(agy) failed")
	}
	defer slots.Release(domain.EngineAgy)

	quota := limits.NewQuota(st, clk, limits.QuotaConfig{
		ShortHours:      5,
		BackgroundShare: 0.7,
	})

	err = st.WithTx(ctx, func(tx app.Tx) error {
		tmID, err := tx.EnsureTeamBySlug(ctx, "core", "t-core")
		if err != nil {
			return err
		}
		// 1. Agent on agy (mask=4), slots full -> PrioTask (50) wakeup created 45s ago -> reason = "slots_full"
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-slots", Slug: "dev-slots", Name: "Slots", TeamID: tmID,
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-slots", AgentID: "ag-slots", Kind: domain.WakeTaskAssigned,
			Priority: domain.PrioTask, Payload: `{}`, Status: "queued",
			CreatedAt: t0.Add(-45 * time.Second).UnixMilli(),
		}); err != nil {
			return err
		}

		// 2. Agent on claude (mask=1), claude is quota-limited until t0+30m -> PrioRoutine (30) wakeup created 120s ago -> reason = "quota_limited"
		if err := tx.UpsertQuotaLimit(ctx, domain.EngineClaude, t0.Add(30*time.Minute).UnixMilli(), t0.UnixMilli()); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-quota", Slug: "mon-quota", Name: "Quota", TeamID: tmID,
			EnginesJSON: `["claude"]`, EngineMask: 1, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-quota", AgentID: "ag-quota", Kind: domain.WakeRoutine,
			Priority: domain.PrioRoutine, Payload: `{}`, Status: "queued",
			CreatedAt: t0.Add(-120 * time.Second).UnixMilli(),
		}); err != nil {
			return err
		}

		// 3. Agent in cooldown until t0+20s -> PrioMaintenance (10) wakeup created 15s ago -> reason = "cooldown"
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-cool", Slug: "hb-cool", Name: "Cooldown", TeamID: tmID,
			EnginesJSON: `["agy","claude"]`, EngineMask: 5, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		if err := tx.SetAgentCooldown(ctx, "ag-cool", t0.Add(20*time.Second).UnixMilli()); err != nil {
			return err
		}
		return tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-cool", AgentID: "ag-cool", Kind: domain.WakeHeartbeat,
			Priority: domain.PrioMaintenance, Payload: `{}`, Status: "queued",
			CreatedAt: t0.Add(-15 * time.Second).UnixMilli(),
		})
	})
	if err != nil {
		t.Fatalf("seed backlog: %v", err)
	}

	h, err := httpapi.New(httpapi.Deps{
		Store:  st,
		Vault:  v,
		Actors: actors,
		Slots:  slots,
		Quota:  quota,
		Config: platform.Config{
			DataDir:       dataDir,
			VaultDir:      vaultDir,
			Listen:        "127.0.0.1:8787",
			PublicURL:     "http://127.0.0.1:8787",
			AdminPassword: "phase4-owner-password",
		},
		Clock: clk,
		IDs:   ids,
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"phase4-owner-password"}`))
	loginReq.Host = "127.0.0.1:8787"
	loginReq.Header.Set("Origin", "http://127.0.0.1:8787")
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	cookie := loginRec.Result().Cookies()[0]

	stReq := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	stReq.Host = "127.0.0.1:8787"
	stReq.AddCookie(cookie)
	stRec := httptest.NewRecorder()
	h.ServeHTTP(stRec, stReq)
	if stRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/status = %d: %s", stRec.Code, stRec.Body.String())
	}

	var parsed struct {
		Queue struct {
			Total        int `json:"total"`
			QuotaDelayed int `json:"quotaDelayed"`
			Backlog      []struct {
				Priority     int    `json:"priority"`
				Count        int    `json:"count"`
				OldestAgeSec int64  `json:"oldestAgeSec"`
				Reason       string `json:"reason"`
			} `json:"backlog"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(stRec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if len(parsed.Queue.Backlog) != 3 {
		t.Fatalf("backlog len = %d, want 3 (raw=%s)", len(parsed.Queue.Backlog), stRec.Body.String())
	}

	byPrio := make(map[int]struct {
		age    int64
		reason string
	})
	for _, b := range parsed.Queue.Backlog {
		byPrio[b.Priority] = struct {
			age    int64
			reason string
		}{age: b.OldestAgeSec, reason: b.Reason}
	}

	if got := byPrio[int(domain.PrioTask)]; got.age != 45 || got.reason != "slots_full" {
		t.Fatalf("PrioTask backlog = %+v, want age=45 reason=slots_full", got)
	}
	if got := byPrio[int(domain.PrioRoutine)]; got.age != 120 || got.reason != "quota_limited" {
		t.Fatalf("PrioRoutine backlog = %+v, want age=120 reason=quota_limited", got)
	}
	if got := byPrio[int(domain.PrioMaintenance)]; got.age != 15 || got.reason != "cooldown" {
		t.Fatalf("PrioMaintenance backlog = %+v, want age=15 reason=cooldown", got)
	}
	if parsed.Queue.QuotaDelayed < 1 {
		t.Fatalf("quotaDelayed = %d, want >= 1", parsed.Queue.QuotaDelayed)
	}
}

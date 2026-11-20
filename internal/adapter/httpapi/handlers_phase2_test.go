package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/httpapi"
	"max/internal/adapter/httpapi/gen"
	"max/internal/adapter/sqlite"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	"max/internal/app/approvals"
	"max/internal/app/chat"
	"max/internal/app/coord"
	"max/internal/app/limits"
	"max/internal/app/scheduler"
	"max/internal/domain"
	"max/internal/platform"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

type phase2APIEnv struct {
	st       *sqlite.Store
	vault    *vaultgit.Vault
	vaultDir string
	syncSvc  *agentsync.Service
	actors   *actor.Service
	coordSvc *coord.Service
	sched    *scheduler.Scheduler
	h        http.Handler
	cookie   *http.Cookie
}

func newPhase2APIEnv(t *testing.T) phase2APIEnv {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	vault := vaultgit.New(vaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		t.Fatalf("vault.EnsureInstalled: %v", err)
	}
	clk := app.SystemClock{}
	ids := app.UUIDv7Gen{}
	syncSvc := agentsync.NewService(st, vault, clk, ids)
	if _, _, err := syncSvc.SyncAll(ctx); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	actorSvc := actor.NewService(st, clk, ids)
	bus := platform.NewBus()
	coordSvc := coord.NewService(coord.Deps{
		Store:  st,
		Actors: actorSvc,
		Clock:  clk,
		IDs:    ids,
		Publish: func(topic, typ string, data any) {
			bus.Publish(platform.Event{Topic: topic, Type: typ, Data: data})
		},
	})
	apprSvc := approvals.NewService(st, actorSvc, nil, clk, ids)
	sched := scheduler.New(st, actorSvc, apprSvc, nil).WithCoord(coordSvc).WithRoutines(vault, time.UTC, ids)
	chatSvc := chat.NewService(st, actorSvc, clk, ids, nil)
	slots := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 3, domain.EngineClaude: 2}, nil)
	quota := limits.NewQuota(st, clk, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7, TZ: "UTC"})

	tpl := "---\nname: Researcher\nengines: [agy]\nweb: true\nrequired_params:\n  - topic\n---\nResearch {{topic}}.\n"
	if err := vault.Write(ctx, "templates/researcher/TEMPLATE.md", []byte(tpl)); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if _, err := vault.CommitPaths(ctx, []string{"templates/researcher/TEMPLATE.md"}, "seed researcher template"); err != nil {
		t.Fatalf("commit template: %v", err)
	}

	h, err := httpapi.New(httpapi.Deps{
		Store:     st,
		Vault:     vault,
		AgentSync: syncSvc,
		Actors:    actorSvc,
		Coord:     coordSvc,
		Scheduler: sched,
		Approvals: apprSvc,
		Chat:      chatSvc,
		Slots:     slots,
		Quota:     quota,
		Engines: map[domain.EngineName]app.Engine{
			domain.EngineAgy:    fakeengine.New(domain.EngineAgy),
			domain.EngineClaude: fakeengine.New(domain.EngineClaude),
		},
		Bus: bus,
		Config: platform.Config{
			Listen:        "127.0.0.1:8787",
			PublicURL:     "http://127.0.0.1:8787",
			AdminPassword: testAdminPassword,
			VaultDir:      vaultDir,
		},
		Clock: clk,
		IDs:   ids,
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	loginRec := doLogin(h, "127.0.0.1:8787", testAdminPassword)
	return phase2APIEnv{
		st:       st.(*sqlite.Store),
		vault:    vault,
		vaultDir: vaultDir,
		syncSvc:  syncSvc,
		actors:   actorSvc,
		coordSvc: coordSvc,
		sched:    sched,
		h:        h,
		cookie:   loginRec.Result().Cookies()[0],
	}
}

func (e phase2APIEnv) do(method, path, body string) *httptest.ResponseRecorder {
	return phase1APIEnv{h: e.h, cookie: e.cookie}.do(method, path, body, nil)
}

func TestListAgentsFilters(t *testing.T) {
	ctx := context.Background()
	env := newPhase2APIEnv(t)
	r1, r2 := "researcher", "writer"
	if err := env.st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "tm-mkt", Slug: "marketing", Name: "Marketing", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		for i := 1; i <= 4; i++ {
			tpl := &r1
			st := domain.AgentActive
			if i == 4 {
				tpl = &r2
				st = domain.AgentPaused
			}
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID: fmt.Sprintf("ag-mkt-%d", i), Slug: fmt.Sprintf("mkt-agent-%02d", i),
				Name: fmt.Sprintf("Alpha Market %d", i), TemplateSlug: tpl, TeamID: "tm-mkt",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
				ConfigRev: "rev", Status: st,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed agents: %v", err)
	}

	if bad := env.do(http.MethodGet, "/api/v1/agents?limit=101", ""); bad.Code != http.StatusBadRequest {
		t.Fatalf("limit=101 code = %d, want 400", bad.Code)
	}
	rec := env.do(http.MethodGet, "/api/v1/agents?team=marketing&template=researcher&status=active&q=Alpha&limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("listAgents status = %d: %s", rec.Code, rec.Body.String())
	}
	var page1 gen.AgentListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &page1)
	if len(page1.Items) != 2 || page1.NextCursor == nil {
		t.Fatalf("page1 = %+v, want 2 items + cursor", page1)
	}
	rec2 := env.do(http.MethodGet, "/api/v1/agents?team=marketing&template=researcher&status=active&q=Alpha&limit=2&cursor="+*page1.NextCursor, "")
	var page2 gen.AgentListResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &page2)
	if len(page2.Items) != 1 || page2.NextCursor != nil {
		t.Fatalf("page2 = %+v, want 1 item + nil cursor", page2)
	}
}

func TestListAgents10k(t *testing.T) {
	ctx := context.Background()
	env := newPhase2APIEnv(t)
	coreTeam, _, _ := env.st.Read().GetTeamBySlug(ctx, "core")
	if err := env.st.WithTx(ctx, func(tx app.Tx) error {
		for i := range 10000 {
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID: fmt.Sprintf("ag-bulk-%05d", i), Slug: fmt.Sprintf("worker-%05d", i),
				Name: fmt.Sprintf("Worker %05d", i), TeamID: coreTeam.ID,
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
				ConfigRev: "rev", Status: domain.AgentActive,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed 10k agents: %v", err)
	}

	plan, err := env.st.ExplainListAgentsPage(ctx, app.AgentFilter{Limit: 50, AfterSlug: "worker-00100"})
	if err != nil {
		t.Fatalf("ExplainListAgentsPage: %v", err)
	}
	joinedPlan := strings.ToUpper(strings.Join(plan, " | "))
	if !strings.Contains(joinedPlan, "INDEX") {
		t.Fatalf("query plan did not use index: %s", joinedPlan)
	}

	start := time.Now()
	rec := env.do(http.MethodGet, "/api/v1/agents?limit=50", "")
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("10k listAgents first page took %v, want < 200ms", elapsed)
	}
	var resp gen.AgentListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 50 || resp.NextCursor == nil {
		t.Fatalf("items=%d, nextCursor=%v", len(resp.Items), resp.NextCursor)
	}
}

func TestUpdateAgentWritesFile(t *testing.T) {
	ctx := context.Background()
	env := newPhase2APIEnv(t)
	chief, _, _ := env.st.Read().GetAgentBySlug(ctx, "chief-of-staff")

	emptyEng := env.do(http.MethodPatch, "/api/v1/agents/"+chief.ID, `{"engines":[]}`)
	if emptyEng.Code != http.StatusBadRequest {
		t.Fatalf("engines=[] code = %d, want 400", emptyEng.Code)
	}

	okRec := env.do(http.MethodPatch, "/api/v1/agents/"+chief.ID, `{"engines":["agy","claude"]}`)
	if okRec.Code != http.StatusOK {
		t.Fatalf("updateAgent code = %d: %s", okRec.Code, okRec.Body.String())
	}
	raw, err := env.vault.Read(ctx, "agents/chief-of-staff/AGENT.md")
	if err != nil || !strings.Contains(string(raw), "claude") {
		t.Fatalf("AGENT.md not updated: err=%v, raw=%s", err, string(raw))
	}
	logOut, err := exec.Command("git", "-C", env.vaultDir, "log", "-n", "1", "--pretty=%s").CombinedOutput()
	if err != nil || !strings.Contains(string(logOut), "chief-of-staff") {
		t.Fatalf("expected git commit for AGENT.md, got: %s (err=%v)", string(logOut), err)
	}
	updated, _, _ := env.st.Read().GetAgent(ctx, chief.ID)
	if !strings.Contains(updated.EnginesJSON, "claude") {
		t.Fatalf("synced DB engines = %s, want claude included", updated.EnginesJSON)
	}
}

func TestUpdateAgentRejectsUngated(t *testing.T) {
	ctx := context.Background()
	env := newPhase2APIEnv(t)
	supMD := "---\nname: Support Rep\nteam: core\nengines: [claude]\nsensitive: true\n---\nHandle inbound support.\n"
	_ = env.vault.Write(ctx, "agents/support-1/AGENT.md", []byte(supMD))
	_, _ = env.vault.CommitPaths(ctx, []string{"agents/support-1/AGENT.md"}, "add support-1")
	_, _, _ = env.syncSvc.SyncAll(ctx)
	sup, ok, _ := env.st.Read().GetAgentBySlug(ctx, "support-1")
	if !ok {
		t.Fatal("support-1 not synced")
	}

	rej := env.do(http.MethodPatch, "/api/v1/agents/"+sup.ID, `{"engines":["agy"]}`)
	if rej.Code != http.StatusConflict {
		t.Fatalf("ungated sensitive update code = %d, want 409 (%s)", rej.Code, rej.Body.String())
	}
	if !strings.Contains(rej.Body.String(), "sensitive") {
		t.Fatalf("expected reason mentioning sensitive role, got %s", rej.Body.String())
	}

	okUnlock := env.do(http.MethodPatch, "/api/v1/agents/"+sup.ID, `{"engines":["agy"],"allowUngatedEngine":true}`)
	if okUnlock.Code != http.StatusOK {
		t.Fatalf("unlocked update code = %d: %s", okUnlock.Code, okUnlock.Body.String())
	}
	logs, _ := env.st.Read().ListAuditLogs(ctx, 50)
	foundUnlock := false
	for _, l := range logs {
		if l.Action == "agent.unlock_engine" && l.Ref == sup.Slug {
			foundUnlock = true
		}
	}
	if !foundUnlock {
		t.Fatalf("expected audit_log entry agent.unlock_engine for %s, got %+v", sup.Slug, logs)
	}
}

func TestCreateAgentsBulk(t *testing.T) {
	env := newPhase2APIEnv(t)
	body := `{"template":"researcher","team":"core","slugPrefix":"res","rows":[{"topic":"AI"},{"topic":"Cloud"},{}]}`
	rec := env.do(http.MethodPost, "/api/v1/agents/bulk", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk code = %d: %s", rec.Code, rec.Body.String())
	}
	var resp gen.CreateAgentsBulkResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Created) != 2 || len(resp.Problems) != 1 {
		t.Fatalf("bulk resp = %+v, want 2 created + 1 problem", resp)
	}
}

func TestPauseResumeWake(t *testing.T) {
	ctx := context.Background()
	env := newPhase2APIEnv(t)
	chief, _, _ := env.st.Read().GetAgentBySlug(ctx, "chief-of-staff")

	if p := env.do(http.MethodPost, "/api/v1/agents/"+chief.ID+"/pause", ""); p.Code != http.StatusOK {
		t.Fatalf("pause code = %d: %s", p.Code, p.Body.String())
	}
	ag1, _, _ := env.st.Read().GetAgent(ctx, chief.ID)
	if ag1.Status != domain.AgentPaused {
		t.Fatalf("status after pause = %q, want paused", ag1.Status)
	}

	if r := env.do(http.MethodPost, "/api/v1/agents/"+chief.ID+"/resume", ""); r.Code != http.StatusOK {
		t.Fatalf("resume code = %d: %s", r.Code, r.Body.String())
	}
	ag2, _, _ := env.st.Read().GetAgent(ctx, chief.ID)
	if ag2.Status != domain.AgentActive {
		t.Fatalf("status after resume = %q, want active", ag2.Status)
	}

	w := env.do(http.MethodPost, "/api/v1/agents/"+chief.ID+"/wake", `{"message":"check status"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("wake code = %d: %s", w.Code, w.Body.String())
	}
	queued, _ := env.st.Read().ListQueuedWakeups(ctx, chief.ID)
	if len(queued) != 1 {
		t.Fatalf("queued wakeups = %d, want 1", len(queued))
	}
}

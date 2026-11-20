package agentsync_test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	runapp "max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func setupTemplateVault(t *testing.T) (context.Context, app.Store, *vaultgit.Vault, *agentsync.Service, string) {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(vaultDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	tplRaw := []byte(`---
name: Chuyên viên theo dõi
autonomy: auto
web: true
browser: anon
skills: [browse-web]
required_params: [target_url, topic]
---
Theo dõi nguồn {{target_url}} về chủ đề {{topic}} và báo cáo ngắn gọn.
`)
	if err := v.Write(ctx, "templates/monitor/TEMPLATE.md", tplRaw); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if _, err := v.CommitPaths(ctx, []string{"templates/monitor/TEMPLATE.md"}, "vault: seed monitor template"); err != nil {
		t.Fatalf("commit template: %v", err)
	}

	svc := agentsync.NewService(st, v, app.SystemClock{}, app.UUIDv7Gen{})
	return ctx, st, v, svc, vaultDir
}

func TestInstantiateWritesFile(t *testing.T) {
	ctx, st, v, svc, _ := setupTemplateVault(t)

	err := svc.Instantiate(ctx, "monitor", "mon-vn", "marketing", map[string]any{
		"target_url": "https://example.com/news",
		"topic":      "fintech",
	})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}

	raw, err := v.Read(ctx, "agents/mon-vn/AGENT.md")
	if err != nil {
		t.Fatalf("read instantiated AGENT.md: %v", err)
	}
	af, err := agentsync.Parse(raw)
	if err != nil {
		t.Fatalf("Parse instantiated AGENT.md: %v", err)
	}
	if len(af.Engines) != 1 || af.Engines[0] != domain.EngineAgy {
		t.Fatalf("expected default engines [agy], got %v", af.Engines)
	}
	if !af.Web || af.Browser != domain.BrowserAnon {
		t.Fatalf("expected inherited web=true browser=anon, got web=%v browser=%q", af.Web, af.Browser)
	}
	if !strings.Contains(af.Body, "https://example.com/news") || !strings.Contains(af.Body, "fintech") {
		t.Fatalf("expected params substituted into body, got: %s", af.Body)
	}

	ag, ok, _ := st.Read().GetAgentBySlug(ctx, "mon-vn")
	if !ok {
		t.Fatal("expected mon-vn synced into SQLite agents table")
	}
	if ag.TemplateSlug == nil || *ag.TemplateSlug != "monitor" {
		t.Fatalf("template_slug=%v, want monitor", ag.TemplateSlug)
	}
}

func TestInstantiateMissingParam(t *testing.T) {
	ctx, _, _, svc, _ := setupTemplateVault(t)
	err := svc.Instantiate(ctx, "monitor", "mon-bad", "marketing", map[string]any{
		"target_url": "https://example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "topic") {
		t.Fatalf("expected missing param error mentioning 'topic', got: %v", err)
	}
}

func TestInstantiateSlugCollision(t *testing.T) {
	ctx, _, _, svc, _ := setupTemplateVault(t)
	params := map[string]any{"target_url": "https://example.com", "topic": "ai-ops"}
	if err := svc.Instantiate(ctx, "monitor", "mon-dup", "marketing", params); err != nil {
		t.Fatal(err)
	}
	if err := svc.Instantiate(ctx, "monitor", "mon-dup", "marketing", params); err == nil {
		t.Fatal("expected slug collision error on duplicate slug")
	}
}

func TestBulk(t *testing.T) {
	ctx, st, _, svc, vaultDir := setupTemplateVault(t)

	commitsBefore := countGitCommits(t, vaultDir)
	rows := make([]map[string]any, 500)
	for i := range 500 {
		if i == 250 {
			rows[i] = map[string]any{"target_url": "https://example.com/missing-topic"} // missing required 'topic'
			continue
		}
		rows[i] = map[string]any{
			"target_url": fmt.Sprintf("https://example.com/%d", i),
			"topic":      fmt.Sprintf("sector-%d", i),
		}
	}

	created, problems, err := svc.InstantiateBulk(ctx, "monitor", "ops", "watcher", rows)
	if err != nil {
		t.Fatalf("InstantiateBulk error: %v", err)
	}
	if len(created) != 499 || len(problems) != 1 {
		t.Fatalf("got created=%d problems=%d, want 499/1", len(created), len(problems))
	}

	commitsAfter := countGitCommits(t, vaultDir)
	if commitsAfter-commitsBefore != 1 {
		t.Fatalf("expected exactly 1 git commit for bulk instantiate, got delta=%d", commitsAfter-commitsBefore)
	}

	opsTeam, ok, _ := st.Read().GetTeamBySlug(ctx, "ops")
	if !ok {
		t.Fatal("expected ops team synced")
	}
	opsAgents, err := st.Read().ListTeamAgents(ctx, opsTeam.ID)
	if err != nil || len(opsAgents) != 499 {
		t.Fatalf("expected 499 ops agents in SQLite, got %d (err=%v)", len(opsAgents), err)
	}
}

func TestTeamCapApplied(t *testing.T) {
	ctx, st, v, svc, _ := setupTemplateVault(t)
	teamRaw := []byte(`---
name: Đội Nghiên cứu
mandate:
  outcome: Cung cấp dữ liệu thị trường chính xác
  measures: [tốc độ cập nhật, độ chính xác trích dẫn]
max_running: 1
quota_tokens_day: 250000
---
Quy ước làm việc của đội.
`)
	if err := v.Write(ctx, "teams/research/TEAM.md", teamRaw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SyncAll(ctx); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	tm, ok, err := st.Read().GetTeamBySlug(ctx, "research")
	if err != nil || !ok {
		t.Fatalf("GetTeamBySlug: ok=%v err=%v", ok, err)
	}
	if tm.MaxRunning != 1 || tm.QuotaTokensDay == nil || *tm.QuotaTokensDay != 250000 {
		t.Fatalf("team = %+v, want MaxRunning=1 QuotaTokensDay=250000", tm)
	}
}

type blockingEngine struct {
	started chan app.RunSpec
	unblock chan struct{}
}

func (e *blockingEngine) Name() domain.EngineName { return domain.EngineAgy }
func (e *blockingEngine) Caps() app.EngineCaps    { return app.EngineCaps{PerRunMCP: true, Resume: true} }
func (e *blockingEngine) Health(context.Context) app.EngineHealth {
	return app.EngineHealth{Installed: true, LoggedIn: true, Version: "test"}
}
func (e *blockingEngine) Run(ctx context.Context, spec app.RunSpec, _ app.PermissionFunc) (<-chan app.EngineEvent, error) {
	e.started <- spec
	ch := make(chan app.EngineEvent, 2)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
		case <-e.unblock:
		}
		ch <- app.EngineEvent{Type: "final", Text: "ok"}
	}()
	return ch, nil
}

type snapshotPrompts struct{ vault app.Vault }

func (p snapshotPrompts) System(ctx context.Context, _ domain.ID) (string, error) {
	raw, _ := p.vault.Read(ctx, "agents/mon-live/AGENT.md")
	return string(raw), nil
}
func (p snapshotPrompts) RunMessage(_ context.Context, _ app.Claimed) (string, error) {
	return "go", nil
}

func TestSyncUsesSnapshot(t *testing.T) {
	ctx, st, v, svc, _ := setupTemplateVault(t)
	if err := svc.Instantiate(ctx, "monitor", "mon-live", "core", map[string]any{
		"target_url": "https://v1.example.com",
		"topic":      "version-1",
	}); err != nil {
		t.Fatal(err)
	}
	ag1, _, _ := st.Read().GetAgentBySlug(ctx, "mon-live")

	actors := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	eng := &blockingEngine{started: make(chan app.RunSpec, 2), unblock: make(chan struct{})}
	cd := 0 * 1
	_ = cd
	zeroDur := app.SystemClock{}.Now().Sub(app.SystemClock{}.Now())
	runSvc := runapp.NewService(runapp.Deps{
		Store:    st,
		Actors:   actors,
		Engines:  map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Prompts:  snapshotPrompts{vault: v},
		Cooldown: &zeroDur,
	})

	// Wake and claim Run 1
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{AgentID: ag1.ID, Kind: domain.WakeMessage, Priority: domain.PrioInteractive})
		return err
	})
	claimed1, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, app.SystemClock{}.Now())
	if err != nil || len(claimed1) != 1 {
		t.Fatalf("ClaimBatch 1: %v %+v", err, claimed1)
	}

	done1 := make(chan error, 1)
	go func() {
		_, err := runSvc.Execute(ctx, claimed1[0])
		done1 <- err
	}()

	spec1 := <-eng.started
	if !strings.Contains(spec1.SystemPrompt, "version-1") {
		t.Fatalf("Run 1 SystemPrompt missing version-1: %s", spec1.SystemPrompt)
	}

	// Modify AGENT.md in vault and SyncAll WHILE Run 1 is mid-flight!
	updatedMD := strings.ReplaceAll(spec1.SystemPrompt, "version-1", "version-2")
	if err := v.Write(ctx, "agents/mon-live/AGENT.md", []byte(updatedMD)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	ag2, _, _ := st.Read().GetAgentBySlug(ctx, "mon-live")
	if ag2.ConfigRev == ag1.ConfigRev {
		t.Fatal("expected ConfigRev to change after AGENT.md edit + SyncAll")
	}

	// Let Run 1 finish; verify its recorded config_rev is still ag1.ConfigRev
	eng.unblock <- struct{}{}
	if err := <-done1; err != nil {
		t.Fatalf("Run 1 Execute: %v", err)
	}
	r1, _, _ := st.Read().GetRun(ctx, claimed1[0].RunID)
	if r1.ConfigRev != ag1.ConfigRev {
		t.Fatalf("Run 1 config_rev=%q, want snapshot %q", r1.ConfigRev, ag1.ConfigRev)
	}

	// Wake and run Run 2; verify it uses ag2.ConfigRev and version-2 prompt
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{AgentID: ag1.ID, Kind: domain.WakeMessage, Priority: domain.PrioInteractive})
		return err
	})
	claimed2, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, app.SystemClock{}.Now())
	if err != nil || len(claimed2) != 1 {
		t.Fatalf("ClaimBatch 2: %v %+v", err, claimed2)
	}
	go func() { eng.unblock <- struct{}{} }()
	if _, err := runSvc.Execute(ctx, claimed2[0]); err != nil {
		t.Fatalf("Run 2 Execute: %v", err)
	}
	spec2 := <-eng.started
	if !strings.Contains(spec2.SystemPrompt, "version-2") {
		t.Fatalf("Run 2 SystemPrompt missing version-2: %s", spec2.SystemPrompt)
	}
	r2, _, _ := st.Read().GetRun(ctx, claimed2[0].RunID)
	if r2.ConfigRev != ag2.ConfigRev {
		t.Fatalf("Run 2 config_rev=%q, want new %q", r2.ConfigRev, ag2.ConfigRev)
	}
}

func countGitCommits(t *testing.T, dir string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-list: %v (%s)", err, string(out))
	}
	var n int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

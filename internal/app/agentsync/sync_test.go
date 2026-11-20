package agentsync_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/agentsync"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func TestParseExample(t *testing.T) {
	raw := []byte(`---
name: Nghiên cứu thị trường
template: researcher
team: marketing
manager: chief-of-staff
engines: [agy]
models: { agy: default }
autonomy: auto
web: true
browser: anon
skills: [browse-web, ingest-source]
quota_tokens_day: 400000
heartbeat: "0 7 * * 1"
params: { focus: "thị trường Việt Nam" }
---
Chỉ dẫn vai trò...
`)
	af, err := agentsync.Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if af.Name != "Nghiên cứu thị trường" || af.Template != "researcher" || af.Team != "marketing" || af.Manager != "chief-of-staff" {
		t.Fatalf("basic fields mismatch: %+v", af)
	}
	if len(af.Engines) != 1 || af.Engines[0] != domain.EngineAgy {
		t.Fatalf("Engines = %v, want [agy]", af.Engines)
	}
	if af.Models[domain.EngineAgy] != "default" {
		t.Fatalf("Models = %v", af.Models)
	}
	if af.Autonomy != domain.AutonomyAuto || !af.Web || af.Browser != domain.BrowserAnon {
		t.Fatalf("capabilities mismatch: autonomy=%q web=%v browser=%q", af.Autonomy, af.Web, af.Browser)
	}
	if len(af.Skills) != 2 || af.Skills[0] != "browse-web" || af.Skills[1] != "ingest-source" {
		t.Fatalf("Skills = %v", af.Skills)
	}
	if af.QuotaTokensDay == nil || *af.QuotaTokensDay != 400000 {
		t.Fatalf("QuotaTokensDay = %v, want 400000", af.QuotaTokensDay)
	}
	if af.Heartbeat != "0 7 * * 1" {
		t.Fatalf("Heartbeat = %q", af.Heartbeat)
	}
	if af.Params["focus"] != "thị trường Việt Nam" {
		t.Fatalf("Params = %v", af.Params)
	}
	if strings.TrimSpace(af.Body) != "Chỉ dẫn vai trò..." {
		t.Fatalf("Body = %q", af.Body)
	}

	// Verify missing engines defaults to [agy].
	minimal := []byte("---\nname: Minimal\nteam: core\n---\nHello\n")
	afMin, err := agentsync.Parse(minimal)
	if err != nil {
		t.Fatalf("Parse minimal: %v", err)
	}
	if len(afMin.Engines) != 1 || afMin.Engines[0] != domain.EngineAgy {
		t.Fatalf("default Engines = %v, want [agy]", afMin.Engines)
	}
}

func TestParseRejects(t *testing.T) {
	errCases := []struct {
		name      string
		raw       []byte
		wantField string
	}{
		{
			name:      "missing closing fence",
			raw:       []byte("---\nname: Bad\nteam: core\nbody without fence"),
			wantField: "frontmatter",
		},
		{
			name:      "invalid yaml",
			raw:       []byte("---\nname: [unclosed\nteam: core\n---\nBody"),
			wantField: "yaml",
		},
		{
			name:      "invalid autonomy",
			raw:       []byte("---\nname: A\nteam: core\nautonomy: full\n---\nBody"),
			wantField: "autonomy",
		},
		{
			name:      "invalid engine",
			raw:       []byte("---\nname: A\nteam: core\nengines: [gpt]\n---\nBody"),
			wantField: "engines",
		},
		{
			name:      "non-utf8 bytes",
			raw:       append([]byte("---\nname: A\nteam: core\n---\n"), 0xff, 0xfe),
			wantField: "utf-8",
		},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentsync.Parse(tc.raw)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.wantField) {
				t.Fatalf("Parse(%s) err = %v, want error mentioning %q", tc.name, err, tc.wantField)
			}
		})
	}

	// Body > 200 lines produces a warning (not an error).
	lines := make([]string, 205)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	longBody := []byte("---\nname: Long\nteam: core\n---\n" + strings.Join(lines, "\n"))
	af, err := agentsync.Parse(longBody)
	if err != nil {
		t.Fatalf("expected warning rather than error for >200 body lines, got %v", err)
	}
	if len(af.Warnings) == 0 || !strings.Contains(strings.ToLower(af.Warnings[0]), "body") {
		t.Fatalf("expected warning mentioning body line cap, got %v", af.Warnings)
	}
}

func writeAgentMD(t *testing.T, vaultDir, slug, content string) {
	t.Helper()
	p := filepath.Join(vaultDir, "agents", slug, "AGENT.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write AGENT.md: %v", err)
	}
}

func TestSyncKeepsOldOnError(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(vaultDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	svc := agentsync.NewService(st, v, app.SystemClock{}, app.UUIDv7Gen{})
	upd1, probs1, err := svc.SyncAll(ctx)
	if err != nil || len(probs1) != 0 || upd1 != 1 {
		t.Fatalf("initial SyncAll = (upd=%d, probs=%+v, err=%v)", upd1, probs1, err)
	}
	oldChief, ok, _ := st.Read().GetAgentBySlug(ctx, "chief-of-staff")
	if !ok {
		t.Fatal("chief-of-staff missing after initial sync")
	}

	// Corrupt chief-of-staff/AGENT.md while adding a healthy second agent.
	writeAgentMD(t, vaultDir, "chief-of-staff", "---\nname: Broken\nteam: core\nautonomy: full\n---\nBad")
	writeAgentMD(t, vaultDir, "writer-1", "---\nname: Writer 1\nteam: core\nengines: [agy]\n---\nWrite docs.")

	upd2, probs2, err := svc.SyncAll(ctx)
	if err != nil {
		t.Fatalf("second SyncAll error: %v", err)
	}
	if upd2 != 1 || len(probs2) != 1 || probs2[0].Slug != "chief-of-staff" {
		t.Fatalf("SyncAll = (upd=%d, probs=%+v), want upd=1 and 1 problem on chief-of-staff", upd2, probs2)
	}

	afterChief, _, _ := st.Read().GetAgentBySlug(ctx, "chief-of-staff")
	if afterChief.ConfigRev != oldChief.ConfigRev || afterChief.Name != oldChief.Name {
		t.Fatalf("broken AGENT.md overwrote previous config: got %+v, want %+v", afterChief, oldChief)
	}
	if _, ok, _ := st.Read().GetAgentBySlug(ctx, "writer-1"); !ok {
		t.Fatal("healthy sibling writer-1 should still have synced")
	}
	inbox, _ := st.Read().ListInboxItems(ctx, 10)
	if len(inbox) == 0 {
		t.Fatal("expected Inbox item reporting broken AGENT.md")
	}
}

func TestSyncSetsMask(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(vaultDir)
	writeAgentMD(t, vaultDir, "multi-eng", "---\nname: Multi\nteam: core\nengines: [agy, claude]\n---\nBody")

	svc := agentsync.NewService(st, v, app.SystemClock{}, app.UUIDv7Gen{})
	upd, probs, err := svc.SyncAll(ctx)
	if err != nil || len(probs) != 0 || upd != 1 {
		t.Fatalf("SyncAll = (%d, %+v, %v)", upd, probs, err)
	}
	ag, ok, _ := st.Read().GetAgentBySlug(ctx, "multi-eng")
	if !ok || ag.EngineMask != 5 {
		t.Fatalf("ag = (%+v, %v), want EngineMask=5", ag, ok)
	}
}

func TestSyncIdempotent(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(vaultDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	svc := agentsync.NewService(st, v, app.SystemClock{}, app.UUIDv7Gen{})
	upd1, _, err := svc.SyncAll(ctx)
	if err != nil || upd1 != 1 {
		t.Fatalf("first SyncAll = (%d, %v), want (1, nil)", upd1, err)
	}
	upd2, probs2, err := svc.SyncAll(ctx)
	if err != nil || len(probs2) != 0 || upd2 != 0 {
		t.Fatalf("second SyncAll = (upd=%d, probs=%+v, err=%v), want upd=0", upd2, probs2, err)
	}
}

func TestRejectsUngatedSensitive(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(vaultDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	svc := agentsync.NewService(st, v, app.SystemClock{}, app.UUIDv7Gen{})
	if _, _, err := svc.SyncAll(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	before, _, _ := st.Read().GetAgentBySlug(ctx, "chief-of-staff")

	// Update chief-of-staff to browser: owner on agy (level 3) without allow_ungated_engine: true.
	writeAgentMD(t, vaultDir, "chief-of-staff", `---
name: Chánh văn phòng
team: core
engines: [agy]
browser: owner
allow_ungated_engine: false
---
Role body.
`)

	upd, probs, err := svc.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	if upd != 0 || len(probs) != 1 {
		t.Fatalf("expected 1 Problem rejecting ungated owner browser on agy, got upd=%d probs=%+v", upd, probs)
	}
	after, _, _ := st.Read().GetAgentBySlug(ctx, "chief-of-staff")
	if after.Browser != before.Browser || after.ConfigRev != before.ConfigRev {
		t.Fatalf("expected old config preserved on ungated rejection: got %+v", after)
	}
}

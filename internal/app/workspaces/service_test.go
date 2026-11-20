package workspaces_test

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"max/internal/adapter/engine"
	"max/internal/app"
	"max/internal/app/workspaces"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func seedDeveloperAgent(t *testing.T, st app.Store, enginesJSON string, level int, allowUngated bool) domain.ID {
	t.Helper()
	ctx := context.Background()
	tpl := "developer"
	agID := domain.ID("ag-dev-" + strings.Trim(enginesJSON, `[]" `))
	err := st.WithTx(ctx, func(tx app.Tx) error {
		tmID, err := tx.EnsureTeamBySlug(ctx, "core", "t-core")
		if err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID:                 agID,
			Slug:               "dev-" + strings.Trim(enginesJSON, `[]" `),
			Name:               "Dev",
			TemplateSlug:       &tpl,
			TeamID:             tmID,
			EnginesJSON:        enginesJSON,
			EngineMask:         4,
			AllowUngatedEngine: allowUngated,
			Autonomy:           domain.AutonomyAuto,
			Browser:            domain.BrowserNone,
			Shell:              true,
			ConfigRev:          "r1",
			Status:             domain.AgentActive,
		})
	})
	if err != nil {
		t.Fatalf("seedDeveloperAgent: %v", err)
	}
	_ = level
	return agID
}

func TestDeveloperCwdOutsideVault(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	_ = os.MkdirAll(vaultDir, 0o755)

	// Create a local bare git repository to clone from without touching network.
	srcRepo := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "init", "--bare", srcRepo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v (%s)", err, string(out))
	}

	agID := seedDeveloperAgent(t, st, `["claude"]`, 1, false)
	svc := workspaces.NewService(workspaces.Config{
		Store:        st,
		DataDir:      dataDir,
		VaultDir:     vaultDir,
		EngineLevels: map[domain.EngineName]int{domain.EngineClaude: 1, domain.EngineAgy: 3},
	})

	dir, err := svc.Prepare(ctx, agID, srcRepo)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	cleanDir := filepath.ToSlash(filepath.Clean(dir))
	cleanVault := filepath.ToSlash(filepath.Clean(vaultDir))
	if strings.HasPrefix(cleanDir, cleanVault+"/") || cleanDir == cleanVault {
		t.Fatalf("developer cwd %q is inside vault %q", cleanDir, cleanVault)
	}
	wantPrefix := filepath.ToSlash(filepath.Join(dataDir, "workspaces")) + "/"
	if !strings.HasPrefix(cleanDir, wantPrefix) {
		t.Fatalf("developer cwd = %q, want under %q", cleanDir, wantPrefix)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("expected git workspace initialized at %q: %v", dir, err)
	}
}

func TestDeveloperRefusedOnLevel3Engine(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")

	agL3 := seedDeveloperAgent(t, st, `["agy"]`, 3, false)
	svc := workspaces.NewService(workspaces.Config{
		Store:        st,
		DataDir:      dataDir,
		VaultDir:     vaultDir,
		EngineLevels: map[domain.EngineName]int{domain.EngineClaude: 1, domain.EngineAgy: 3},
	})

	if _, err := svc.Prepare(ctx, agL3, ""); err == nil {
		t.Fatal("expected developer agent on Level-3 engine to be refused without Level-1 engine")
	}
}

func TestDeveloperRunsAsSeparateOSUser(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")

	// Prepare sensitive files owned by max with 0600 / 0700 permissions.
	dbFile := filepath.Join(dataDir, "max.sqlite3")
	otherHome := filepath.Join(dataDir, "agent-home", "agy")
	_ = os.MkdirAll(otherHome, 0o700)
	_ = os.WriteFile(dbFile, []byte("sqlite-secret"), 0o600)
	_ = os.WriteFile(filepath.Join(otherHome, "token.json"), []byte("oauth-token"), 0o600)

	agID := seedDeveloperAgent(t, st, `["claude"]`, 1, false)
	svc := workspaces.NewService(workspaces.Config{
		Store:        st,
		DataDir:      dataDir,
		VaultDir:     vaultDir,
		DevOSUser:    "max-dev",
		EngineLevels: map[domain.EngineName]int{domain.EngineClaude: 1},
	})

	if err := svc.VerifyHostIsolation(); err != nil {
		t.Fatalf("VerifyHostIsolation: %v", err)
	}
	dir, err := svc.Prepare(ctx, agID, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	procSpec := engine.RunAs(engine.ProcSpec{Bin: "claude", Dir: dir}, svc.OSUser())
	if procSpec.OSUser != "max-dev" {
		t.Fatalf("OSUser = %q, want max-dev", procSpec.OSUser)
	}
	if cur, uErr := user.Current(); uErr == nil && cur.Username == procSpec.OSUser {
		t.Fatalf("developer OSUser %q must differ from daemon user %q", procSpec.OSUser, cur.Username)
	}
	if runtime.GOOS != "windows" {
		fiDB, _ := os.Stat(dbFile)
		if fiDB.Mode().Perm()&0o077 != 0 {
			t.Fatalf("max.sqlite3 permissions %o allow group/other read", fiDB.Mode().Perm())
		}
		fiHome, _ := os.Stat(otherHome)
		if fiHome.Mode().Perm()&0o077 != 0 {
			t.Fatalf("agent-home/agy permissions %o allow group/other access", fiHome.Mode().Perm())
		}
	}
}

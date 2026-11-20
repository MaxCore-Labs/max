package scopeaudit_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"max/internal/adapter/scopeaudit"
	"max/internal/domain"
)

func initTestVaultRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("company/COMPANY.md", "# Acme Corp\nOriginal company handbook.\n")
	write("agents/a/AGENT.md", "---\nname: Agent A\n---\nRole A.\n")
	write("agents/a/notes/x.md", "initial note\n")
	write("agents/b/AGENT.md", "---\nname: Agent B\n---\nRole B.\n")

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit("init")
	runGit("config", "user.name", "Max")
	runGit("config", "user.email", "max@localhost")
	runGit("add", ".")
	runGit("commit", "-m", "initial")

	return dir
}

func scopeA() domain.AgentScope {
	return domain.AgentScope{Slug: "a", TeamSlug: "core"}
}

func TestInScopeUntouched(t *testing.T) {
	ctx := context.Background()
	vault := initTestVaultRepo(t)
	quar := t.TempDir()

	p := filepath.Join(vault, "agents", "a", "notes", "x.md")
	if err := os.WriteFile(p, []byte("updated note\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	touched, violations, err := scopeaudit.Audit(ctx, vault, quar, scopeA())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("unexpected violations: %+v", violations)
	}
	if len(touched) != 1 || touched[0] != "agents/a/notes/x.md" {
		t.Fatalf("touched = %v, want [agents/a/notes/x.md]", touched)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "updated note\n" {
		t.Fatalf("file content = %q, want updated note", string(got))
	}
}

func TestOutOfScopeModifiedReverted(t *testing.T) {
	ctx := context.Background()
	vault := initTestVaultRepo(t)
	quar := t.TempDir()

	comp := filepath.Join(vault, "company", "COMPANY.md")
	if err := os.WriteFile(comp, []byte("TAMPERED\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	touched, violations, err := scopeaudit.Audit(ctx, vault, quar, scopeA())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(touched) != 0 {
		t.Fatalf("touched = %v, want empty", touched)
	}
	if len(violations) != 1 || violations[0].Rel != "company/COMPANY.md" || !violations[0].Reverted {
		t.Fatalf("violations = %+v, want 1 reverted violation on company/COMPANY.md", violations)
	}
	restored, _ := os.ReadFile(comp)
	if !strings.Contains(string(restored), "Original company handbook.") {
		t.Fatalf("COMPANY.md was not reverted: %q", string(restored))
	}
}

func TestOutOfScopeNewFileRemoved(t *testing.T) {
	ctx := context.Background()
	vault := initTestVaultRepo(t)
	quar := t.TempDir()

	rogue := filepath.Join(vault, "agents", "b", "notes", "y.md")
	if err := os.MkdirAll(filepath.Dir(rogue), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(rogue, []byte("intrusion\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	touched, violations, err := scopeaudit.Audit(ctx, vault, quar, scopeA())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(touched) != 0 || len(violations) != 1 {
		t.Fatalf("touched=%v violations=%+v", touched, violations)
	}
	if _, err := os.Stat(rogue); !os.IsNotExist(err) {
		t.Fatalf("expected rogue file to be removed, stat err=%v", err)
	}
}

func TestSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	vault := initTestVaultRepo(t)
	quar := t.TempDir()

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("do-not-delete-me"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	linkPath := filepath.Join(vault, "agents", "a", "notes", "link")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("os.Symlink unavailable without privilege on this host: %v", err)
	}
	if err := os.WriteFile(linkPath, []byte("written-via-symlink"), 0o600); err != nil {
		t.Fatalf("write through symlink: %v", err)
	}

	touched, violations, err := scopeaudit.Audit(ctx, vault, quar, scopeA())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(touched) != 0 || len(violations) != 1 {
		t.Fatalf("touched=%v violations=%+v", touched, violations)
	}
	if !strings.Contains(violations[0].Reason, outsideFile) && !strings.Contains(violations[0].Reason, "outside.txt") {
		t.Fatalf("expected violation Reason to mention outside target %q, got %q", outsideFile, violations[0].Reason)
	}
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("expected symlink inside vault to be removed, lstat err=%v", err)
	}
	outsideContent, err := os.ReadFile(outsideFile)
	if err != nil || string(outsideContent) != "written-via-symlink" {
		t.Fatalf("outside file must not be deleted or altered by Max: err=%v content=%q", err, string(outsideContent))
	}
}

func TestBinaryOutsideArtifacts(t *testing.T) {
	ctx := context.Background()
	vault := initTestVaultRepo(t)
	quar := t.TempDir()

	binBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02, 0xff}
	badBin := filepath.Join(vault, "agents", "a", "notes", "blob.bin")
	okBin := filepath.Join(vault, "agents", "a", "artifacts", "chart.png")
	if err := os.MkdirAll(filepath.Dir(okBin), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(badBin, binBytes, 0o644); err != nil {
		t.Fatalf("write badBin: %v", err)
	}
	if err := os.WriteFile(okBin, binBytes, 0o644); err != nil {
		t.Fatalf("write okBin: %v", err)
	}

	touched, violations, err := scopeaudit.Audit(ctx, vault, quar, scopeA())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(violations) != 1 || violations[0].Rel != "agents/a/notes/blob.bin" {
		t.Fatalf("violations = %+v, want 1 on agents/a/notes/blob.bin", violations)
	}
	if len(touched) != 1 || touched[0] != "agents/a/artifacts/chart.png" {
		t.Fatalf("touched = %v, want [agents/a/artifacts/chart.png]", touched)
	}
}

func TestOversizeFile(t *testing.T) {
	ctx := context.Background()
	vault := initTestVaultRepo(t)
	quar := t.TempDir()

	big := filepath.Join(vault, "agents", "a", "artifacts", "huge.txt")
	if err := os.MkdirAll(filepath.Dir(big), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(big, bytes.Repeat([]byte("a"), 6*1024*1024), 0o644); err != nil {
		t.Fatalf("write 6MB: %v", err)
	}

	touched, violations, err := scopeaudit.Audit(ctx, vault, quar, scopeA())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(touched) != 0 || len(violations) != 1 {
		t.Fatalf("touched=%v violations=%+v", touched, violations)
	}
	if !strings.Contains(violations[0].Reason, "quá 5 MB") {
		t.Fatalf("Reason = %q, want mention of 'quá 5 MB'", violations[0].Reason)
	}
	if _, err := os.Stat(big); !os.IsNotExist(err) {
		t.Fatal("expected 6 MB file to be removed")
	}
}

func TestSecretInFileQuarantined(t *testing.T) {
	ctx := context.Background()
	vault := initTestVaultRepo(t)
	quar := filepath.Join(t.TempDir(), "quarantine", "run-1")

	pem := "-----BEGIN " + "PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcw\n-----END " + "PRIVATE KEY-----\n"
	leak1 := filepath.Join(vault, "agents", "a", "notes", "api.md")
	leak2 := filepath.Join(vault, "agents", "a", "notes", "pem.md")
	leak3 := filepath.Join(vault, "agents", "a", "notes", "url.md")
	clean := filepath.Join(vault, "agents", "a", "notes", "clean.md")

	_ = os.WriteFile(leak1, []byte("token: "+"sk-ant-"+"api03-secret1234567890\n"), 0o644)
	_ = os.WriteFile(leak2, []byte(pem), 0o644)
	_ = os.WriteFile(leak3, []byte("repo https://deploy:superpass@git.example.com/org/repo\n"), 0o644)
	_ = os.WriteFile(clean, []byte("pure public summary\n"), 0o644)

	touched, violations, err := scopeaudit.Audit(ctx, vault, quar, scopeA())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(touched) != 1 || touched[0] != "agents/a/notes/clean.md" {
		t.Fatalf("touched = %v, want only [agents/a/notes/clean.md]", touched)
	}
	if len(violations) != 3 {
		t.Fatalf("len(violations) = %d (%+v), want 3", len(violations), violations)
	}
	for _, v := range violations {
		if !strings.Contains(v.Reason, "chứa secret") {
			t.Fatalf("Reason = %q, want 'chứa secret'", v.Reason)
		}
		qPath := filepath.Join(quar, filepath.FromSlash(v.Rel))
		if _, err := os.Stat(qPath); err != nil {
			t.Fatalf("expected quarantined copy at %s: %v", qPath, err)
		}
		vPath := filepath.Join(vault, filepath.FromSlash(v.Rel))
		if _, err := os.Stat(vPath); !os.IsNotExist(err) {
			t.Fatalf("expected secret file removed from vault: %s", vPath)
		}
	}
}

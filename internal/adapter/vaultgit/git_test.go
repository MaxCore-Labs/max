package vaultgit_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"max/internal/adapter/vaultgit"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestInstallOnEmptyDir(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(dir)

	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	count := gitOut(t, dir, "rev-list", "--count", "HEAD")
	if count != "1" {
		t.Fatalf("commit count = %q, want 1", count)
	}
	raw, err := v.Read(ctx, "agents/chief-of-staff/AGENT.md")
	if err != nil || !strings.Contains(string(raw), "Chánh văn phòng") {
		t.Fatalf("Read chief-of-staff/AGENT.md: err=%v content=%q", err, string(raw))
	}
}

func TestInstallSkipsNonEmpty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	existing := filepath.Join(dir, "custom.md")
	if err := os.WriteFile(existing, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	v := vaultgit.New(dir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled non-empty: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agents", "chief-of-staff", "AGENT.md")); !os.IsNotExist(err) {
		t.Fatal("expected EnsureInstalled to skip non-empty directory")
	}
}

func TestCommitPathsOnlyThose(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(dir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	_ = os.WriteFile(filepath.Join(dir, "wiki", "a.md"), []byte("doc A\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "wiki", "b.md"), []byte("doc B\n"), 0o644)

	sha, err := v.CommitPaths(ctx, []string{"wiki/a.md"}, "run(chief-of-staff): add a")
	if err != nil || sha == "" {
		t.Fatalf("CommitPaths = (%q, %v)", sha, err)
	}

	status := gitOut(t, dir, "status", "--porcelain=v1")
	if strings.Contains(status, "wiki/a.md") {
		t.Fatalf("expected wiki/a.md clean after commit, status:\n%s", status)
	}
	if !strings.Contains(status, "wiki/b.md") {
		t.Fatalf("expected wiki/b.md to remain dirty, status:\n%s", status)
	}
}

func TestCommitMessageNoAIAttribution(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(dir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	_ = os.WriteFile(filepath.Join(dir, "wiki", "note.md"), []byte("clean\n"), 0o644)
	dirtyMsg := "run(writer): draft\n\nCo-Authored-By: " + "Cla" + "ude <noreply@example.com>"
	sha, err := v.CommitPaths(ctx, []string{"wiki/note.md"}, dirtyMsg)
	if err != nil || sha == "" {
		t.Fatalf("CommitPaths: sha=%q err=%v", sha, err)
	}

	author := gitOut(t, dir, "log", "-1", "--format=%an <%ae>")
	if author != "Max <max@localhost>" {
		t.Fatalf("author = %q, want 'Max <max@localhost>'", author)
	}
	fullMsg := strings.ToLower(gitOut(t, dir, "log", "-1", "--format=%B%n%an%n%ae%n%cn%n%ce"))
	for _, forbidden := range []string{"cla" + "ude", "anthro" + "pic", "gem" + "ini", "open" + "ai", "co-authored" + "-by"} {
		if strings.Contains(fullMsg, forbidden) {
			t.Fatalf("commit leaked forbidden token %q:\n%s", forbidden, fullMsg)
		}
	}
}

func TestPullSkippedWhenRunsActive(t *testing.T) {
	ctx := context.Background()
	// Point at a non-git directory: when runsActive > 0, PullIfIdle must return nil without invoking git!
	v := vaultgit.New(t.TempDir())
	if err := v.PullIfIdle(ctx, 1); err != nil {
		t.Fatalf("expected PullIfIdle(runsActive=1) to skip git, got %v", err)
	}
}

func TestPullConflictGoesToBranch(t *testing.T) {
	ctx := context.Background()
	remoteBare := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remoteBare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, string(out))
	}

	localDir := filepath.Join(t.TempDir(), "local")
	v := vaultgit.New(localDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	branch := gitOut(t, localDir, "rev-parse", "--abbrev-ref", "HEAD")
	gitOut(t, localDir, "remote", "add", "origin", remoteBare)
	gitOut(t, localDir, "push", "-u", "origin", branch)

	// Clone peer, modify company/COMPANY.md, and push to remote.
	peerDir := filepath.Join(t.TempDir(), "peer")
	if out, err := exec.Command("git", "clone", remoteBare, peerDir).CombinedOutput(); err != nil {
		t.Fatalf("clone peer: %v\n%s", err, string(out))
	}
	gitOut(t, peerDir, "config", "user.name", "Owner")
	gitOut(t, peerDir, "config", "user.email", "owner@localhost")
	_ = os.WriteFile(filepath.Join(peerDir, "company", "COMPANY.md"), []byte("REMOTE OWNER EDIT\n"), 0o644)
	gitOut(t, peerDir, "add", "company/COMPANY.md")
	gitOut(t, peerDir, "commit", "-m", "owner edit")
	gitOut(t, peerDir, "push", "origin", branch)
	remoteHead := gitOut(t, peerDir, "rev-parse", "HEAD")

	// Conflicting local commit on the same file/line.
	_ = os.WriteFile(filepath.Join(localDir, "company", "COMPANY.md"), []byte("LOCAL MAX EDIT\n"), 0o644)
	if _, err := v.CommitPaths(ctx, []string{"company/COMPANY.md"}, "local edit"); err != nil {
		t.Fatalf("local CommitPaths: %v", err)
	}

	err := v.PullIfIdle(ctx, 0)
	if !errors.Is(err, vaultgit.ErrConflict) {
		t.Fatalf("PullIfIdle err = %v, want errors.Is(..., ErrConflict)", err)
	}

	localHead := gitOut(t, localDir, "rev-parse", "HEAD")
	if localHead != remoteHead {
		t.Fatalf("main branch HEAD = %s, want remote HEAD %s", localHead, remoteHead)
	}

	branches := gitOut(t, localDir, "branch", "--list", "max/conflict-*")
	if !strings.Contains(branches, "max/conflict-") {
		t.Fatalf("expected max/conflict-* branch preserving local edit, got %q", branches)
	}
}

func TestRefuseProjectSettings(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(dir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	if err := v.RefuseIfProjectSettings(ctx); err != nil {
		t.Fatalf("clean vault failed RefuseIfProjectSettings: %v", err)
	}

	bad := filepath.Join(dir, ".claude", "settings.local.json")
	_ = os.MkdirAll(filepath.Dir(bad), 0o755)
	_ = os.WriteFile(bad, []byte(`{}`), 0o644)
	if err := v.RefuseIfProjectSettings(ctx); err == nil {
		t.Fatal("expected RefuseIfProjectSettings to reject .claude/settings.local.json")
	}
}

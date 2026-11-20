package vaultgit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"max/internal/adapter/vaultgit"
)

func TestMirror(t *testing.T) {
	ctx := context.Background()
	vaultDir := t.TempDir()

	writeSkill := func(rel, content string) {
		t.Helper()
		full := filepath.Join(vaultDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeSkill("skills/alpha/SKILL.md", "---\nname: alpha\ndescription: v1\n---\nBody v1\n")
	writeSkill("skills/beta/SKILL.md", "---\nname: beta\ndescription: v1\n---\nBeta body\n")
	writeSkill("skills/_proposed/draft/SKILL.md", "---\nname: draft\ndescription: proposed\n---\nDraft\n")

	if err := vaultgit.MirrorSkills(ctx, vaultDir); err != nil {
		t.Fatalf("MirrorSkills initial: %v", err)
	}

	// Verify alpha and beta mirrored, _proposed excluded
	gotAlpha, err := os.ReadFile(filepath.Join(vaultDir, ".claude", "skills", "alpha", "SKILL.md"))
	if err != nil || string(gotAlpha) != "---\nname: alpha\ndescription: v1\n---\nBody v1\n" {
		t.Fatalf("alpha mirror mismatch: err=%v, got=%q", err, string(gotAlpha))
	}
	if _, err := os.Stat(filepath.Join(vaultDir, ".claude", "skills", "_proposed")); !os.IsNotExist(err) {
		t.Fatalf("expected _proposed excluded from mirror, stat err=%v", err)
	}

	// Modify alpha and delete beta
	writeSkill("skills/alpha/SKILL.md", "---\nname: alpha\ndescription: v2\n---\nBody v2\n")
	if err := os.RemoveAll(filepath.Join(vaultDir, "skills", "beta")); err != nil {
		t.Fatal(err)
	}

	if err := vaultgit.MirrorSkills(ctx, vaultDir); err != nil {
		t.Fatalf("MirrorSkills update/delete: %v", err)
	}

	gotAlphaV2, err := os.ReadFile(filepath.Join(vaultDir, ".claude", "skills", "alpha", "SKILL.md"))
	if err != nil || string(gotAlphaV2) != "---\nname: alpha\ndescription: v2\n---\nBody v2\n" {
		t.Fatalf("alpha v2 mirror mismatch: err=%v, got=%q", err, string(gotAlphaV2))
	}
	if _, err := os.Stat(filepath.Join(vaultDir, ".claude", "skills", "beta")); !os.IsNotExist(err) {
		t.Fatalf("expected deleted skill beta removed from mirror, stat err=%v", err)
	}
}

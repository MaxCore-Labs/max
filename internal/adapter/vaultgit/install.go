package vaultgit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	vaulttemplate "max/vault-template"
)

// EnsureInstalled initializes a git repository from vault-template if the vault directory is empty.
func (v *Vault) EnsureInstalled(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := os.MkdirAll(v.dir, 0o755); err != nil {
		return fmt.Errorf("mkdir vault %q: %w", v.dir, err)
	}
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return fmt.Errorf("readdir vault %q: %w", v.dir, err)
	}
	for _, e := range entries {
		if e.Name() != ".DS_Store" {
			return nil
		}
	}

	if err := copyTemplateTree(vaulttemplate.FS, v.dir); err != nil {
		return fmt.Errorf("install vault template: %w", err)
	}
	if _, err := v.runGitLocked(ctx, "init"); err != nil {
		return err
	}
	if _, err := v.runGitLocked(ctx, "config", "user.name", "Max"); err != nil {
		return err
	}
	if _, err := v.runGitLocked(ctx, "config", "user.email", "max@localhost"); err != nil {
		return err
	}
	if _, err := v.runGitLocked(ctx, "add", "."); err != nil {
		return err
	}
	if _, err := v.runGitLocked(ctx, "commit", "-m", "chore(vault): initialize workspace"); err != nil {
		return err
	}
	return nil
}

func copyTemplateTree(src fs.FS, dstRoot string) error {
	return fs.WalkDir(src, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel == "." || rel == "embed.go" {
			return nil
		}
		target := filepath.Join(dstRoot, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := fs.ReadFile(src, rel)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

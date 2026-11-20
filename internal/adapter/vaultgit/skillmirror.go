package vaultgit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MirrorSkills synchronizes vaultDir/skills/<slug> (excluding _proposed and dotfiles) into vaultDir/.claude/skills/<slug>,
// updating modified files and removing mirrored directories for deleted skills.
func MirrorSkills(ctx context.Context, vaultDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	srcBase := filepath.Join(vaultDir, "skills")
	dstBase := filepath.Join(vaultDir, ".claude", "skills")

	entries, err := os.ReadDir(srcBase)
	if os.IsNotExist(err) {
		_ = os.RemoveAll(dstBase)
		return nil
	}
	if err != nil {
		return fmt.Errorf("readdir skills: %w", err)
	}

	if err := os.MkdirAll(dstBase, 0o755); err != nil {
		return fmt.Errorf("mkdir mirror skills: %w", err)
	}

	activeSlugs := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !entry.IsDir() || name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		activeSlugs[name] = true
		srcSkill := filepath.Join(srcBase, name)
		dstSkill := filepath.Join(dstBase, name)
		if err := syncDirExact(srcSkill, dstSkill); err != nil {
			return fmt.Errorf("mirror skill %q: %w", name, err)
		}
	}

	dstEntries, err := os.ReadDir(dstBase)
	if err != nil {
		return fmt.Errorf("readdir mirror skills: %w", err)
	}
	for _, de := range dstEntries {
		if !activeSlugs[de.Name()] {
			if err := os.RemoveAll(filepath.Join(dstBase, de.Name())); err != nil {
				return fmt.Errorf("remove stale mirrored skill %q: %w", de.Name(), err)
			}
		}
	}
	return nil
}

func syncDirExact(srcDir, dstDir string) error {
	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

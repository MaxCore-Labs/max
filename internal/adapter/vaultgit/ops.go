package vaultgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"max/internal/domain"
)

// Remove deletes a vault-relative file or subtree without following symlinks.
func (v *Vault) Remove(_ context.Context, rel string) error {
	clean, ok := domain.CleanRel(rel)
	if !ok {
		return fmt.Errorf("invalid vault remove path %q", rel)
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	cur := v.dir
	segs := strings.Split(clean, "/")
	for i, seg := range segs {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink remove at %q", clean)
		}
		if i == len(segs)-1 {
			return os.RemoveAll(cur)
		}
	}
	return nil
}

// HeadSHA returns the current HEAD commit SHA of the vault git repository.
func (v *Vault) HeadSHA(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.runGitLocked(ctx, "rev-parse", "HEAD")
}

// RevertLastByPrefix reverts the most recent commit whose subject begins with prefix (e.g. "learn:").
func (v *Vault) RevertLastByPrefix(ctx context.Context, prefix string) (string, error) {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return "", errors.New("vault revert prefix required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	out, err := v.runGitLocked(ctx, "log", "-n", "50", "--format=%H %s")
	if err != nil {
		return "", err
	}
	var targetSHA, targetSubj string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, subj, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if strings.HasPrefix(subj, "revert("+p) {
			continue
		}
		if strings.HasPrefix(subj, p) {
			targetSHA = sha
			targetSubj = subj
			break
		}
	}
	if targetSHA == "" {
		return "", fmt.Errorf("no commit found with prefix %q", p)
	}
	if _, err := v.runGitLocked(ctx, "revert", "--no-commit", targetSHA); err != nil {
		_, _ = v.runGitLocked(ctx, "revert", "--abort")
		return "", err
	}
	msg := sanitizeCommitMessage(fmt.Sprintf("revert(%s): undo %s", strings.TrimSuffix(p, ":"), targetSubj))
	if _, err := v.runGitLocked(ctx, "commit", "-m", msg); err != nil {
		return "", err
	}
	return v.runGitLocked(ctx, "rev-parse", "HEAD")
}

// MirrorSkills synchronizes skills/<slug> into .claude/skills/<slug> on the vault root.
func (v *Vault) MirrorSkills(ctx context.Context) error {
	return MirrorSkills(ctx, v.dir)
}

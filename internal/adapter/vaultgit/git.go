package vaultgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

// ErrConflict is returned wrapped by PullIfIdle when a rebase conflict is diverted to max/conflict-*.
var ErrConflict = errors.New("vault git rebase conflict")

// ConflictError wraps ErrConflict with the name of the preserved local conflict branch.
type ConflictError struct {
	Branch string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: local edits saved to branch %s", ErrConflict.Error(), e.Branch)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

var aiTrailerOrVendorRe = regexp.MustCompile(`(?im)^co-authored-by:.*$|\b(?:cla` + `ude|anthro` + `pic|gem` + `ini|open` + `ai)\b`)

// Vault implements app.Vault over the git CLI serialized through a single mutex.
type Vault struct {
	dir string
	mu  sync.Mutex
}

var _ app.Vault = (*Vault)(nil)

// New constructs a git-backed Vault rooted at dir.
func New(dir string) *Vault {
	return &Vault{dir: dir}
}

// Dir returns the vault root filesystem path.
func (v *Vault) Dir() string { return v.dir }

// CommitPaths stages and commits ONLY the specified relative paths using author Max <max@localhost>.
func (v *Vault) CommitPaths(ctx context.Context, paths []string, msg string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	cleanPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, ok := domain.CleanRel(p)
		if !ok {
			return "", fmt.Errorf("invalid vault commit path %q", p)
		}
		cleanPaths = append(cleanPaths, rel)
	}

	cleanMsg := sanitizeCommitMessage(msg)

	v.mu.Lock()
	defer v.mu.Unlock()

	addArgs := append([]string{"add", "--"}, cleanPaths...)
	if _, err := v.runGitLocked(ctx, addArgs...); err != nil {
		return "", err
	}

	diffArgs := append([]string{"diff", "--cached", "--quiet", "--"}, cleanPaths...)
	if _, err := v.runGitLocked(ctx, diffArgs...); err == nil {
		// Nothing staged on these paths.
		return "", nil
	}

	commitArgs := append([]string{"commit", "-m", cleanMsg, "--"}, cleanPaths...)
	if _, err := v.runGitLocked(ctx, commitArgs...); err != nil {
		return "", err
	}
	return v.runGitLocked(ctx, "rev-parse", "HEAD")
}

// PullIfIdle runs git fetch + rebase when runsActive == 0; conflicts move local commits to max/conflict-<ts>.
func (v *Vault) PullIfIdle(ctx context.Context, runsActive int) error {
	if runsActive > 0 {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	remotes, err := v.runGitLocked(ctx, "remote")
	if err != nil || strings.TrimSpace(remotes) == "" {
		return nil
	}
	branch, err := v.runGitLocked(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if _, err := v.runGitLocked(ctx, "fetch", "origin", branch); err != nil {
		return err
	}

	upstream := "origin/" + branch
	if _, err := v.runGitLocked(ctx, "rebase", upstream); err != nil {
		_, _ = v.runGitLocked(ctx, "rebase", "--abort")
		conflictBranch := fmt.Sprintf("max/conflict-%d", time.Now().UTC().UnixMilli())
		if _, bErr := v.runGitLocked(ctx, "branch", conflictBranch, "HEAD"); bErr != nil {
			return fmt.Errorf("save conflict branch %q: %w", conflictBranch, bErr)
		}
		if _, rErr := v.runGitLocked(ctx, "reset", "--hard", upstream); rErr != nil {
			return fmt.Errorf("reset to %s after conflict: %w", upstream, rErr)
		}
		return &ConflictError{Branch: conflictBranch}
	}
	return nil
}

// Push pushes committed vault history to origin when a remote is configured.
func (v *Vault) Push(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	remotes, err := v.runGitLocked(ctx, "remote")
	if err != nil || strings.TrimSpace(remotes) == "" {
		return nil
	}
	_, err = v.runGitLocked(ctx, "push")
	return err
}

// Read safely reads a vault-relative file without following symlinks.
func (v *Vault) Read(_ context.Context, rel string) ([]byte, error) {
	clean, ok := domain.CleanRel(rel)
	if !ok {
		return nil, fmt.Errorf("invalid vault path %q", rel)
	}
	cur := v.dir
	for _, seg := range strings.Split(clean, "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink read at %q", clean)
		}
	}
	return os.ReadFile(cur)
}

// RefuseIfProjectSettings returns an error if .claude/settings*.json exists inside the vault (Spec 8.6).
func (v *Vault) RefuseIfProjectSettings(_ context.Context) error {
	matches, err := filepath.Glob(filepath.Join(v.dir, ".claude", "settings*.json"))
	if err != nil {
		return fmt.Errorf("glob .claude/settings*.json: %w", err)
	}
	if len(matches) > 0 {
		return fmt.Errorf("forbidden project settings file in vault: %s", filepath.Base(matches[0]))
	}
	return nil
}

func (v *Vault) runGitLocked(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = v.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Max",
		"GIT_AUTHOR_EMAIL=max@localhost",
		"GIT_COMMITTER_NAME=Max",
		"GIT_COMMITTER_EMAIL=max@localhost",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w (%s)", args, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func sanitizeCommitMessage(msg string) string {
	clean := platform.Redact(msg)
	clean = aiTrailerOrVendorRe.ReplaceAllString(clean, "")
	lines := strings.Split(clean, "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	if len(kept) == 0 {
		return "vault: update"
	}
	return strings.Join(kept, "\n")
}

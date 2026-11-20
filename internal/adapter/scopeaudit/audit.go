package scopeaudit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"max/internal/domain"
	"max/internal/platform"
)

const maxVaultFileBytes = 5 * 1024 * 1024

// Violation describes an out-of-scope, symlink, binary, oversize, or secret-bearing vault change reverted after a run.
type Violation struct {
	Rel      string
	Reason   string
	Reverted bool
}

type statusEntry struct {
	xy        string
	rel       string
	untracked bool
}

// Audit inspects modified and untracked paths in vaultDir against scope, reverting/removing disallowed edits
// and quarantining any file containing secrets into quarantineDir (Spec 8.4.5).
func Audit(ctx context.Context, vaultDir, quarantineDir string, scope domain.AgentScope) (touched []string, violations []Violation, err error) {
	entries, err := gitStatusEntries(ctx, vaultDir)
	if err != nil {
		return nil, nil, err
	}

	for _, e := range entries {
		cleanRel, valid := domain.CleanRel(e.rel)
		if !valid {
			rev := revertEntry(ctx, vaultDir, e.rel, e.untracked)
			violations = append(violations, Violation{
				Rel:      e.rel,
				Reason:   "invalid vault relative path",
				Reverted: rev,
			})
			continue
		}

		if linkTarget, isLink := detectSymlinkComponent(vaultDir, cleanRel); isLink {
			rev := removeSymlinkSafely(ctx, vaultDir, cleanRel, e.untracked)
			violations = append(violations, Violation{
				Rel:      cleanRel,
				Reason:   fmt.Sprintf("symlink not permitted (target: %s)", linkTarget),
				Reverted: rev,
			})
			continue
		}

		okWrite, writeReason := domain.CanWrite(scope, cleanRel)
		if !okWrite {
			rev := revertEntry(ctx, vaultDir, cleanRel, e.untracked)
			violations = append(violations, Violation{
				Rel:      cleanRel,
				Reason:   writeReason,
				Reverted: rev,
			})
			continue
		}

		fullPath := filepath.Join(vaultDir, filepath.FromSlash(cleanRel))
		fi, statErr := os.Lstat(fullPath)
		if os.IsNotExist(statErr) {
			// In-scope deletion of a tracked file.
			touched = append(touched, cleanRel)
			continue
		}
		if statErr != nil {
			return nil, nil, fmt.Errorf("lstat %q: %w", cleanRel, statErr)
		}
		if fi.IsDir() {
			continue
		}

		if fi.Size() > maxVaultFileBytes {
			rev := revertEntry(ctx, vaultDir, cleanRel, e.untracked)
			violations = append(violations, Violation{
				Rel:      cleanRel,
				Reason:   fmt.Sprintf("file vượt quá 5 MB (%d bytes)", fi.Size()),
				Reverted: rev,
			})
			continue
		}

		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			return nil, nil, fmt.Errorf("read %q: %w", cleanRel, readErr)
		}

		if isBinaryContent(data) && !isInAgentArtifacts(scope, cleanRel) {
			rev := revertEntry(ctx, vaultDir, cleanRel, e.untracked)
			violations = append(violations, Violation{
				Rel:      cleanRel,
				Reason:   "file nhị phân không được phép ngoài artifacts/",
				Reverted: rev,
			})
			continue
		}

		if platform.ContainsSecret(string(data)) {
			if quarantineDir != "" {
				qDest := filepath.Join(quarantineDir, filepath.FromSlash(cleanRel))
				if mkErr := os.MkdirAll(filepath.Dir(qDest), 0o700); mkErr == nil {
					_ = os.WriteFile(qDest, data, 0o600)
				}
			}
			rev := revertEntry(ctx, vaultDir, cleanRel, e.untracked)
			violations = append(violations, Violation{
				Rel:      cleanRel,
				Reason:   "file chứa secret, đã chuyển sang quarantine",
				Reverted: rev,
			})
			continue
		}

		touched = append(touched, cleanRel)
	}

	return touched, violations, nil
}

func gitStatusEntries(ctx context.Context, vaultDir string) ([]statusEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "-z", "-uall")
	cmd.Dir = vaultDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}

	tokens := bytes.Split(out, []byte{0})
	var entries []statusEntry
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if len(tok) < 4 {
			continue
		}
		xy := string(tok[:2])
		rel := filepath.ToSlash(string(tok[3:]))
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}
		entries = append(entries, statusEntry{
			xy:        xy,
			rel:       rel,
			untracked: xy == "??" || xy == "!!",
		})
	}
	return entries, nil
}

func detectSymlinkComponent(vaultDir, cleanRel string) (string, bool) {
	cur := vaultDir
	for _, seg := range strings.Split(cleanRel, "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", false
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(cur)
			if target == "" {
				target = "unresolved-symlink"
			}
			return target, true
		}
	}
	return "", false
}

func removeSymlinkSafely(ctx context.Context, vaultDir, cleanRel string, untracked bool) bool {
	cur := vaultDir
	for _, seg := range strings.Split(cleanRel, "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(cur)
			break
		}
	}
	if !untracked {
		_ = gitCheckoutFile(ctx, vaultDir, cleanRel)
	}
	return true
}

func revertEntry(ctx context.Context, vaultDir, rel string, untracked bool) bool {
	full := filepath.Join(vaultDir, filepath.FromSlash(rel))
	if untracked {
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return false
		}
		pruneEmptyParents(vaultDir, filepath.Dir(full))
		return true
	}
	_ = exec.CommandContext(ctx, "git", "-C", vaultDir, "reset", "-q", "--", rel).Run()
	return gitCheckoutFile(ctx, vaultDir, rel) == nil
}

func gitCheckoutFile(ctx context.Context, vaultDir, rel string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", "--", rel)
	cmd.Dir = vaultDir
	return cmd.Run()
}

func pruneEmptyParents(vaultDir, dir string) {
	cleanRoot := filepath.Clean(vaultDir)
	cur := filepath.Clean(dir)
	for cur != cleanRoot && strings.HasPrefix(cur, cleanRoot+string(filepath.Separator)) {
		if err := os.Remove(cur); err != nil {
			return
		}
		cur = filepath.Dir(cur)
	}
}

func isBinaryContent(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	return !utf8.Valid(data)
}

func isInAgentArtifacts(scope domain.AgentScope, cleanRel string) bool {
	prefix := "agents/" + scope.Slug + "/artifacts/"
	return scope.Slug != "" && strings.HasPrefix(cleanRel, prefix)
}

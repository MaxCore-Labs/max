package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"max/internal/app"
	"max/internal/domain"
)

var validSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Config configures the isolated developer repository workspace Service.
type Config struct {
	Store        app.Store
	DataDir      string
	VaultDir     string
	DevOSUser    string
	EngineLevels map[domain.EngineName]int
}

// Service prepares isolated per-developer git workspaces under <DataDir>/workspaces/<slug> outside the vault (Spec 8.5.1 & P4.01).
type Service struct {
	cfg Config
}

// NewService constructs a developer workspaces Service.
func NewService(cfg Config) *Service {
	if strings.TrimSpace(cfg.DevOSUser) == "" {
		cfg.DevOSUser = "max-dev"
	}
	if len(cfg.EngineLevels) == 0 {
		cfg.EngineLevels = map[domain.EngineName]int{
			domain.EngineClaude: 1,
			domain.EngineCodex:  2,
			domain.EngineAgy:    3,
		}
	}
	return &Service{cfg: cfg}
}

// OSUser returns the dedicated OS account name under which developer shell processes run.
func (s *Service) OSUser() string {
	return s.cfg.DevOSUser
}

// VerifyHostIsolation locks down <DataDir>/max.sqlite3 and <DataDir>/agent-home/* permissions so separate OS users cannot read them.
func (s *Service) VerifyHostIsolation() error {
	if cur, err := user.Current(); err == nil && cur.Username == s.cfg.DevOSUser {
		return fmt.Errorf("developer OS user %q must not equal daemon user %q", s.cfg.DevOSUser, cur.Username)
	}
	dbPath := filepath.Join(s.cfg.DataDir, "max.sqlite3")
	if _, err := os.Stat(dbPath); err == nil {
		_ = os.Chmod(dbPath, 0o600)
	}
	homeBase := filepath.Join(s.cfg.DataDir, "agent-home")
	if entries, err := os.ReadDir(homeBase); err == nil {
		_ = os.Chmod(homeBase, 0o700)
		for _, e := range entries {
			_ = os.Chmod(filepath.Join(homeBase, e.Name()), 0o700)
		}
	}
	return nil
}

// Prepare validates Level-1 engine eligibility and prepares an isolated repo directory at <DataDir>/workspaces/<slug>.
func (s *Service) Prepare(ctx context.Context, agentID domain.ID, repoURL string) (string, error) {
	ag, ok, err := s.cfg.Store.Read().GetAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("agent %q not found", agentID)
	}
	if !ag.Shell && (ag.TemplateSlug == nil || *ag.TemplateSlug != "developer") {
		return "", fmt.Errorf("agent %q is not a shell-capable developer", ag.Slug)
	}
	if !validSlugRe.MatchString(ag.Slug) {
		return "", fmt.Errorf("invalid developer slug %q", ag.Slug)
	}

	if err := s.checkLevel1Requirement(ag); err != nil {
		return "", err
	}
	if err := s.VerifyHostIsolation(); err != nil {
		return "", err
	}

	wsRoot, err := filepath.Abs(filepath.Join(s.cfg.DataDir, "workspaces"))
	if err != nil {
		return "", err
	}
	targetDir := filepath.Join(wsRoot, ag.Slug)

	if s.cfg.VaultDir != "" {
		absVault, vErr := filepath.Abs(s.cfg.VaultDir)
		if vErr == nil {
			slTarget := filepath.ToSlash(filepath.Clean(targetDir))
			slVault := filepath.ToSlash(filepath.Clean(absVault))
			if slTarget == slVault || strings.HasPrefix(slTarget, slVault+"/") || strings.HasPrefix(slVault, slTarget+"/") {
				return "", errors.New("developer workspace directory must be strictly outside vault")
			}
		}
	}

	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		return "", fmt.Errorf("mkdir workspaces root: %w", err)
	}

	gitDir := filepath.Join(targetDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return targetDir, nil
	}

	repo := strings.TrimSpace(repoURL)
	if repo != "" {
		cmd := exec.CommandContext(ctx, "git", "clone", "--", repo, targetDir)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git clone into workspace: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return targetDir, nil
	}

	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = targetDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git init workspace: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return targetDir, nil
}

func (s *Service) checkLevel1Requirement(ag app.AgentRecord) error {
	if ag.AllowUngatedEngine {
		return nil
	}
	var engines []domain.EngineName
	_ = json.Unmarshal([]byte(ag.EnginesJSON), &engines)
	for _, eng := range engines {
		if lvl, ok := s.cfg.EngineLevels[eng]; ok && lvl == 1 {
			return nil
		}
	}
	return fmt.Errorf("developer role %q requires at least one Level-1 engine (refused on Level 2/3 engines %v)", ag.Slug, engines)
}

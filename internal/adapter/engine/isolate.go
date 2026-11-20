package engine

import (
	"os/exec"
	"os/user"
	"runtime"
	"strings"

	"max/internal/app"
)

// Level classifies an engine's built-in tool isolation capability into Level 1, 2, or 3 per Spec 8.4.5.
func Level(caps app.EngineCaps) int {
	if caps.PreToolGate && caps.ToolAllowlist {
		return 1
	}
	if caps.Sandbox {
		return 2
	}
	return 3
}

// RunAs returns a copy of spec configured to run under the dedicated OS account osUser.
func RunAs(spec ProcSpec, osUser string) ProcSpec {
	spec.OSUser = strings.TrimSpace(osUser)
	return spec
}

func applyOSUserWrapper(s ProcSpec) ProcSpec {
	target := strings.TrimSpace(s.OSUser)
	if target == "" || runtime.GOOS == "windows" {
		return s
	}
	if cur, err := user.Current(); err == nil && cur.Username == target {
		return s
	}
	if sudoBin, err := exec.LookPath("sudo"); err == nil {
		wrapped := append([]string{"-n", "-u", target, "--", s.Bin}, s.Args...)
		s.Bin = sudoBin
		s.Args = wrapped
	}
	return s
}

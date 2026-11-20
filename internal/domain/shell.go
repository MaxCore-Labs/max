package domain

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	pipeToShellRe = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|]*\|\s*(?:ba|z|da|k)?sh\b`)
	rmRootRe      = regexp.MustCompile(`(?:^|\s)rm\s+(?:-[a-zA-Z]*r[a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*r|--recursive)\s+/(?:\s|$|\*)`)
)

// ShellVerdict classifies a developer shell command into Allow (read/test), Deny (destructive/exfil/privilege), or Ask.
func ShellVerdict(cmd string) Verdict {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return Deny
	}
	if pipeToShellRe.MatchString(trimmed) || rmRootRe.MatchString(trimmed) {
		return Deny
	}
	if strings.Contains(trimmed, "$(") || strings.Contains(trimmed, "`") {
		return Ask
	}

	segments := splitShellSegments(trimmed)
	if len(segments) == 0 {
		return Deny
	}

	overall := Allow
	for _, seg := range segments {
		v := singleSegmentVerdict(seg)
		if v == Deny {
			return Deny
		}
		if v == Ask {
			overall = Ask
		}
	}
	return overall
}

func singleSegmentVerdict(seg string) Verdict {
	s := strings.TrimSpace(seg)
	if s == "" {
		return Deny
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Deny
	}
	bin := fields[0]

	switch bin {
	case "sudo", "su", "ssh", "scp", "sftp", "nc", "netcat", "mkfs", "shutdown", "reboot":
		return Deny
	case "rm":
		for _, f := range fields[1:] {
			if f == "/" || f == "/*" || f == "--no-preserve-root" {
				return Deny
			}
		}
		return Ask
	case "git":
		if len(fields) < 2 {
			return Ask
		}
		switch fields[1] {
		case "push":
			return Deny
		case "status", "diff", "log", "show":
			if containsRedirect(s) {
				return Ask
			}
			return Allow
		default:
			return Ask
		}
	case "ls", "pwd", "rg", "grep", "whoami", "uname":
		if containsRedirect(s) {
			return Ask
		}
		return Allow
	case "go":
		if len(fields) >= 2 && (fields[1] == "test" || fields[1] == "vet") && !containsRedirect(s) {
			return Allow
		}
		return Ask
	default:
		return Ask
	}
}

func splitShellSegments(cmd string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	raw := replacer.Replace(cmd)
	var out []string
	for _, part := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(part)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func containsRedirect(seg string) bool {
	return strings.ContainsAny(seg, "><")
}

func extractShellCommand(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(trimmed), &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "CommandLine", "cmd"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

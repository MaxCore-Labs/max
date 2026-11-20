package domain

import (
	"path"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Zone classifies a vault-relative path by trust boundary (Spec 8.6).
type Zone int

const (
	ZoneTrusted Zone = iota
	ZoneData
	ZoneOutside
)

func (z Zone) String() string {
	switch z {
	case ZoneTrusted:
		return "trusted"
	case ZoneData:
		return "data"
	default:
		return "outside"
	}
}

// AgentScope defines the vault read/write scope of a single agent.
type AgentScope struct {
	Slug      string
	TeamSlug  string
	ReadExtra []string
}

// CleanRel validates and normalizes a vault-relative path to Unicode NFC.
// It rejects empty paths, ".", absolute paths, Windows drive letters, NUL bytes, and any ".." segment.
func CleanRel(p string) (rel string, ok bool) {
	if p == "" || strings.IndexByte(p, 0) >= 0 {
		return "", false
	}
	s := norm.NFC.String(p)
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) || hasDrivePrefix(s) {
		return "", false
	}
	s = strings.ReplaceAll(s, `\`, "/")
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(s)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func hasDrivePrefix(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// Classify returns the trust Zone of a vault path.
func Classify(rel string) Zone {
	clean, ok := CleanRel(rel)
	if !ok {
		return ZoneOutside
	}
	segs := strings.Split(clean, "/")
	for _, seg := range segs {
		if strings.HasPrefix(seg, ".env") {
			return ZoneOutside
		}
	}
	top := segs[0]
	if top == "data" || top == ".git" {
		return ZoneOutside
	}
	if top == "skills" {
		if len(segs) >= 2 && segs[1] == "_proposed" {
			return ZoneData
		}
		return ZoneTrusted
	}
	switch top {
	case "memory", "company", "templates", "routines", ".claude":
		return ZoneTrusted
	case "teams":
		if len(segs) == 1 || (len(segs) == 3 && segs[2] == "TEAM.md") {
			return ZoneTrusted
		}
		return ZoneData
	case "agents":
		if len(segs) == 1 || (len(segs) == 3 && segs[2] == "AGENT.md") {
			return ZoneTrusted
		}
		return ZoneData
	case "wiki", "sources", "journal":
		return ZoneData
	default:
		// Root files such as CLAUDE.md, AGENTS.md, .gitignore are trusted config.
		if len(segs) == 1 {
			return ZoneTrusted
		}
		return ZoneTrusted
	}
}

// CanRead reports whether agent scope a is permitted to read rel inside the vault.
func CanRead(a AgentScope, rel string) bool {
	clean, ok := CleanRel(rel)
	if !ok {
		return false
	}
	if Classify(clean) == ZoneOutside {
		return false
	}
	segs := strings.Split(clean, "/")
	switch segs[0] {
	case "company", "skills", "wiki", "sources", "journal", "memory", "templates", "routines":
		return true
	case "teams":
		if len(segs) >= 2 && a.TeamSlug != "" && segs[1] == a.TeamSlug {
			return true
		}
		return matchesExtra(a.ReadExtra, clean)
	case "agents":
		if len(segs) >= 2 && a.Slug != "" && segs[1] == a.Slug {
			return true
		}
		return matchesExtra(a.ReadExtra, clean)
	default:
		return matchesExtra(a.ReadExtra, clean)
	}
}

// CanWrite reports whether agent scope a is permitted to write rel inside the vault.
func CanWrite(a AgentScope, rel string) (ok bool, reason string) {
	clean, valid := CleanRel(rel)
	if !valid {
		return false, "invalid or out-of-vault path"
	}
	switch Classify(clean) {
	case ZoneOutside:
		return false, "path outside allowed vault scope"
	case ZoneTrusted:
		return false, "path is in trusted zone"
	}
	segs := strings.Split(clean, "/")
	switch segs[0] {
	case "wiki", "sources":
		if len(segs) < 2 {
			return false, "cannot overwrite top-level directory"
		}
		return true, ""
	case "skills":
		if len(segs) >= 3 && segs[1] == "_proposed" {
			return true, ""
		}
		return false, "only skills/_proposed/<slug>/... is writable"
	case "agents":
		if a.Slug == "" || len(segs) < 4 || segs[1] != a.Slug {
			return false, "cannot write outside own agent directory"
		}
		if segs[2] == "notes" || segs[2] == "artifacts" {
			return true, ""
		}
		return false, "agent may only write under notes/ or artifacts/"
	default:
		return false, "path is read-only for agents"
	}
}

func matchesExtra(extra []string, clean string) bool {
	for _, raw := range extra {
		prefix, ok := CleanRel(raw)
		if !ok {
			continue
		}
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

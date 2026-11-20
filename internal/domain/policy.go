package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// AgentPolicy captures the capabilities and scope of an agent for policy evaluation.
type AgentPolicy struct {
	Scope       AgentScope
	Autonomy    Autonomy
	Web         bool
	Browser     BrowserProfile
	Shell       bool
	EngineLevel int
}

// ToolCall describes a normalized tool invocation evaluated by Decide.
type ToolCall struct {
	Name       string
	Class      ToolClass
	Rel        string
	SearchRoot string
	Connection string
	InputHash  string
	Input      json.RawMessage
}

// Grant represents an owner-issued standing permission for an agent tool call.
type Grant struct {
	ID                 ID
	Connection         string
	Tool               string
	Constraints        Constraints
	AllowedRootOrigins []RootOrigin
	ExpiresAt          *time.Time
	Revoked            bool
}

// ClassifyBrowserTool classifies Playwright MCP 0.0.85 tools per Spike S1 (9 read-only R tools; everything else Ba).
func ClassifyBrowserTool(name string) ToolClass {
	switch name {
	case "browser_navigate",
		"browser_navigate_back",
		"browser_wait_for",
		"browser_snapshot",
		"browser_find",
		"browser_console_messages",
		"browser_network_requests",
		"browser_network_request",
		"browser_take_screenshot":
		return ToolClassR
	default:
		return ToolClassBa
	}
}

// ClassifyBuiltin maps built-in CLI tool names to their base ToolClass (Spec 8.5.1 & 8.6).
func ClassifyBuiltin(tool string) ToolClass {
	if strings.HasPrefix(tool, "browser_") {
		return ClassifyBrowserTool(tool)
	}
	switch tool {
	case "Read", "Grep", "Glob", "WebSearch", "WebFetch",
		"read_file", "grep_search", "find_by_name", "list_dir", "search_web", "read_url_content":
		return ToolClassR
	case "Write", "Edit", "MultiEdit", "NotebookEdit", "write_file", "edit_file":
		return ToolClassWd
	case "Bash", "run_command", "execute_command":
		return ToolClassX
	default:
		return ToolClassUnknown
	}
}

// Decide evaluates a tool call purely against AgentPolicy, task RootOrigin, and active Grants (Spec 8.6).
func Decide(a AgentPolicy, c ToolCall, origin RootOrigin, grants []Grant, now time.Time) Decision {
	if a.Autonomy != AutonomySuggest && a.Autonomy != AutonomyAuto {
		return Decision{Verdict: Deny, Reason: "invalid agent autonomy mode"}
	}
	cls := c.Class
	if cls == "" {
		cls = ClassifyBuiltin(c.Name)
	}
	if cls == ToolClassWd && c.Rel != "" && Classify(c.Rel) == ZoneTrusted {
		cls = ToolClassWt
	}

	switch cls {
	case ToolClassR:
		return decideRead(a, c)
	case ToolClassC:
		return decideCoord(a, c)
	case ToolClassWd:
		return decideWriteData(a, c)
	case ToolClassWt:
		return Decision{Verdict: Deny, Reason: "trusted zone writes require dedicated Go gate"}
	case ToolClassP:
		if a.Autonomy == AutonomySuggest {
			return Decision{Verdict: Deny, Reason: "suggest-mode agent cannot create proposals"}
		}
		return Decision{Verdict: Allow}
	case ToolClassBa:
		return decideBrowser(a, c, origin, grants, now)
	case ToolClassE:
		if a.Autonomy == AutonomySuggest {
			return Decision{Verdict: Deny, Reason: "suggest-mode agent cannot execute external actions"}
		}
		if gid, ok, mismatch := findMatchingGrant(grants, c.Connection, c.Name, c.Input, origin, now); ok {
			return Decision{Verdict: Allow, GrantID: gid}
		} else if mismatch != "" {
			return Decision{Verdict: Ask, Reason: "grant constraint mismatch: " + mismatch}
		}
		return Decision{Verdict: Ask, Reason: "external action requires owner approval or active grant"}
	case ToolClassX:
		if a.Autonomy == AutonomySuggest {
			return Decision{Verdict: Deny, Reason: "suggest-mode agent cannot execute shell commands"}
		}
		if !a.Shell {
			return Decision{Verdict: Deny, Reason: "shell capability is disabled for this agent"}
		}
		if cmd := extractShellCommand(c.Input); cmd != "" {
			switch ShellVerdict(cmd) {
			case Deny:
				return Decision{Verdict: Deny, Reason: "shell command is forbidden by security policy"}
			case Allow:
				return Decision{Verdict: Allow}
			}
		}
		if gid, ok, _ := findMatchingGrant(grants, c.Connection, c.Name, c.Input, origin, now); ok {
			return Decision{Verdict: Allow, GrantID: gid}
		}
		return Decision{Verdict: Ask, Reason: "shell execution requires owner approval"}
	default:
		if a.Autonomy == AutonomySuggest {
			return Decision{Verdict: Deny, Reason: "unclassified tool denied in suggest mode"}
		}
		return Decision{Verdict: Ask, Reason: "unclassified tool requires owner approval"}
	}
}

func decideRead(a AgentPolicy, c ToolCall) Decision {
	switch c.Name {
	case "WebSearch", "WebFetch", "search_web", "read_url_content":
		if !a.Web {
			return Decision{Verdict: Deny, Reason: "web access is disabled for this agent"}
		}
	}
	if strings.HasPrefix(c.Name, "browser_") && a.Browser == BrowserNone {
		return Decision{Verdict: Deny, Reason: "browser access is disabled for this agent"}
	}
	if c.Rel != "" && !CanRead(a.Scope, c.Rel) {
		return Decision{Verdict: Deny, Reason: "path outside agent vault read scope"}
	}
	if c.SearchRoot != "" && !CanRead(a.Scope, c.SearchRoot) {
		return Decision{Verdict: Deny, Reason: "search root outside agent vault read scope"}
	}
	return Decision{Verdict: Allow}
}

func decideCoord(a AgentPolicy, c ToolCall) Decision {
	if a.Autonomy == AutonomySuggest {
		switch c.Name {
		case "ask_owner", "notify_owner", "task_comment", "task_get", "task_list", "ping":
			return Decision{Verdict: Allow}
		default:
			return Decision{Verdict: Deny, Reason: "suggest-mode agent may only comment or notify/ask owner"}
		}
	}
	return Decision{Verdict: Allow}
}

func decideWriteData(a AgentPolicy, c ToolCall) Decision {
	if a.Autonomy == AutonomySuggest {
		return Decision{Verdict: Deny, Reason: "suggest-mode agent cannot write data"}
	}
	if c.Rel != "" {
		ok, reason := CanWrite(a.Scope, c.Rel)
		if !ok {
			return Decision{Verdict: Deny, Reason: reason}
		}
	}
	return Decision{Verdict: Allow}
}

func decideBrowser(a AgentPolicy, c ToolCall, origin RootOrigin, grants []Grant, now time.Time) Decision {
	if a.Autonomy == AutonomySuggest {
		return Decision{Verdict: Deny, Reason: "suggest-mode agent cannot perform browser actions"}
	}
	switch a.Browser {
	case BrowserNone, "":
		return Decision{Verdict: Deny, Reason: "browser profile is none"}
	case BrowserAnon:
		return Decision{Verdict: Allow}
	case BrowserOwner:
		conn := c.Connection
		if conn == "" {
			conn = "browser"
		}
		if gid, ok, mismatch := findMatchingGrant(grants, conn, c.Name, c.Input, origin, now); ok {
			return Decision{Verdict: Allow, GrantID: gid}
		} else if mismatch != "" {
			return Decision{Verdict: Ask, Reason: "grant constraint mismatch: " + mismatch}
		}
		return Decision{Verdict: Ask, Reason: "owner browser profile requires approval or matching grant"}
	default:
		return Decision{Verdict: Deny, Reason: "unknown browser profile"}
	}
}

func findMatchingGrant(grants []Grant, conn, tool string, input json.RawMessage, origin RootOrigin, now time.Time) (ID, bool, string) {
	var firstMismatch string
	for _, g := range grants {
		if g.Revoked {
			continue
		}
		if g.ExpiresAt != nil && !now.Before(*g.ExpiresAt) {
			continue
		}
		if g.Connection != conn {
			continue
		}
		if g.Tool != "*" && g.Tool != tool {
			continue
		}
		if !originAllowed(g.AllowedRootOrigins, origin) {
			continue
		}
		ok, reason := g.Constraints.Match(input)
		if ok {
			return g.ID, true, ""
		}
		if firstMismatch == "" {
			firstMismatch = reason
		}
	}
	return "", false, firstMismatch
}

func originAllowed(allowed []RootOrigin, origin RootOrigin) bool {
	if len(allowed) == 0 {
		return origin == OriginOwner || origin == OriginRoutine
	}
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}

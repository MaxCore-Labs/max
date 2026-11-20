package domain

import (
	"encoding/json"
	"net/url"
	"strings"
)

// BrowserOriginRule defines an owner-configured browser action verdict for an origin pattern (Spec 8.10 & P4.02).
type BrowserOriginRule struct {
	Pattern string
	Action  string
	Verdict Verdict
}

// BrowserPolicy holds ordered origin rules for an owner browser profile; default verdict is Ask.
type BrowserPolicy struct {
	Rules []BrowserOriginRule
}

// OriginRule evaluates policy against (origin, action). Defaults to Ask; Deny takes precedence over Ask and Allow.
func OriginRule(policy BrowserPolicy, origin string, action string) Verdict {
	scheme, host, ok := parseOriginParts(origin)
	if !ok {
		return Ask
	}
	act := strings.ToLower(strings.TrimSpace(action))
	if act == "" {
		return Ask
	}

	matchedAllow := false
	matchedAsk := false
	for _, r := range policy.Rules {
		if !matchOriginPattern(r.Pattern, scheme, host) {
			continue
		}
		rAct := strings.ToLower(strings.TrimSpace(r.Action))
		if rAct != "*" && rAct != act {
			continue
		}
		switch r.Verdict {
		case Deny:
			return Deny
		case Ask:
			matchedAsk = true
		case Allow:
			matchedAllow = true
		}
	}
	if matchedAsk {
		return Ask
	}
	if matchedAllow {
		return Allow
	}
	return Ask
}

// IsOwnerPasswordAction detects attempts to type or fill password fields so the agent uses handoff instead.
func IsOwnerPasswordAction(toolName string, raw json.RawMessage) bool {
	switch toolName {
	case "browser_type", "browser_fill_form":
	default:
		return false
	}
	lower := strings.ToLower(string(raw))
	if strings.Contains(lower, "type=password") ||
		strings.Contains(lower, `type="password"`) ||
		strings.Contains(lower, `type='password'`) ||
		strings.Contains(lower, `"type":"password"`) ||
		strings.Contains(lower, `"type": "password"`) {
		return true
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	if tgt, ok := m["target"].(string); ok {
		tl := strings.ToLower(tgt)
		if strings.Contains(tl, "password") || strings.Contains(tl, "passwd") {
			return true
		}
	}
	if fields, ok := m["fields"].([]any); ok {
		for _, item := range fields {
			fm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, k := range []string{"type", "name", "selector", "label"} {
				if s, ok := fm[k].(string); ok && strings.Contains(strings.ToLower(s), "password") {
					return true
				}
			}
		}
	}
	return false
}

func parseOriginParts(raw string) (scheme, host string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", false
	}
	return strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), true
}

func matchOriginPattern(pattern, scheme, host string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "*" {
		return true
	}
	idx := strings.Index(p, "://")
	if idx <= 0 {
		return false
	}
	pScheme := p[:idx]
	pHost := p[idx+3:]
	if slash := strings.IndexByte(pHost, '/'); slash >= 0 {
		pHost = pHost[:slash]
	}
	if colon := strings.IndexByte(pHost, ':'); colon >= 0 {
		pHost = pHost[:colon]
	}
	if pScheme != "*" && pScheme != scheme {
		return false
	}
	if pHost == "*" {
		return true
	}
	if strings.HasPrefix(pHost, "*.") {
		suffix := pHost[1:] // ".docs.example.com"
		return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
	}
	return host == pHost
}

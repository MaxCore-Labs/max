package platform

import "regexp"

var (
	pemBlockRe = regexp.MustCompile(`(?s)-----BEGIN ([A-Z0-9 ]*?)PRIVATE KEY-----.*?-----END [A-Z0-9 ]*?PRIVATE KEY-----`)
	bearerRe   = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	urlPassRe  = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)([^@\s/]+)(@)`)
	tgTokenRe  = regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{20,}\b`)
	apiKeyRe   = regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,}|AIza[0-9A-Za-z_-]{20,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{16,})\b`)
)

// Redact scrubs secret keys, tokens, basic-auth passwords, and PEM private keys
// from s while preserving surrounding diagnostic text.
func Redact(s string) string {
	if s == "" {
		return ""
	}
	out := pemBlockRe.ReplaceAllString(s, "-----BEGIN [REDACTED] PRIVATE KEY-----")
	out = bearerRe.ReplaceAllString(out, "Bearer [REDACTED]")
	out = urlPassRe.ReplaceAllString(out, "${1}[REDACTED]${3}")
	out = tgTokenRe.ReplaceAllString(out, "[REDACTED_TG_TOKEN]")
	out = apiKeyRe.ReplaceAllString(out, "[REDACTED_API_KEY]")
	return out
}

// ContainsSecret reports whether s matches any known secret pattern (used by post-run quarantine).
func ContainsSecret(s string) bool {
	if s == "" {
		return false
	}
	return pemBlockRe.MatchString(s) ||
		bearerRe.MatchString(s) ||
		urlPassRe.MatchString(s) ||
		tgTokenRe.MatchString(s) ||
		apiKeyRe.MatchString(s)
}

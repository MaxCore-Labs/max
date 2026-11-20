package domain_test

import (
	"testing"

	"max/internal/domain"
)

func TestOriginRule(t *testing.T) {
	pol := domain.BrowserPolicy{
		Rules: []domain.BrowserOriginRule{
			{Pattern: "https://*.docs.example.com", Action: "read", Verdict: domain.Allow},
			{Pattern: "https://app.example.com", Action: "*", Verdict: domain.Allow},
			{Pattern: "https://*.bank.example.com", Action: "*", Verdict: domain.Deny},
			{Pattern: "https://billing.example.com", Action: "click", Verdict: domain.Ask},
		},
	}

	cases := []struct {
		origin string
		action string
		want   domain.Verdict
	}{
		{"https://api.docs.example.com", "read", domain.Allow},
		{"https://api.docs.example.com", "click", domain.Ask}, // default Ask when action doesn't match read rule
		{"https://app.example.com", "click", domain.Allow},
		{"https://my.bank.example.com", "read", domain.Deny},
		{"https://billing.example.com", "click", domain.Ask},
		{"https://unknown-site.org", "read", domain.Ask}, // default Ask
	}

	for _, tc := range cases {
		if got := domain.OriginRule(pol, tc.origin, tc.action); got != tc.want {
			t.Fatalf("OriginRule(%q, %q) = %v, want %v", tc.origin, tc.action, got, tc.want)
		}
	}
}

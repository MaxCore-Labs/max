package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"max/internal/domain"
)

func TestMatch(t *testing.T) {
	c := domain.Constraints{
		Equals:    map[string]string{"currency": "USD"},
		OneOf:     map[string][]string{"to": {"a@x", "b@x"}},
		MaxNumber: map[string]float64{"amount": 100},
	}

	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantSub string
	}{
		{
			name:   "all constraints match",
			input:  `{"currency":"USD","to":"a@x","amount":100}`,
			wantOK: true,
		},
		{
			name:    "OneOf mismatch",
			input:   `{"currency":"USD","to":"c@x","amount":50}`,
			wantOK:  false,
			wantSub: "to",
		},
		{
			name:    "MaxNumber exceeded by 0.01",
			input:   `{"currency":"USD","to":"b@x","amount":100.01}`,
			wantOK:  false,
			wantSub: "amount",
		},
		{
			name:    "missing required constrained field",
			input:   `{"currency":"USD","to":"a@x"}`,
			wantOK:  false,
			wantSub: "amount",
		},
		{
			name:    "non-object JSON array",
			input:   `["a@x", 100]`,
			wantOK:  false,
			wantSub: "object",
		},
	}

	for _, tc := range cases {
		ok, reason := c.Match(json.RawMessage(tc.input))
		if ok != tc.wantOK {
			t.Fatalf("%s: Match(%s) = (%v, %q), want %v", tc.name, tc.input, ok, reason, tc.wantOK)
		}
		if !tc.wantOK && tc.wantSub != "" && !strings.Contains(reason, tc.wantSub) {
			t.Fatalf("%s: reason %q does not mention %q", tc.name, reason, tc.wantSub)
		}
	}
}

func TestPolicyUsesGrant(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	pol := domain.AgentPolicy{
		Scope:    domain.AgentScope{Slug: "sales-01"},
		Autonomy: domain.AutonomyAuto,
	}
	grants := []domain.Grant{{
		ID:                 "g-1",
		Connection:         "gmail",
		Tool:               "send_email",
		Constraints:        domain.Constraints{OneOf: map[string][]string{"to": {"vip@client.com"}}},
		AllowedRootOrigins: []domain.RootOrigin{domain.OriginOwner, domain.OriginRoutine},
	}}

	decOK := domain.Decide(pol, domain.ToolCall{
		Name:       "send_email",
		Class:      domain.ToolClassE,
		Connection: "gmail",
		Input:      json.RawMessage(`{"to":"vip@client.com","subject":"Hi"}`),
	}, domain.OriginOwner, grants, now)
	if decOK.Verdict != domain.Allow || decOK.GrantID != "g-1" {
		t.Fatalf("matching grant decision = %+v, want Allow with GrantID=g-1", decOK)
	}

	decMismatch := domain.Decide(pol, domain.ToolCall{
		Name:       "send_email",
		Class:      domain.ToolClassE,
		Connection: "gmail",
		Input:      json.RawMessage(`{"to":"stranger@evil.com","subject":"Hi"}`),
	}, domain.OriginOwner, grants, now)
	if decMismatch.Verdict != domain.Ask || !strings.Contains(decMismatch.Reason, "to") {
		t.Fatalf("mismatch decision = %+v, want Ask mentioning field 'to'", decMismatch)
	}
}

func TestExternalOriginIgnoresGrant(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	pol := domain.AgentPolicy{Scope: domain.AgentScope{Slug: "support-01"}, Autonomy: domain.AutonomyAuto}
	grants := []domain.Grant{{
		ID:                 "g-ext",
		Connection:         "gmail",
		Tool:               "send_email",
		Constraints:        domain.Constraints{OneOf: map[string][]string{"to": {"vip@client.com"}}},
		AllowedRootOrigins: []domain.RootOrigin{domain.OriginOwner, domain.OriginRoutine},
	}}

	dec := domain.Decide(pol, domain.ToolCall{
		Name:       "send_email",
		Class:      domain.ToolClassE,
		Connection: "gmail",
		Input:      json.RawMessage(`{"to":"vip@client.com"}`),
	}, domain.OriginExternal, grants, now)
	if dec.Verdict != domain.Ask {
		t.Fatalf("external origin decision = %+v, want Ask (must ignore default grant)", dec)
	}
}

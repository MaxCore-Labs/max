package domain_test

import (
	"testing"
	"time"

	"max/internal/domain"
)

func TestClassifyBuiltin(t *testing.T) {
	cases := []struct {
		tool string
		want domain.ToolClass
	}{
		{"Read", domain.ToolClassR},
		{"Grep", domain.ToolClassR},
		{"Glob", domain.ToolClassR},
		{"WebSearch", domain.ToolClassR},
		{"WebFetch", domain.ToolClassR},
		{"Write", domain.ToolClassWd},
		{"Edit", domain.ToolClassWd},
		{"Bash", domain.ToolClassX},
		{"mystery_tool", domain.ToolClassUnknown},
	}
	for _, tc := range cases {
		if got := domain.ClassifyBuiltin(tc.tool); got != tc.want {
			t.Errorf("ClassifyBuiltin(%q) = %q, want %q", tc.tool, got, tc.want)
		}
	}
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Minute)

	scope := domain.AgentScope{Slug: "writer-1", TeamSlug: "marketing"}

	suggestPol := domain.AgentPolicy{
		Scope:       scope,
		Autonomy:    domain.AutonomySuggest,
		Web:         true,
		Browser:     domain.BrowserNone,
		Shell:       false,
		EngineLevel: 1,
	}
	autoPol := domain.AgentPolicy{
		Scope:       scope,
		Autonomy:    domain.AutonomyAuto,
		Web:         true,
		Browser:     domain.BrowserNone,
		Shell:       false,
		EngineLevel: 1,
	}
	anonBrowserPol := autoPol
	anonBrowserPol.Browser = domain.BrowserAnon

	ownerBrowserPol := autoPol
	ownerBrowserPol.Browser = domain.BrowserOwner

	devPol := autoPol
	devPol.Shell = true

	validGrant := domain.Grant{
		Connection:         "gmail",
		Tool:               "send_email",
		AllowedRootOrigins: []domain.RootOrigin{domain.OriginOwner, domain.OriginRoutine},
		ExpiresAt:          &future,
	}
	revokedGrant := validGrant
	revokedGrant.Revoked = true

	expiredGrant := validGrant
	expiredGrant.ExpiresAt = &past

	cases := []struct {
		name   string
		policy domain.AgentPolicy
		call   domain.ToolCall
		origin domain.RootOrigin
		grants []domain.Grant
		want   domain.Verdict
	}{
		{
			name:   "R in scope suggest -> Allow",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "Read", Class: domain.ToolClassR, Rel: "wiki/guide.md"},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "R in scope auto -> Allow",
			policy: autoPol,
			call:   domain.ToolCall{Name: "Read", Class: domain.ToolClassR, Rel: "agents/writer-1/notes/a.md"},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "R out of scope -> Deny",
			policy: autoPol,
			call:   domain.ToolCall{Name: "Read", Class: domain.ToolClassR, Rel: "agents/other/notes/a.md"},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "R SearchRoot out of scope -> Deny",
			policy: autoPol,
			call:   domain.ToolCall{Name: "Grep", Class: domain.ToolClassR, SearchRoot: "teams/sales"},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "Wd suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "Write", Class: domain.ToolClassWd, Rel: "wiki/new.md"},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "Wd auto in scope -> Allow",
			policy: autoPol,
			call:   domain.ToolCall{Name: "Write", Class: domain.ToolClassWd, Rel: "wiki/new.md"},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "Wd auto on trusted path -> Deny",
			policy: autoPol,
			call:   domain.ToolCall{Name: "Write", Class: domain.ToolClassWd, Rel: "company/COMPANY.md"},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "Wt suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "Write", Class: domain.ToolClassWt, Rel: "company/COMPANY.md"},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "Wt auto -> Deny",
			policy: autoPol,
			call:   domain.ToolCall{Name: "Write", Class: domain.ToolClassWt, Rel: "company/COMPANY.md"},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "C ask_owner suggest -> Allow",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "ask_owner", Class: domain.ToolClassC},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "C notify_owner suggest -> Allow",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "notify_owner", Class: domain.ToolClassC},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "C task_comment suggest -> Allow",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "task_comment", Class: domain.ToolClassC},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "C delegate suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "delegate", Class: domain.ToolClassC},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "C delegate auto -> Allow",
			policy: autoPol,
			call:   domain.ToolCall{Name: "delegate", Class: domain.ToolClassC},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "P suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "routine_propose", Class: domain.ToolClassP},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "P auto -> Allow",
			policy: autoPol,
			call:   domain.ToolCall{Name: "routine_propose", Class: domain.ToolClassP},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "Ba suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "browser_click", Class: domain.ToolClassBa},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "Ba Browser=none -> Deny",
			policy: autoPol,
			call:   domain.ToolCall{Name: "browser_click", Class: domain.ToolClassBa},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "Ba Browser=anon -> Allow",
			policy: anonBrowserPol,
			call:   domain.ToolCall{Name: "browser_click", Class: domain.ToolClassBa},
			origin: domain.OriginOwner,
			want:   domain.Allow,
		},
		{
			name:   "Ba Browser=owner without grant -> Ask",
			policy: ownerBrowserPol,
			call:   domain.ToolCall{Name: "browser_click", Class: domain.ToolClassBa, Connection: "browser"},
			origin: domain.OriginOwner,
			want:   domain.Ask,
		},
		{
			name:   "E suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "send_email", Class: domain.ToolClassE, Connection: "gmail"},
			origin: domain.OriginOwner,
			grants: []domain.Grant{validGrant},
			want:   domain.Deny,
		},
		{
			name:   "E auto without grant -> Ask",
			policy: autoPol,
			call:   domain.ToolCall{Name: "send_email", Class: domain.ToolClassE, Connection: "gmail"},
			origin: domain.OriginOwner,
			want:   domain.Ask,
		},
		{
			name:   "E auto with matching grant and origin=owner -> Allow",
			policy: autoPol,
			call:   domain.ToolCall{Name: "send_email", Class: domain.ToolClassE, Connection: "gmail"},
			origin: domain.OriginOwner,
			grants: []domain.Grant{validGrant},
			want:   domain.Allow,
		},
		{
			name:   "E auto with matching grant but origin=external -> Ask",
			policy: autoPol,
			call:   domain.ToolCall{Name: "send_email", Class: domain.ToolClassE, Connection: "gmail"},
			origin: domain.OriginExternal,
			grants: []domain.Grant{validGrant},
			want:   domain.Ask,
		},
		{
			name:   "E auto with revoked grant -> Ask",
			policy: autoPol,
			call:   domain.ToolCall{Name: "send_email", Class: domain.ToolClassE, Connection: "gmail"},
			origin: domain.OriginOwner,
			grants: []domain.Grant{revokedGrant},
			want:   domain.Ask,
		},
		{
			name:   "E auto with expired grant -> Ask",
			policy: autoPol,
			call:   domain.ToolCall{Name: "send_email", Class: domain.ToolClassE, Connection: "gmail"},
			origin: domain.OriginOwner,
			grants: []domain.Grant{expiredGrant},
			want:   domain.Ask,
		},
		{
			name:   "X with Shell=false -> Deny",
			policy: autoPol,
			call:   domain.ToolCall{Name: "Bash", Class: domain.ToolClassX},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "X suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "Bash", Class: domain.ToolClassX},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "X with Shell=true without grant -> Ask",
			policy: devPol,
			call:   domain.ToolCall{Name: "Bash", Class: domain.ToolClassX},
			origin: domain.OriginOwner,
			want:   domain.Ask,
		},
		{
			name:   "Unknown suggest -> Deny",
			policy: suggestPol,
			call:   domain.ToolCall{Name: "custom_unknown", Class: domain.ToolClassUnknown},
			origin: domain.OriginOwner,
			want:   domain.Deny,
		},
		{
			name:   "Unknown auto -> Ask",
			policy: autoPol,
			call:   domain.ToolCall{Name: "custom_unknown", Class: domain.ToolClassUnknown},
			origin: domain.OriginOwner,
			want:   domain.Ask,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := domain.Decide(tc.policy, tc.call, tc.origin, tc.grants, now)
			if dec.Verdict != tc.want {
				t.Fatalf("Verdict = %v (%q), want %v", dec.Verdict, dec.Reason, tc.want)
			}
			if dec.Verdict != domain.Allow && dec.Reason == "" {
				t.Fatalf("non-Allow Decision (%v) must have non-empty Reason", dec.Verdict)
			}
		})
	}
}

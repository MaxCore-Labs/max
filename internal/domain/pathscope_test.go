package domain_test

import (
	"testing"

	"max/internal/domain"
)

func TestCanWrite(t *testing.T) {
	scope := domain.AgentScope{
		Slug:     "writer-1",
		TeamSlug: "marketing",
	}

	cases := []struct {
		path string
		want bool
	}{
		{"agents/writer-1/notes/a.md", true},
		{"agents/writer-1/artifacts/x/y.md", true},
		{"agents/writer-1/AGENT.md", false},
		{"agents/other/notes/a.md", false},
		{"wiki/x.md", true},
		{"sources/x.md", true},
		{"memory/facts/x.md", false},
		{"company/COMPANY.md", false},
		{"teams/marketing/TEAM.md", false},
		{"skills/a/SKILL.md", false},
		{"routines/r.md", false},
		{".claude/settings.json", false},
		{"CLAUDE.md", false},
		{"skills/_proposed/a/SKILL.md", true},
		{"../etc/passwd", false},
		{"/etc/passwd", false},
		{"agents/writer-1/notes/../../other/x", false},
		{"journal/2026-11-20.md", false},
		{".env", false},
		{"wiki/.env.local", false},
		{"data/max.sqlite3", false},
		{"agents/writer-1/notes/a\x00.md", false},
		{`C:\Windows\System32\cmd.exe`, false},
	}

	for _, tc := range cases {
		got, reason := domain.CanWrite(scope, tc.path)
		if got != tc.want {
			t.Errorf("CanWrite(%q) = (%v, %q), want %v", tc.path, got, reason, tc.want)
		}
		if !got && reason == "" {
			t.Errorf("CanWrite(%q) denied with empty reason", tc.path)
		}
	}
}

func TestCanRead(t *testing.T) {
	scope := domain.AgentScope{
		Slug:      "writer-1",
		TeamSlug:  "marketing",
		ReadExtra: []string{"agents/shared-researcher/artifacts"},
	}

	cases := []struct {
		path string
		want bool
	}{
		{"company/COMPANY.md", true},
		{"company", true},
		{"teams/marketing/TEAM.md", true},
		{"teams/marketing", true},
		{"wiki/x.md", true},
		{"wiki", true},
		{"sources/x.md", true},
		{"journal/2026-11.md", true},
		{"skills/a/SKILL.md", true},
		{"skills/_proposed/a/SKILL.md", true},
		{"memory/facts/x.md", true},
		{"agents/writer-1/AGENT.md", true},
		{"agents/writer-1/notes/a.md", true},
		{"agents/writer-1", true},
		{"agents/shared-researcher/artifacts/report.md", true},
		{"agents/shared-researcher/notes/secret.md", false},
		{"agents/other/notes/a.md", false},
		{"teams/sales/TEAM.md", false},
		{".claude/settings.json", false},
		{"data/max.sqlite3", false},
		{"wiki/.env", false},
		{"../etc/passwd", false},
		{"/etc/passwd", false},
	}

	for _, tc := range cases {
		if got := domain.CanRead(scope, tc.path); got != tc.want {
			t.Errorf("CanRead(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestCleanRelUnicode(t *testing.T) {
	// NFD: 'e' + combining circumflex (\u0302) + combining acute (\u0301)
	nfd := "wiki/Tie\u0302\u0301ng.md"
	// NFC: precomposed 'ế' (\u1ebf)
	nfc := "wiki/Ti\u1ebfng.md"

	relNFD, ok1 := domain.CleanRel(nfd)
	relNFC, ok2 := domain.CleanRel(nfc)
	if !ok1 || !ok2 {
		t.Fatalf("CleanRel ok = (%v, %v), want (true, true)", ok1, ok2)
	}
	if relNFD != relNFC {
		t.Fatalf("CleanRel(NFD)=%q != CleanRel(NFC)=%q", relNFD, relNFC)
	}

	for _, bad := range []string{
		"",
		".",
		"..",
		"../etc/passwd",
		"/etc/passwd",
		`\etc\passwd`,
		"C:/foo",
		"wiki/\x00bad.md",
		"agents/writer-1/notes/../../other/x",
		"agents/writer-1/notes/../../../etc/passwd",
	} {
		if got, ok := domain.CleanRel(bad); ok {
			t.Errorf("CleanRel(%q) = (%q, true), want ok=false", bad, got)
		}
	}
}

func TestClassifyZones(t *testing.T) {
	if z := domain.Classify("company/COMPANY.md"); z != domain.ZoneTrusted {
		t.Fatalf("company/COMPANY.md = %v, want ZoneTrusted", z)
	}
	if z := domain.Classify("skills/a/SKILL.md"); z != domain.ZoneTrusted {
		t.Fatalf("skills/a/SKILL.md = %v, want ZoneTrusted", z)
	}
	if z := domain.Classify("skills/_proposed/a/SKILL.md"); z != domain.ZoneData {
		t.Fatalf("skills/_proposed/a/SKILL.md = %v, want ZoneData", z)
	}
	if z := domain.Classify("wiki/.env.production"); z != domain.ZoneOutside {
		t.Fatalf("wiki/.env.production = %v, want ZoneOutside", z)
	}
	if z := domain.Classify("../escape"); z != domain.ZoneOutside {
		t.Fatalf("../escape = %v, want ZoneOutside", z)
	}
}

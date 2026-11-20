package prompt_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"max/internal/app/prompt"
	"max/internal/domain"
)

func TestKernelFileUnderCap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "prompts", "kernel.md"))
	if err != nil {
		t.Fatalf("read prompts/kernel.md: %v", err)
	}
	content := string(raw)
	if len(content) == 0 {
		t.Fatal("prompts/kernel.md is empty")
	}
	if utf8.RuneCountInString(content) > 6000 || len(content) > 6000 {
		t.Fatalf("prompts/kernel.md exceeds 6000 chars: bytes=%d runes=%d", len(content), utf8.RuneCountInString(content))
	}
	for _, required := range []string{
		"untrusted reference data",
		"task or routine",
		"CAPTCHA",
		"End your turn immediately",
		"delegate",
		"artifacts/",
		"git commit",
		"truthfully",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("prompts/kernel.md missing mandatory rule phrase %q", required)
		}
	}
}

func TestSystemOrder(t *testing.T) {
	out, warnings := prompt.BuildSystem(prompt.SystemInput{
		Kernel:  "KERNEL_BLOCK",
		Company: "COMPANY_BLOCK",
		Team:    "TEAM_BLOCK",
		Role:    "ROLE_BLOCK",
		SkillIndex: []prompt.SkillRef{
			{Slug: "cite-sources", Summary: "Cite primary URLs", Path: "skills/cite-sources/SKILL.md"},
		},
	})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	markers := []string{"KERNEL_BLOCK", "COMPANY_BLOCK", "TEAM_BLOCK", "ROLE_BLOCK", "cite-sources"}
	lastIdx := -1
	for _, m := range markers {
		idx := strings.Index(out, m)
		if idx < 0 {
			t.Fatalf("missing marker %q in:\n%s", m, out)
		}
		if idx <= lastIdx {
			t.Fatalf("marker %q appeared out of order at %d (prev %d)", m, idx, lastIdx)
		}
		lastIdx = idx
	}
}

func makeNLines(prefix string, n int) string {
	lines := make([]string, n)
	for i := range n {
		lines[i] = fmt.Sprintf("%s-line-%d", prefix, i+1)
	}
	return strings.Join(lines, "\n")
}

func TestSystemCaps(t *testing.T) {
	out, warnings := prompt.BuildSystem(prompt.SystemInput{
		Kernel:  "Kernel ok",
		Company: makeNLines("comp", 151),
		Team:    makeNLines("team", 81),
		Role:    makeNLines("role", 201),
	})
	if strings.Contains(out, "comp-line-151") || !strings.Contains(out, "comp-line-150") {
		t.Fatal("company block was not capped at 150 lines")
	}
	if strings.Contains(out, "team-line-81") || !strings.Contains(out, "team-line-80") {
		t.Fatal("team block was not capped at 80 lines")
	}
	if strings.Contains(out, "role-line-201") || !strings.Contains(out, "role-line-200") {
		t.Fatal("role block was not capped at 200 lines")
	}
	joinedWarn := strings.ToLower(strings.Join(warnings, " | "))
	for _, blockName := range []string{"company", "team", "role"} {
		if !strings.Contains(joinedWarn, blockName) {
			t.Fatalf("expected warning mentioning %q, got %v", blockName, warnings)
		}
	}
}

func TestSystemHasNoClock(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "prompts", "kernel.md"))
	if err != nil {
		t.Fatalf("read kernel: %v", err)
	}
	out, _ := prompt.BuildSystem(prompt.SystemInput{
		Kernel:  string(raw),
		Company: "Acme Co",
		Team:    "Core Team",
		Role:    "Chief of Staff",
	})
	year := time.Now().UTC().Format("2006")
	if strings.Contains(out, year) {
		t.Fatalf("system prompt must not embed current year %s", year)
	}
	clockRe := regexp.MustCompile(`\b\d{2}:\d{2}(:\d{2})?\b`)
	if clockRe.MatchString(out) {
		t.Fatalf("system prompt contains clock string: %s", clockRe.FindString(out))
	}
}

func TestRunMessageBlocks(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	bigRecall := make([]string, 8)
	for i := range bigRecall {
		bigRecall[i] = fmt.Sprintf("recall-%d: %s", i+1, strings.Repeat("r", 1500))
	}
	notes := makeNLines("note-"+strings.Repeat("n", 80), 95)

	msg := prompt.BuildRunMessage(prompt.RunInput{
		Now:          now,
		TZ:           "Asia/Ho_Chi_Minh",
		TaskSummary:  "Draft weekly report",
		GoalAncestry: "Q4 Growth -> Content cadence",
		QuotaNote:    "Short window: 12% used",
		Notes:        notes,
		Recall:       bigRecall,
		Inbox: []prompt.InboxItem{
			{ID: "w-1", Kind: domain.WakeTaskAssigned, Summary: "Task assigned: Draft weekly report"},
		},
	})

	blocks := []string{"<context>", "<notes>", "<recall>", "<inbox>"}
	prev := -1
	for _, b := range blocks {
		idx := strings.Index(msg, b)
		if idx < 0 {
			t.Fatalf("missing block %s in:\n%s", b, msg)
		}
		if idx <= prev {
			t.Fatalf("block %s out of order", b)
		}
		prev = idx
	}
	if strings.Contains(msg, "recall-6:") {
		t.Fatal("Recall was not capped to at most 5 items before trimming")
	}
	if strings.Contains(msg, "line-81") {
		t.Fatal("Notes was not capped at 80 lines")
	}
	if utf8.RuneCountInString(msg) > 12000 || len(msg) > 12000 {
		t.Fatalf("BuildRunMessage exceeded 12000 chars: bytes=%d runes=%d", len(msg), utf8.RuneCountInString(msg))
	}
}

func TestHistoryOnlyWhenEngineChanged(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	without := prompt.BuildRunMessage(prompt.RunInput{Now: now, TZ: "UTC"})
	if strings.Contains(without, "<history>") {
		t.Fatal("unexpected <history> block when History is empty")
	}

	with := prompt.BuildRunMessage(prompt.RunInput{
		Now:     now,
		TZ:      "UTC",
		History: "Task created by owner; 2 prior comments.",
	})
	if !strings.Contains(with, "<history>") || !strings.Contains(with, "2 prior comments") {
		t.Fatalf("expected <history> block when History is non-empty, got:\n%s", with)
	}
}

func TestOnlyMountedToolsMentioned(t *testing.T) {
	out, _ := prompt.BuildSystem(prompt.SystemInput{
		Kernel:  "Kernel",
		Company: "Acme",
		Team:    "Research",
		Role: strings.Join([]string{
			"Synthesize findings in wiki/.",
			"Use browser_navigate and browser_snapshot to inspect competitor pricing.",
			"Save key updates via note_update.",
		}, "\n"),
		MountedTools: []string{"Read", "Write", "note_update", "ask_owner"},
		SkillIndex: []prompt.SkillRef{
			{
				Slug:          "web-scrape",
				Summary:       "Scrape pages with browser_snapshot",
				Path:          "skills/web-scrape/SKILL.md",
				RequiredTools: []string{"browser_snapshot"},
			},
			{
				Slug:          "clean-notes",
				Summary:       "Keep structured markdown notes",
				Path:          "skills/clean-notes/SKILL.md",
				RequiredTools: []string{"note_update"},
			},
		},
	})

	if strings.Contains(out, "browser_") {
		t.Fatalf("system prompt leaked unmounted browser_ tool reference:\n%s", out)
	}
	if strings.Contains(out, "web-scrape") {
		t.Fatalf("system prompt included skill requiring unmounted tool:\n%s", out)
	}
	if !strings.Contains(out, "clean-notes") {
		t.Fatalf("expected mounted skill clean-notes in:\n%s", out)
	}
}

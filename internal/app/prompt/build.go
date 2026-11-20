package prompt

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"max/internal/domain"
)

const (
	maxKernelChars  = 6000
	maxCompanyLines = 150
	maxTeamLines    = 80
	maxRoleLines    = 200
	maxNotesLines   = 80
	maxRecallItems  = 5
	maxRunMsgChars  = 12000
)

// SkillRef describes a vault skill entry eligible for inclusion in the system prompt index.
type SkillRef struct {
	Slug          string
	Summary       string
	Path          string
	RequiredTools []string
}

// InboxItem summarizes one drained wakeup inside the <inbox> block of a run message.
type InboxItem struct {
	ID      domain.ID
	Kind    domain.WakeKind
	Summary string
}

// SystemInput provides raw blocks assembled into a stable cache-friendly system prompt (Spec 8.4.6).
type SystemInput struct {
	Kernel       string
	Company      string
	Team         string
	Role         string
	SkillIndex   []SkillRef
	MountedTools []string
}

// RunInput provides dynamic runtime data assembled into the bounded per-run user message (<= 12,000 chars).
type RunInput struct {
	Now          time.Time
	TZ           string
	TaskSummary  string
	GoalAncestry string
	QuotaNote    string
	Notes        string
	Recall       []string
	Inbox        []InboxItem
	History      string
}

var toolTokenRe = regexp.MustCompile(`\b(?:browser_[a-z0-9_]+|memory_[a-z0-9_]+|task_[a-z0-9_]+|routine_propose|agent_propose|instruction_propose|remember_fact|note_update|ask_owner|notify_owner|delegate|message_agent|request_review|WebSearch|WebFetch|Bash)\b`)

// BuildSystem concatenates Kernel, Company, Team, Role, and filtered SkillIndex while enforcing line/char caps.
func BuildSystem(in SystemInput) (text string, warnings []string) {
	kernel := strings.TrimSpace(in.Kernel)
	if utf8.RuneCountInString(kernel) > maxKernelChars || len(kernel) > maxKernelChars {
		kernel = truncateChars(kernel, maxKernelChars)
		warnings = append(warnings, "kernel block exceeded 6000 chars and was truncated")
	}

	company, truncated := capLines(strings.TrimSpace(in.Company), maxCompanyLines)
	if truncated {
		warnings = append(warnings, "company block exceeded 150 lines and was truncated")
	}

	team, truncated := capLines(strings.TrimSpace(in.Team), maxTeamLines)
	if truncated {
		warnings = append(warnings, "team block exceeded 80 lines and was truncated")
	}

	roleRaw := strings.TrimSpace(in.Role)
	if in.MountedTools != nil {
		roleRaw = filterUnmountedToolLines(roleRaw, in.MountedTools)
	}
	role, truncated := capLines(roleRaw, maxRoleLines)
	if truncated {
		warnings = append(warnings, "role block exceeded 200 lines and was truncated")
	}

	var sections []string
	if kernel != "" {
		sections = append(sections, kernel)
	}
	if company != "" {
		sections = append(sections, "## Company\n"+company)
	}
	if team != "" {
		sections = append(sections, "## Team\n"+team)
	}
	if role != "" {
		sections = append(sections, "## Role\n"+role)
	}
	if len(in.MountedTools) > 0 {
		sections = append(sections, "## Mounted Tools\n"+strings.Join(in.MountedTools, ", "))
	}

	skills := filterSkills(in.SkillIndex, in.MountedTools)
	if len(skills) > 0 {
		var sb strings.Builder
		sb.WriteString("## Skill Index\n")
		for _, sk := range skills {
			_, _ = fmt.Fprintf(&sb, "- `%s` (%s): %s\n", sk.Slug, sk.Path, sk.Summary)
		}
		sections = append(sections, strings.TrimRight(sb.String(), "\n"))
	}

	return strings.Join(sections, "\n\n"), warnings
}

// BuildRunMessage builds the bounded <context>, optional <history>, <notes>, <recall>, and <inbox> blocks.
func BuildRunMessage(in RunInput) string {
	loc := time.UTC
	if in.TZ != "" {
		if l, err := time.LoadLocation(in.TZ); err == nil {
			loc = l
		}
	}
	nowStr := in.Now.In(loc).Format(time.RFC3339)

	var ctxLines []string
	ctxLines = append(ctxLines, "time: "+nowStr)
	if strings.TrimSpace(in.GoalAncestry) != "" {
		ctxLines = append(ctxLines, "goal_ancestry: "+strings.TrimSpace(in.GoalAncestry))
	}
	if strings.TrimSpace(in.TaskSummary) != "" {
		ctxLines = append(ctxLines, "task: "+strings.TrimSpace(in.TaskSummary))
	}
	if strings.TrimSpace(in.QuotaNote) != "" {
		ctxLines = append(ctxLines, "quota: "+strings.TrimSpace(in.QuotaNote))
	}
	ctxBlock := "<context>\n" + strings.Join(ctxLines, "\n") + "\n</context>"

	historyContent := strings.TrimSpace(in.History)
	notesContent, _ := capLines(strings.TrimSpace(in.Notes), maxNotesLines)

	recallItems := in.Recall
	if len(recallItems) > maxRecallItems {
		recallItems = recallItems[:maxRecallItems]
	}
	recallContent := strings.Join(recallItems, "\n")

	var inboxLines []string
	for _, it := range in.Inbox {
		inboxLines = append(inboxLines, fmt.Sprintf("- [%s] (%s) %s", it.Kind, it.ID, strings.TrimSpace(it.Summary)))
	}
	inboxContent := strings.Join(inboxLines, "\n")

	assemble := func(hist, notes, recall, inbox string) string {
		var parts []string
		parts = append(parts, ctxBlock)
		if hist != "" {
			parts = append(parts, "<history>\n"+hist+"\n</history>")
		}
		parts = append(parts, "<notes>\n"+notes+"\n</notes>")
		parts = append(parts, "<recall>\n"+recall+"\n</recall>")
		parts = append(parts, "<inbox>\n"+inbox+"\n</inbox>")
		return strings.Join(parts, "\n\n")
	}

	out := assemble(historyContent, notesContent, recallContent, inboxContent)
	if withinBudget(out, maxRunMsgChars) {
		return out
	}

	// Step 1: Trim <recall> first.
	for len(recallItems) > 0 && !withinBudget(out, maxRunMsgChars) {
		recallItems = recallItems[:len(recallItems)-1]
		recallContent = strings.Join(recallItems, "\n")
		out = assemble(historyContent, notesContent, recallContent, inboxContent)
	}
	if withinBudget(out, maxRunMsgChars) {
		return out
	}

	// Step 2: Trim <notes> next.
	if notesContent != "" {
		over := len(out) - maxRunMsgChars
		keep := max(0, len(notesContent)-over-32)
		notesContent = truncateChars(notesContent, keep)
		out = assemble(historyContent, notesContent, recallContent, inboxContent)
	}
	if withinBudget(out, maxRunMsgChars) {
		return out
	}

	// Step 3: Trim <history> then <inbox> if still exceeding 12,000 chars.
	if historyContent != "" {
		over := len(out) - maxRunMsgChars
		keep := max(0, len(historyContent)-over-32)
		historyContent = truncateChars(historyContent, keep)
		out = assemble(historyContent, notesContent, recallContent, inboxContent)
	}
	if !withinBudget(out, maxRunMsgChars) {
		over := len(out) - maxRunMsgChars
		keep := max(0, len(inboxContent)-over-32)
		inboxContent = truncateChars(inboxContent, keep)
		out = assemble(historyContent, notesContent, recallContent, inboxContent)
	}
	return truncateChars(out, maxRunMsgChars)
}

func withinBudget(s string, maxChars int) bool {
	return len(s) <= maxChars && utf8.RuneCountInString(s) <= maxChars
}

func capLines(s string, maxLines int) (string, bool) {
	if s == "" {
		return "", false
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s, false
	}
	return strings.Join(lines[:maxLines], "\n"), true
}

func truncateChars(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes && utf8.RuneCountInString(s) <= maxBytes {
		return s
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		rl := utf8.RuneLen(r)
		if b.Len()+rl > maxBytes || count+1 > maxBytes {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

func filterUnmountedToolLines(role string, mounted []string) string {
	allowed := make(map[string]bool, len(mounted))
	for _, t := range mounted {
		allowed[t] = true
	}
	lines := strings.Split(role, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		refs := toolTokenRe.FindAllString(line, -1)
		unmounted := false
		for _, ref := range refs {
			if !allowed[ref] {
				unmounted = true
				break
			}
		}
		if !unmounted {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func filterSkills(skills []SkillRef, mounted []string) []SkillRef {
	if mounted == nil {
		return skills
	}
	allowed := make(map[string]bool, len(mounted))
	for _, t := range mounted {
		allowed[t] = true
	}
	var out []SkillRef
	for _, sk := range skills {
		ok := true
		for _, req := range sk.RequiredTools {
			if !allowed[req] {
				ok = false
				break
			}
		}
		if ok && mounted != nil {
			for _, ref := range toolTokenRe.FindAllString(sk.Summary, -1) {
				if !allowed[ref] {
					ok = false
					break
				}
			}
		}
		if ok {
			out = append(out, sk)
		}
	}
	return out
}

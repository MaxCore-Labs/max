package prompt

import (
	"fmt"
	"strings"
	"time"
)

const (
	maxHistoryEvents      = 20
	defaultHistoryMaxChar = 12000
)

// TaskEvent summarizes a durable task event for cross-engine <history> carry-over.
type TaskEvent struct {
	Kind      string
	Actor     string
	Body      string
	CreatedAt time.Time
}

// BuildHistory formats taskSummary and at most the 20 most recent events into a string bounded by maxChars (<= 12,000).
func BuildHistory(taskSummary string, events []TaskEvent, maxChars int) string {
	if maxChars <= 0 || maxChars > defaultHistoryMaxChar {
		maxChars = defaultHistoryMaxChar
	}

	recent := events
	if len(recent) > maxHistoryEvents {
		recent = recent[len(recent)-maxHistoryEvents:]
	}

	var b strings.Builder
	if sum := strings.TrimSpace(taskSummary); sum != "" {
		b.WriteString("Task: ")
		b.WriteString(sum)
		if len(recent) > 0 {
			b.WriteString("\n")
		}
	}

	if len(recent) > 0 {
		b.WriteString("Recent events:\n")
		for _, ev := range recent {
			ts := ""
			if !ev.CreatedAt.IsZero() {
				ts = ev.CreatedAt.UTC().Format(time.RFC3339) + " "
			}
			actor := strings.TrimSpace(ev.Actor)
			if actor == "" {
				actor = "system"
			}
			line := fmt.Sprintf("- %s[%s/%s] %s\n", ts, ev.Kind, actor, strings.TrimSpace(ev.Body))
			if !withinBudget(b.String()+line, maxChars) {
				rem := maxChars - len(b.String())
				if rem > 16 {
					b.WriteString(truncateChars(line, rem))
				}
				break
			}
			b.WriteString(line)
		}
	}
	return truncateChars(strings.TrimSpace(b.String()), maxChars)
}

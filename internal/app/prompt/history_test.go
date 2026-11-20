package prompt_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"max/internal/app/prompt"
)

func TestHistoryCaps(t *testing.T) {
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	events := make([]prompt.TaskEvent, 30)
	for i := range 30 {
		events[i] = prompt.TaskEvent{
			Kind:      "comment",
			Actor:     "researcher",
			Body:      fmt.Sprintf("Evt-%02d: %s", i+1, strings.Repeat("findings ", 40)),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
	}

	// 1. Event count cap: max 20 most recent events (Evt-01..Evt-10 dropped, Evt-30 present)
	out := prompt.BuildHistory("Launch Q4 campaign", events, 12000)
	if strings.Contains(out, "Evt-01:") || strings.Contains(out, "Evt-10:") {
		t.Fatalf("expected oldest events beyond 20-event cap dropped, got:\n%s", out)
	}
	if !strings.Contains(out, "Evt-30:") {
		t.Fatalf("expected newest event Evt-30 retained, got:\n%s", out)
	}
	if utf8.RuneCountInString(out) > 12000 || len(out) > 12000 {
		t.Fatalf("exceeded 12000-char cap: len=%d", len(out))
	}

	// 2. Tight character budget cap
	tight := prompt.BuildHistory("Summary header", events, 600)
	if utf8.RuneCountInString(tight) > 600 || len(tight) > 600 {
		t.Fatalf("BuildHistory(..., 600) exceeded 600 chars: len=%d", len(tight))
	}
}

package domain

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const catchUpGrace = 5 * time.Minute

// RoutineFile represents a parsed routines/<slug>.md file (Spec 8.11).
type RoutineFile struct {
	Slug        string
	Agent       string
	Schedule    string
	At          *time.Time
	Enabled     bool
	Origin      string // trusted | inbound
	TimeoutMin  int
	Notify      string
	CatchUp     string // run_once | skip
	Connections []string
	Body        string
}

type rawRoutineFrontmatter struct {
	Agent       string   `yaml:"agent"`
	Schedule    string   `yaml:"schedule"`
	At          string   `yaml:"at"`
	Enabled     *bool    `yaml:"enabled"`
	Origin      string   `yaml:"origin"`
	TimeoutMin  int      `yaml:"timeout_min"`
	Notify      string   `yaml:"notify"`
	CatchUp     string   `yaml:"catch_up"`
	Connections []string `yaml:"connections"`
}

// ParseRoutine parses and validates raw routines/<slug>.md bytes.
func ParseRoutine(slug string, raw []byte) (RoutineFile, error) {
	if !utf8.Valid(raw) {
		return RoutineFile{}, fmt.Errorf("invalid utf-8 in routine %q", slug)
	}
	norm := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return RoutineFile{}, fmt.Errorf("missing opening --- frontmatter fence in routine %q", slug)
	}
	rest := norm[len("---\n"):]
	endIdx := strings.Index(rest, "\n---\n")
	fenceLen := len("\n---\n")
	if endIdx < 0 {
		if strings.HasSuffix(rest, "\n---") {
			endIdx = len(rest) - len("\n---")
			fenceLen = len("\n---")
		} else {
			return RoutineFile{}, fmt.Errorf("missing closing --- frontmatter fence in routine %q", slug)
		}
	}
	yamlText := rest[:endIdx]
	body := rest[endIdx+fenceLen:]

	var rf rawRoutineFrontmatter
	dec := yaml.NewDecoder(bytes.NewReader([]byte(yamlText)))
	dec.KnownFields(true)
	if err := dec.Decode(&rf); err != nil {
		return RoutineFile{}, fmt.Errorf("invalid yaml in routine %q: %w", slug, err)
	}

	agent := strings.TrimSpace(rf.Agent)
	if agent == "" {
		return RoutineFile{}, fmt.Errorf("routine %q missing required field 'agent'", slug)
	}
	schedStr := strings.TrimSpace(rf.Schedule)
	var atPtr *time.Time
	if strings.TrimSpace(rf.At) != "" {
		parsedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(rf.At))
		if err != nil {
			return RoutineFile{}, fmt.Errorf("invalid 'at' timestamp in routine %q: %w", slug, err)
		}
		atPtr = &parsedAt
	}
	if schedStr == "" && atPtr == nil {
		return RoutineFile{}, fmt.Errorf("routine %q requires either 'schedule' or 'at'", slug)
	}
	if schedStr != "" {
		if _, err := cron.ParseStandard(schedStr); err != nil {
			return RoutineFile{}, fmt.Errorf("invalid cron schedule %q in routine %q: %w", schedStr, slug, err)
		}
	}

	enabled := true
	if rf.Enabled != nil {
		enabled = *rf.Enabled
	}
	origin := strings.TrimSpace(rf.Origin)
	if origin == "" {
		origin = "trusted"
	}
	if origin != "trusted" && origin != "inbound" {
		return RoutineFile{}, fmt.Errorf("invalid origin %q in routine %q (allowed: trusted, inbound)", origin, slug)
	}
	catchUp := strings.TrimSpace(rf.CatchUp)
	if catchUp == "" {
		catchUp = "run_once"
	}
	if catchUp != "run_once" && catchUp != "skip" {
		return RoutineFile{}, fmt.Errorf("invalid catch_up %q in routine %q (allowed: run_once, skip)", catchUp, slug)
	}
	timeout := rf.TimeoutMin
	if timeout <= 0 {
		timeout = 20
	}

	return RoutineFile{
		Slug:        slug,
		Agent:       agent,
		Schedule:    schedStr,
		At:          atPtr,
		Enabled:     enabled,
		Origin:      origin,
		TimeoutMin:  timeout,
		Notify:      strings.TrimSpace(rf.Notify),
		CatchUp:     catchUp,
		Connections: rf.Connections,
		Body:        body,
	}, nil
}

// DueSlots evaluates schedule in tz between lastFired and now, returning at most 1 due slot.
// Backward clock jumps never re-fire slots; downtime > catchUpGrace honours catchUp ("run_once" vs "skip").
func DueSlots(schedule string, tz *time.Location, lastFired, now time.Time, catchUp string) ([]time.Time, error) {
	if tz == nil {
		tz = time.UTC
	}
	sched, err := cron.ParseStandard(strings.TrimSpace(schedule))
	if err != nil {
		return nil, fmt.Errorf("parse cron %q: %w", schedule, err)
	}

	nowInTZ := now.In(tz)
	var cursor time.Time
	if lastFired.IsZero() {
		cursor = nowInTZ.Truncate(time.Minute).Add(-time.Second)
	} else {
		if !now.After(lastFired) {
			return nil, nil
		}
		cursor = lastFired.In(tz)
	}

	var latest time.Time
	missed := 0
	const maxScan = 20000
	for range maxScan {
		next := sched.Next(cursor)
		if next.IsZero() || next.After(nowInTZ) {
			break
		}
		latest = next
		missed++
		cursor = next
	}
	if missed == 0 {
		return nil, nil
	}

	isDowntime := missed > 1 || nowInTZ.Sub(latest) > catchUpGrace
	if isDowntime && catchUp == "skip" {
		return nil, nil
	}
	return []time.Time{latest}, nil
}

// NextCronPeriod returns the next scheduled fire time after from in tz along with the interval to the following slot.
func NextCronPeriod(schedule string, tz *time.Location, from time.Time) (time.Time, time.Duration, error) {
	if tz == nil {
		tz = time.UTC
	}
	sched, err := cron.ParseStandard(strings.TrimSpace(schedule))
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("parse cron %q: %w", schedule, err)
	}
	t1 := sched.Next(from.In(tz))
	t2 := sched.Next(t1)
	period := t2.Sub(t1)
	if period <= 0 {
		period = time.Hour
	}
	return t1, period, nil
}

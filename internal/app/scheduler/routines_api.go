package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
)

var (
	// ErrRoutineNotFound indicates routines/<slug>.md does not exist in the vault.
	ErrRoutineNotFound = errors.New("routine not found")
	routineSlugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// RoutineValidationError reports a field-specific validation error on routine upsert.
type RoutineValidationError struct {
	Field   string
	Message string
}

func (e *RoutineValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Message)
}

// RoutineBusyError indicates the previous run/task for this routine is still active.
type RoutineBusyError struct {
	Slug   string
	TaskID domain.ID
	RunID  domain.ID
}

func (e *RoutineBusyError) Error() string {
	if e.RunID != "" {
		return fmt.Sprintf("routine %q is already running (runId=%s, taskId=%s)", e.Slug, e.RunID, e.TaskID)
	}
	return fmt.Sprintf("routine %q already has active task %s", e.Slug, e.TaskID)
}

// RoutineUpsertInput describes parameters for creating or updating routines/<slug>.md.
type RoutineUpsertInput struct {
	Slug        string
	Agent       string
	Schedule    string
	At          string
	Enabled     bool
	Origin      string
	TimeoutMin  int
	Notify      string
	CatchUp     string
	Connections []string
	Body        string
}

type encodedRoutineFrontmatter struct {
	Agent       string   `yaml:"agent"`
	Schedule    string   `yaml:"schedule,omitempty"`
	At          string   `yaml:"at,omitempty"`
	Enabled     bool     `yaml:"enabled"`
	Origin      string   `yaml:"origin,omitempty"`
	TimeoutMin  int      `yaml:"timeout_min,omitempty"`
	Notify      string   `yaml:"notify,omitempty"`
	CatchUp     string   `yaml:"catch_up,omitempty"`
	Connections []string `yaml:"connections,omitempty"`
}

// ValidateAndEncodeRoutine validates field constraints (naming the exact invalid field) and encodes routines/<slug>.md.
func ValidateAndEncodeRoutine(in RoutineUpsertInput) ([]byte, domain.RoutineFile, error) {
	slug := strings.TrimSpace(in.Slug)
	if !routineSlugRe.MatchString(slug) {
		return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "slug", Message: "must match ^[a-z0-9][a-z0-9_-]{0,63}$"}
	}
	agent := strings.TrimSpace(in.Agent)
	if agent == "" {
		return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "agent", Message: "agent is required"}
	}
	sched := strings.TrimSpace(in.Schedule)
	atStr := strings.TrimSpace(in.At)
	if sched == "" && atStr == "" {
		return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "schedule", Message: "either schedule or at is required"}
	}
	if sched != "" {
		if _, err := cron.ParseStandard(sched); err != nil {
			return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "schedule", Message: err.Error()}
		}
	}
	if atStr != "" {
		if _, err := time.Parse(time.RFC3339, atStr); err != nil {
			return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "at", Message: err.Error()}
		}
	}
	origin := strings.TrimSpace(in.Origin)
	if origin == "" {
		origin = "trusted"
	}
	if origin != "trusted" && origin != "inbound" {
		return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "origin", Message: "must be trusted or inbound"}
	}
	catchUp := strings.TrimSpace(in.CatchUp)
	if catchUp == "" {
		catchUp = "run_once"
	}
	if catchUp != "run_once" && catchUp != "skip" {
		return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "catchUp", Message: "must be run_once or skip"}
	}
	timeout := in.TimeoutMin
	if timeout <= 0 {
		timeout = 20
	}

	fm := encodedRoutineFrontmatter{
		Agent:       agent,
		Schedule:    sched,
		At:          atStr,
		Enabled:     in.Enabled,
		Origin:      origin,
		TimeoutMin:  timeout,
		Notify:      strings.TrimSpace(in.Notify),
		CatchUp:     catchUp,
		Connections: in.Connections,
	}
	var fmBuf bytes.Buffer
	enc := yaml.NewEncoder(&fmBuf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		return nil, domain.RoutineFile{}, err
	}
	_ = enc.Close()

	body := in.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(fmBuf.Bytes())
	out.WriteString("---\n")
	out.WriteString(body)

	parsed, err := domain.ParseRoutine(slug, out.Bytes())
	if err != nil {
		return nil, domain.RoutineFile{}, &RoutineValidationError{Field: "schedule", Message: err.Error()}
	}
	return out.Bytes(), parsed, nil
}

// TriggerRoutineNow immediately creates a task and wakes the routine's agent without waiting for its cron schedule.
// If the previous routine task is still non-final, it returns *RoutineBusyError carrying the active run/task ID.
func (s *Scheduler) TriggerRoutineNow(ctx context.Context, slug string, now time.Time) (domain.ID, domain.ID, error) {
	if s.vault == nil {
		return "", "", ErrRoutineNotFound
	}
	raw, err := s.vault.Read(ctx, "routines/"+slug+".md")
	if err != nil {
		return "", "", ErrRoutineNotFound
	}
	rf, err := domain.ParseRoutine(slug, raw)
	if err != nil {
		return "", "", &RoutineValidationError{Field: "schedule", Message: err.Error()}
	}
	ag, ok, err := s.store.Read().GetAgentBySlug(ctx, rf.Agent)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", &RoutineValidationError{Field: "agent", Message: fmt.Sprintf("agent %q not found", rf.Agent)}
	}

	st, hasState, err := s.store.Read().GetRoutineState(ctx, slug)
	if err != nil {
		return "", "", err
	}
	if hasState && st.ActiveTaskID != nil && *st.ActiveTaskID != "" {
		prevTk, pok, perr := s.store.Read().GetTask(ctx, *st.ActiveTaskID)
		if perr == nil && pok && !domain.TaskStatus(prevTk.Status).Final() {
			runID := ""
			if prevTk.CheckoutRunID != nil && *prevTk.CheckoutRunID != "" {
				runID = *prevTk.CheckoutRunID
			} else if activeRuns, _ := s.store.Read().ListActiveRunsByTasks(ctx, []domain.ID{prevTk.ID}); len(activeRuns) > 0 {
				runID = activeRuns[0].ID
			} else if ag.ActiveRunID != nil {
				runID = *ag.ActiveRunID
			}
			return "", "", &RoutineBusyError{Slug: slug, TaskID: prevTk.ID, RunID: runID}
		}
	}

	nowMs := now.UnixMilli()
	rootOrigin := domain.OriginRoutine
	if rf.Origin == "inbound" {
		rootOrigin = domain.OriginExternal
	}

	var taskID, wakeupID domain.ID
	err = s.store.WithTx(ctx, func(tx app.Tx) error {
		taskID = s.ids.New()
		agID := ag.ID
		if err := tx.InsertTask(ctx, app.TaskRecord{
			ID:              taskID,
			RootID:          taskID,
			RootOrigin:      rootOrigin,
			Depth:           0,
			Title:           fmt.Sprintf("Routine: %s", rf.Slug),
			Description:     strings.TrimSpace(rf.Body),
			Status:          string(domain.TaskTodo),
			AssigneeAgentID: &agID,
			CreatedAt:       nowMs,
			UpdatedAt:       nowMs,
		}); err != nil {
			return err
		}
		convID, err := tx.EnsureTaskConversation(ctx, ag.ID, taskID, s.ids.New())
		if err != nil {
			return err
		}
		if s.actors != nil {
			payload, _ := json.Marshal(map[string]any{"routine": rf.Slug, "task_id": taskID, "manual": true})
			wid, _, err := s.actors.Wake(ctx, tx, actor.WakeInput{
				AgentID:        ag.ID,
				Kind:           domain.WakeRoutine,
				Priority:       domain.PrioRoutine,
				ConversationID: &convID,
				Payload:        payload,
			})
			if err != nil {
				return err
			}
			wakeupID = wid
		}
		st.Slug = rf.Slug
		st.LastFiredAt = &nowMs
		st.ActiveTaskID = &taskID
		st.BadFileReported = false
		st.UpdatedAt = nowMs
		return tx.UpsertRoutineState(ctx, st)
	})
	if err != nil {
		return "", "", err
	}
	if s.notify != nil {
		s.notify()
	}
	return taskID, wakeupID, nil
}

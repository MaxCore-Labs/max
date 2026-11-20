package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"max/internal/domain"
)

// InvariantViolation describes a single detected invariant breach in SQLite state.
type InvariantViolation struct {
	Code    string
	Message string
}

// InvariantCheckParams configures optional state assertions during CheckDBInvariants.
type InvariantCheckParams struct {
	Quiescent bool
	Recovered bool
	SlotCaps  map[domain.EngineName]int
}

// ExecRaw executes raw SQL on the writer connection (for test fault injection and loadtest bulk seeding).
func (s *Store) ExecRaw(ctx context.Context, query string, args ...any) error {
	_, err := s.wdb.ExecContext(ctx, query, args...)
	return err
}

// CountWakeupsDoneAndTotal returns counts of done wakeups, terminal (done+dropped) wakeups, and total wakeups.
func (s *Store) CountWakeupsDoneAndTotal(ctx context.Context) (doneCount, totalCount int, err error) {
	err = s.rdb.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'done' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM wakeups
	`).Scan(&doneCount, &totalCount)
	return doneCount, totalCount, err
}

// CountWakeupsStatusSummary returns (done, dropped, nonTerminal) counts across wakeups.
func (s *Store) CountWakeupsStatusSummary(ctx context.Context) (done, dropped, nonTerminal int, err error) {
	err = s.rdb.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'done' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'dropped' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('queued','claimed') THEN 1 ELSE 0 END), 0)
		FROM wakeups
	`).Scan(&done, &dropped, &nonTerminal)
	return done, dropped, nonTerminal, err
}

// CheckDBInvariants evaluates Spec section 11 database invariants (I1-I7, I12) on a consistent read-only snapshot.
func (s *Store) CheckDBInvariants(ctx context.Context, p InvariantCheckParams) ([]InvariantViolation, error) {
	tx, err := s.rdb.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin ro tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var out []InvariantViolation
	add := func(code, msg string) {
		out = append(out, InvariantViolation{Code: code, Message: msg})
	}

	// I1: <= 1 running run per agent + bijection with agents.active_run_id.
	var dupRunning, mismatchActive int
	_ = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT agent_id FROM runs WHERE status = 'running' AND ended_at IS NULL
			GROUP BY agent_id HAVING COUNT(*) > 1
		)`).Scan(&dupRunning)
	_ = tx.QueryRowContext(ctx, `
		SELECT (
			SELECT COUNT(*) FROM runs r JOIN agents a ON a.id = r.agent_id
			WHERE r.status = 'running' AND r.ended_at IS NULL AND COALESCE(a.active_run_id, '') <> r.id
		) + (
			SELECT COUNT(*) FROM agents a
			LEFT JOIN runs r ON r.id = a.active_run_id AND r.status = 'running' AND r.ended_at IS NULL
			WHERE a.active_run_id IS NOT NULL AND (r.id IS NULL OR r.agent_id <> a.id)
		)`).Scan(&mismatchActive)
	if dupRunning > 0 || mismatchActive > 0 {
		add("I1", fmt.Sprintf("duplicate running=%d, active_run_id mismatch=%d", dupRunning, mismatchActive))
	}

	// I2: Live running runs per engine <= configured slots, and teams.running accurate & <= max_running.
	var teamMismatch int
	_ = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM teams t
		LEFT JOIN (
			SELECT a.team_id, COUNT(*) AS c FROM runs r JOIN agents a ON a.id = r.agent_id
			WHERE r.status = 'running' AND r.ended_at IS NULL GROUP BY a.team_id
		) x ON x.team_id = t.id
		WHERE t.running <> COALESCE(x.c, 0) OR t.running > t.max_running OR t.running < 0`).Scan(&teamMismatch)
	if teamMismatch > 0 {
		add("I2", fmt.Sprintf("%d team(s) have inconsistent or over-cap running counter", teamMismatch))
	}
	for eng, capSlots := range p.SlotCaps {
		var live int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE engine = ? AND status = 'running' AND ended_at IS NULL`, string(eng)).Scan(&live)
		if live > capSlots {
			add("I2", fmt.Sprintf("engine %s running=%d > slots=%d", eng, live, capSlots))
		}
	}

	// I3: Wakeup status <-> run_id consistency, claimed <-> running bijection, and quiescent completion.
	var badWakeRefs, claimedMismatch int
	_ = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wakeups
		WHERE (status IN ('queued','dropped') AND run_id IS NOT NULL)
		   OR (status = 'done' AND run_id IS NULL)`).Scan(&badWakeRefs)
	_ = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wakeups w
		LEFT JOIN runs r ON r.id = w.run_id AND r.status = 'running' AND r.ended_at IS NULL
		WHERE w.status = 'claimed' AND (w.run_id IS NULL OR r.id IS NULL OR r.agent_id <> w.agent_id)`).Scan(&claimedMismatch)
	if badWakeRefs > 0 || claimedMismatch > 0 {
		add("I3", fmt.Sprintf("invalid wakeup run_id refs=%d, orphan claimed=%d", badWakeRefs, claimedMismatch))
	}
	if p.Quiescent {
		var nonTerminal int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status NOT IN ('done','dropped')`).Scan(&nonTerminal)
		if nonTerminal > 0 {
			add("I3", fmt.Sprintf("%d wakeup(s) did not reach terminal done/dropped state", nonTerminal))
		}
	}

	// I4: No duplicate class E tool_calls with identical idempotency_key in ('started','done','ambiguous').
	var dupIdemTools int
	_ = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT idempotency_key FROM tool_calls
			WHERE class = 'E' AND idempotency_key IS NOT NULL AND status IN ('started','done','ambiguous')
			GROUP BY idempotency_key HAVING COUNT(*) > 1
		)`).Scan(&dupIdemTools)
	if dupIdemTools > 0 {
		add("I4", fmt.Sprintf("%d duplicate class-E idempotency_key execution(s)", dupIdemTools))
	}

	// I5: Every task checkout_run_id points to a currently running run and is unique across active tasks.
	var badCheckout, dupCheckout int
	_ = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks tk
		LEFT JOIN runs r ON r.id = tk.checkout_run_id AND r.status = 'running' AND r.ended_at IS NULL
		WHERE tk.checkout_run_id IS NOT NULL AND r.id IS NULL`).Scan(&badCheckout)
	_ = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT checkout_run_id FROM tasks WHERE checkout_run_id IS NOT NULL
			GROUP BY checkout_run_id HAVING COUNT(*) > 1
		)`).Scan(&dupCheckout)
	if badCheckout > 0 || dupCheckout > 0 {
		add("I5", fmt.Sprintf("stale checkout=%d, duplicate checkout=%d", badCheckout, dupCheckout))
	}

	// I6: No consumed approval lacks decided_at.
	var unapprovedConsumed int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE status = 'consumed' AND decided_at IS NULL`).Scan(&unapprovedConsumed)
	if unapprovedConsumed > 0 {
		add("I6", fmt.Sprintf("%d approval(s) consumed without decided_at", unapprovedConsumed))
	}

	// I7: After recovery or quiescence, 0 runs stuck in running state.
	if p.Recovered || p.Quiescent {
		var stuckRuns int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE status = 'running'`).Scan(&stuckRuns)
		if stuckRuns > 0 {
			add("I7", fmt.Sprintf("%d run(s) still stuck in running state", stuckRuns))
		}
	}

	// I12: No run touched_paths outside agent write scope or inside trusted zone.
	rows, qErr := tx.QueryContext(ctx, `
		SELECT r.id, a.slug, r.touched_paths
		FROM runs r JOIN agents a ON a.id = r.agent_id
		WHERE r.touched_paths <> '[]' AND r.touched_paths <> ''`)
	if qErr == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var runID, slug, rawPaths string
			if rows.Scan(&runID, &slug, &rawPaths) == nil {
				var paths []string
				if json.Unmarshal([]byte(rawPaths), &paths) == nil {
					scope := domain.AgentScope{Slug: slug}
					for _, rel := range paths {
						if ok, reason := domain.CanWrite(scope, rel); !ok {
							add("I12", fmt.Sprintf("run %s (%s) touched out-of-scope path %q: %s", runID, slug, rel, reason))
						}
					}
				}
			}
		}
	}

	return out, nil
}

package domain_test

import (
	"testing"

	"max/internal/domain"
)

func TestNextStepAfter(t *testing.T) {
	cases := []struct {
		name       string
		status     domain.RunStatus
		taskTurns  int
		idleRuns   int
		attempts   int
		wantReq    bool
		wantCont   bool
		wantBlock  bool
		wantReason string
	}{
		{
			name:   "succeeded finishes wakeup",
			status: domain.RunSucceeded, taskTurns: 10, idleRuns: 0, attempts: 1,
		},
		{
			name:   "parked waits for external event",
			status: domain.RunParked, taskTurns: 5, idleRuns: 0, attempts: 1,
		},
		{
			name:   "turn_limit under cap schedules continue",
			status: domain.RunTurnLimit, taskTurns: 40, idleRuns: 0, attempts: 1,
			wantCont: true,
		},
		{
			name:   "timed_out under cap schedules continue",
			status: domain.RunTimedOut, taskTurns: 80, idleRuns: 1, attempts: 1,
			wantCont: true,
		},
		{
			name:   "turn_limit at 400 task turns blocks task as stalled",
			status: domain.RunTurnLimit, taskTurns: 400, idleRuns: 0, attempts: 1,
			wantBlock: true, wantReason: "stalled",
		},
		{
			name:   "turn_limit with 3 idle runs blocks task as stalled",
			status: domain.RunTurnLimit, taskTurns: 60, idleRuns: 3, attempts: 1,
			wantBlock: true, wantReason: "stalled",
		},
		{
			name:   "quota_stopped requeues wakeup without blocking",
			status: domain.RunQuotaStopped, taskTurns: 20, idleRuns: 0, attempts: 3,
			wantReq: true,
		},
		{
			name:   "interrupted below 3 attempts requeues",
			status: domain.RunInterrupted, taskTurns: 5, idleRuns: 0, attempts: 2,
			wantReq: true,
		},
		{
			name:   "failed at 3 attempts drops and blocks with error",
			status: domain.RunFailed, taskTurns: 5, idleRuns: 0, attempts: 3,
			wantBlock: true, wantReason: "error",
		},
		{
			name:   "interrupted at 3 attempts drops and blocks with error",
			status: domain.RunInterrupted, taskTurns: 5, idleRuns: 0, attempts: 3,
			wantBlock: true, wantReason: "error",
		},
		{
			name:   "cancelled neither requeues nor continues by default",
			status: domain.RunCancelled, taskTurns: 5, idleRuns: 0, attempts: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, cont, blk, reason := domain.NextStepAfter(tc.status, tc.taskTurns, tc.idleRuns, tc.attempts)
			if req != tc.wantReq || cont != tc.wantCont || blk != tc.wantBlock || reason != tc.wantReason {
				t.Fatalf("NextStepAfter(%s, turns=%d, idle=%d, attempts=%d) = (%v,%v,%v,%q), want (%v,%v,%v,%q)",
					tc.status, tc.taskTurns, tc.idleRuns, tc.attempts,
					req, cont, blk, reason,
					tc.wantReq, tc.wantCont, tc.wantBlock, tc.wantReason)
			}
		})
	}
}

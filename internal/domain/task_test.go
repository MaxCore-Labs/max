package domain

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from TaskStatus
		to   TaskStatus
		want bool
	}{
		// Valid transitions from todo
		{TaskTodo, TaskInProgress, true},
		{TaskTodo, TaskBlocked, true},
		{TaskTodo, TaskWaitingOwner, true},
		{TaskTodo, TaskDone, true},
		{TaskTodo, TaskCancelled, true},
		{TaskTodo, TaskInReview, false},
		{TaskTodo, TaskTodo, false},

		// Valid transitions from in_progress
		{TaskInProgress, TaskTodo, true},
		{TaskInProgress, TaskBlocked, true},
		{TaskInProgress, TaskWaitingOwner, true},
		{TaskInProgress, TaskInReview, true},
		{TaskInProgress, TaskDone, true},
		{TaskInProgress, TaskCancelled, true},
		{TaskInProgress, TaskInProgress, false},

		// Valid transitions from blocked
		{TaskBlocked, TaskTodo, true},
		{TaskBlocked, TaskInProgress, true},
		{TaskBlocked, TaskWaitingOwner, true},
		{TaskBlocked, TaskCancelled, true},
		{TaskBlocked, TaskDone, false},
		{TaskBlocked, TaskInReview, false},
		{TaskBlocked, TaskBlocked, false},

		// Valid transitions from waiting_owner
		{TaskWaitingOwner, TaskTodo, true},
		{TaskWaitingOwner, TaskInProgress, true},
		{TaskWaitingOwner, TaskBlocked, true},
		{TaskWaitingOwner, TaskDone, true},
		{TaskWaitingOwner, TaskCancelled, true},
		{TaskWaitingOwner, TaskInReview, false},

		// Valid transitions from in_review
		{TaskInReview, TaskDone, true},
		{TaskInReview, TaskInProgress, true},
		{TaskInReview, TaskWaitingOwner, true},
		{TaskInReview, TaskBlocked, true},
		{TaskInReview, TaskCancelled, true},
		{TaskInReview, TaskTodo, false},

		// Final states cannot transition anywhere
		{TaskDone, TaskTodo, false},
		{TaskDone, TaskInProgress, false},
		{TaskDone, TaskCancelled, false},
		{TaskDone, TaskDone, false},
		{TaskCancelled, TaskTodo, false},
		{TaskCancelled, TaskInProgress, false},
		{TaskCancelled, TaskDone, false},
		{TaskCancelled, TaskCancelled, false},

		// Unknown states
		{TaskStatus("bogus"), TaskTodo, false},
		{TaskTodo, TaskStatus("bogus"), false},
	}

	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}

	if !TaskDone.Final() || !TaskCancelled.Final() {
		t.Fatalf("expected done and cancelled to be final")
	}
	for _, nonFinal := range []TaskStatus{TaskTodo, TaskInProgress, TaskBlocked, TaskWaitingOwner, TaskInReview} {
		if nonFinal.Final() {
			t.Errorf("expected %q to be non-final", nonFinal)
		}
	}
}

func TestInitialStatus(t *testing.T) {
	st, reason := InitialStatus(0)
	if st != TaskTodo || reason != nil {
		t.Fatalf("InitialStatus(0) = (%q, %v), want (todo, nil)", st, reason)
	}

	st, reason = InitialStatus(2)
	if st != TaskBlocked || reason == nil || *reason != BlockedDependency {
		t.Fatalf("InitialStatus(2) = (%q, %v), want (blocked, dependency)", st, reason)
	}
}

func TestAutoUnblockable(t *testing.T) {
	if !AutoUnblockable(BlockedDependency) {
		t.Fatal("expected dependency to be auto-unblockable")
	}
	for _, r := range []BlockedReason{BlockedStalled, BlockedError, BlockedNeedsOwner, BlockedReason("other")} {
		if AutoUnblockable(r) {
			t.Errorf("expected %q not to be auto-unblockable", r)
		}
	}
}

func TestHasCycle(t *testing.T) {
	// A blocked by B, B blocked by C
	edges := map[ID][]ID{
		"A": {"B"},
		"B": {"C"},
	}

	if !HasCycle(edges, "A", "A") {
		t.Fatal("self-dependency A->A must be detected as cycle")
	}
	if !HasCycle(edges, "C", "A") {
		t.Fatal("adding C blocked by A must create cycle A->B->C->A")
	}
	if HasCycle(edges, "D", "A") {
		t.Fatal("adding D blocked by A must not create cycle")
	}
	if HasCycle(edges, "A", "D") {
		t.Fatal("adding A blocked by D must not create cycle")
	}
}

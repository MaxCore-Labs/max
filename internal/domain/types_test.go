package domain

import "testing"

func TestEngineMask(t *testing.T) {
	cases := []struct {
		in   []EngineName
		want int
	}{
		{[]EngineName{EngineAgy}, 4},
		{[]EngineName{EngineClaude, EngineCodex}, 3},
		{[]EngineName{EngineAgy, EngineClaude}, 5},
		{[]EngineName{EngineAgy, EngineClaude, EngineCodex, EngineAgy}, 7},
		{nil, 0},
		{[]EngineName{}, 0},
	}
	for _, tc := range cases {
		if got := EngineMask(tc.in); got != tc.want {
			t.Fatalf("EngineMask(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestCoalescible(t *testing.T) {
	coalescible := []WakeKind{WakeMessage, WakeTaskUpdate, WakeHeartbeat, WakeRoutine}
	for _, k := range coalescible {
		if !k.Coalescible() {
			t.Fatalf("expected %q to be coalescible", k)
		}
	}
	nonCoalescible := []WakeKind{
		WakeTaskAssigned,
		WakeChildDone,
		WakeApprovalResult,
		WakeOwnerReply,
		WakeContinue,
		WakeSystem,
	}
	for _, k := range nonCoalescible {
		if k.Coalescible() {
			t.Fatalf("expected %q NOT to be coalescible", k)
		}
	}
}

func TestTerminal(t *testing.T) {
	if RunRunning.Terminal() {
		t.Fatal("expected running NOT to be terminal")
	}
	terminals := []RunStatus{
		RunSucceeded,
		RunFailed,
		RunCancelled,
		RunTimedOut,
		RunTurnLimit,
		RunParked,
		RunQuotaStopped,
		RunInterrupted,
	}
	for _, s := range terminals {
		if !s.Terminal() {
			t.Fatalf("expected %q to be terminal", s)
		}
	}
}

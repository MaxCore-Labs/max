package domain

const (
	MaxTurnsPerRun   = 40
	MaxTurnsPerTask  = 400
	MaxIdleRuns      = 3
	MaxWakeAttempts  = 3
	ColdResumeTokens = 60000
)

// NextStepAfter implements the Spec 8.3 post-run transition table purely.
func NextStepAfter(status RunStatus, taskTurns, idleRuns, attempts int) (requeue, continueWake, blockTask bool, reason string) {
	switch status {
	case RunSucceeded, RunParked, RunCancelled:
		return false, false, false, ""
	case RunTurnLimit, RunTimedOut:
		if taskTurns >= MaxTurnsPerTask || idleRuns >= MaxIdleRuns {
			return false, false, true, "stalled"
		}
		return false, true, false, ""
	case RunQuotaStopped:
		return true, false, false, ""
	case RunInterrupted, RunFailed:
		if attempts >= MaxWakeAttempts {
			return false, false, true, "error"
		}
		return true, false, false, ""
	default:
		return false, false, false, ""
	}
}

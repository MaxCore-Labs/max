package scenario_test

import (
	"context"
	"testing"

	"max/internal/testutil/scenario"
)

func TestLoadtestMultiAgent(t *testing.T) {
	ctx := context.Background()
	res, err := scenario.RunMultiAgentSim(ctx, scenario.MultiAgentConfig{
		Agents: 200,
		Teams:  5,
		Goals:  15,
		Slots:  6,
	})
	if err != nil {
		t.Fatalf("RunMultiAgentSim: %v", err)
	}
	if res.Delegates == 0 || res.Messages == 0 || res.Reviews == 0 {
		t.Fatalf("expected delegate/message/review activity, got %+v", res)
	}
	if len(res.Violations) > 0 {
		t.Fatalf("invariant violations (I1-I14): %+v", res.Violations)
	}
}

func TestStormScenario(t *testing.T) {
	ctx := context.Background()
	res, err := scenario.RunStormSim(ctx, scenario.StormConfig{
		NormalAgents: 5,
		Slots:        4,
	})
	if err != nil {
		t.Fatalf("RunStormSim: %v", err)
	}
	// Theoretical upper bound: 4 storm agents * 12 runs/hr cap + 5 normal agents = 53 runs max.
	const maxAllowedRuns = 55
	if res.TotalRuns > maxAllowedRuns {
		t.Fatalf("storm was not bounded: totalRuns = %d, want <= %d", res.TotalRuns, maxAllowedRuns)
	}
	if res.InboxAlerts == 0 {
		t.Fatalf("expected >= 1 Inbox escalation/cap alert from storm, got 0")
	}
	if res.NormalTasksDone != 5 {
		t.Fatalf("normalTasksDone = %d, want 5 (normal agents must still be served)", res.NormalTasksDone)
	}
	if len(res.Violations) > 0 {
		t.Fatalf("invariant violations after storm: %+v", res.Violations)
	}
}

func TestMonitorSparse24h(t *testing.T) {
	ctx := context.Background()
	res, err := scenario.RunMonitor24hSim(ctx, 200)
	if err != nil {
		t.Fatalf("RunMonitor24hSim: %v", err)
	}
	if res.AgentsCreated != 200 {
		t.Fatalf("agentsCreated = %d, want 200", res.AgentsCreated)
	}
	if len(res.Violations) > 0 {
		t.Fatalf("invariant violations in 24h monitor sim: %+v", res.Violations)
	}
}

func TestMonitor1000AgentsSoak(t *testing.T) {
	ctx := context.Background()
	res, err := scenario.RunMonitor1000SoakSim(ctx, 1000, 14)
	if err != nil {
		t.Fatalf("RunMonitor1000SoakSim: %v", err)
	}
	t.Logf("1000-agent 14d soak metrics: %+v", res)
	if res.AgentsCreated != 1000 {
		t.Fatalf("agentsCreated = %d, want 1000", res.AgentsCreated)
	}
	if res.RunsExecuted == 0 {
		t.Fatal("expected >= 1 heartbeat run executed across 14 simulated days")
	}
	if res.OperationalInterventions != 0 {
		t.Fatalf("operationalInterventions = %d, want 0", res.OperationalInterventions)
	}
	if len(res.Violations) > 0 {
		t.Fatalf("invariant violations (I1-I14): %+v", res.Violations)
	}
}

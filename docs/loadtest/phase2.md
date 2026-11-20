# Phase 2 Multi-Agent, Storm & Chaos Benchmark Report

Date: 2026-11-20
Task: `P2.17` (`max loadtest --scenario multi`, storm containment, 200-monitor 24h schedule, chaos recovery)

## 1. High-Scale Multi-Agent Queue & Task Benchmark

Command executed:

```sh
max loadtest --scenario multi --agents 5000 --wakeups 100000 --slots 8 --sleep-min-ms 1 --sleep-max-ms 5
```

### Measured Results

| Metric | Target | Measured | Status |
|---|---|---|---|
| Agents seeded across role templates & teams | `5,000` | `5,000` | PASS |
| Multi-kind wakeups (`task_assigned`, `task_update`, `child_done`, `routine`, `message`) | `100,000` | `100,000` (`done=100000`, `dropped=0`, `pending=0`) | PASS |
| Runs executed (`fakecli` ACP) | - | `5,000` | PASS |
| Wall-clock time | - | `61.43 s` | PASS |
| Wakeup throughput | - | `1,627.8 wakeups/s` | PASS |
| `ClaimBatch` latency `p50` | - | `7.80 ms` | PASS |
| `ClaimBatch` latency `p95` (`I8`) | `< 20 ms` | `10.75 ms` | PASS |
| `ClaimBatch` latency `p99` (`I8`) | `< 50 ms` | `13.78 ms` | PASS |
| Go runtime `Sys` memory | `<= 512 MiB` | `25.7 MiB` | PASS |
| Invariant violations (`I1`-`I14`) | `0` | `0` | PASS |

## 2. Multi-Agent Crash-Recovery Chaos Harness

Command executed:

```sh
max loadtest --scenario multi --agents 3000 --wakeups 30000 --slots 4 --sleep-min-ms 1 --sleep-max-ms 5 --kill-every 3s --chaos-rounds 10
```

| Metric | Target | Measured | Status |
|---|---|---|---|
| Abrupt worker kills | `10` | `10` kills | PASS |
| Post-recovery `I7` checks | `0` violations | `0` violations | PASS |
| Final wakeups accounting (`I3`) | `30,000` | `done=30000`, `dropped=0`, `pending=0` | PASS |
| Drain `ClaimBatch` `p50` / `p95` / `p99` | `p95 < 20 ms`, `p99 < 50 ms` | `3.14 ms` / `4.35 ms` / `5.12 ms` | PASS |
| Go runtime `Sys` memory | `<= 512 MiB` | `21.7 MiB` | PASS |
| Invariant violations (`I1`-`I14`) | `0` | `0` | PASS |

## 3. Coordination Scenarios (`internal/testutil/scenario`)

- **`TestLoadtestMultiAgent`** (`200` agents, `5` teams, `delegate`, `message_agent`, blocker unblocking, 2-round `reviewer` gate): `PASS` in `1.30 s`, `0` violations across `I1`-`I14` (including `I13` clean vault git working tree).
- **`TestStormScenario`** (`4` rogue agents attempting infinite ring messages, pair spam, and recursive delegation alongside `5` normal agents): halted deterministically at coordination & `12 runs/hr` ceilings (`TotalRuns <= 53`), escalated alert written to `inbox_items`, all `5/5` normal tasks completed (`done`), `0` invariant violations.
- **`TestMonitorSparse24h`** (`200` bulk-instantiated `monitor` agents on `0 */6 * * *` jittered ±10% across 288 virtual-clock steps = `24h`): `PASS` in `8.30 s`, `0` violations across `I1`-`I14`.

## 4. Live 3-Process `agy` Launch Scenario (Spec 10.1)

- Status: **Gated on explicit owner quota confirmation** (zero subscription quota spent during automated suite execution). Ready to run via `max serve` with `MAX_SLOTS_AGY=3` upon owner approval.

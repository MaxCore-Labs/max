# Phase 4 Capacity & 1,000-Agent Soak Report

Date: 2026-11-20
Task: `P4.04` (System Capacity Panel, Queue Backlog Diagnostics, and 1,000-Agent 14-Day Sparse `monitor` Soak)

## 1. Queue Backlog Age & Reason Telemetry (`getStatus` + `CapacityPanel`)

`GET /api/v1/status` (`queue.backlog` and `queue.quotaDelayed`) and `web/src/lib/components/CapacityPanel.svelte` on `/system` now expose real-time capacity diagnostics per priority class (`100` Interactive, `50` Task, `30` Routine, `10` Maintenance/Heartbeat):

- **Engine Slots**: Live `used / slots` occupancy and rate-limit hold state per configured CLI engine.
- **Oldest Queued Wakeup Age (`oldestAgeSec`)**: Derived via single-pass SQLite window aggregation (`ListQueuedBacklogStats`) over `wakeups WHERE status = 'queued'`.
- **Primary Wait Reason (`reason`)**: Classified deterministically per priority class into:
  - `cooldown` — oldest due agent is within post-run cooldown (`cooldown_until > now` for non-interactive priorities).
  - `quota_limited` — agent is `quota_paused` or all eligible enabled engines are held by rolling-window share (`70%` background cap) or active `limited_until`.
  - `slots_full` — agent already holds an active run, team `running >= max_running`, or all eligible enabled engine slots (respecting interactive slot reservation) are occupied.
  - `ready` — eligible for immediate dispatch on the next claim tick.

Verified by:
- Go integration test: `TestStatusBacklogAge` (`internal/adapter/httpapi/handlers_phase4_test.go`) — `PASS`.
- Component test: `CapacityPanel.test.ts` (`web/src/lib/components/CapacityPanel.test.ts`) — `PASS`.

## 2. 1,000-Agent 14-Day Sparse `monitor` Soak Simulation (`fakeengine` / `fakecli`)

Command executed:

```sh
go test -v ./internal/testutil/scenario/... -run TestMonitor1000AgentsSoak
```

### Workload Profile
- **Agents instantiated**: `1,000` vault-backed `monitor` agents (`agents/mon-1/AGENT.md` .. `agents/mon-1000/AGENT.md`) created in bulk via `agentsync.InstantiateBulk` and committed to the git vault using chunked staging + `--pathspec-from-file`.
- **Schedule**: Sparse weekly cron `0 9 * * 1` with deterministic per-agent ±10% jitter across `14` simulated days (`336` hourly scheduler sweeps).
- **Active vs. Idle S5 Precondition**: `40` monitors seeded with recurring watch tasks across both weeks; `960` idle monitors verified to trigger `0` wasted CLI spawns when no pending work/diff exists.
- **Concurrency Cap**: `3` `agy` slots (`1` reserved for owner interactive chat, `2` available for background monitor runs).

### Measured Results

| Metric | Target | Measured | Status |
|---|---|---|---|
| Agents defined & synced from git vault | `1,000` | `1,000` | PASS |
| Simulated soak duration | `14 days` (`336 h`) | `14 days` (`336` sweeps) | PASS |
| Max wakeups scheduled in the same second | De-clustered via ±10% jitter | `2` / `1,000` agents | PASS |
| Heartbeat runs executed (`fakeengine`) | - | `3,611` runs | PASS |
| Total simulated quota usage | Tracked in `quota_ledger` & `usage_daily` | `541,650` tokens | PASS |
| `ClaimBatch` latency `p50` | - | `2.02 ms` | PASS |
| `ClaimBatch` latency `p95` (`I8`) | `< 20 ms` | `4.00 ms` | PASS |
| `ClaimBatch` latency `p99` (`I8`) | `< 50 ms` | `6.00 ms` | PASS |
| Operational interventions required | `0` | `0` | PASS |
| Invariant violations (`I1`–`I14`, incl. `I13` clean git vault) | `0` | `0` | PASS |

## 3. Live 2-Week Real `agy` Soak Gate

- Automated 14-day virtual-clock soak with `fakeengine`/`fakecli` passed with `0` operational interventions, `0` invariant violations, and `0` live subscription quota consumed.
- Continuous 2-week wall-clock soak against live `agy` subscription processes is ready to run via `max serve` and gated on explicit owner confirmation before spending live subscription quota.

# Phase 1 Load & Chaos Benchmark Report

Date: 2026-11-20
Task: `P1.29` (`max loadtest` & `scripts/chaos.sh`)

## 1. High-Scale Actor & Queue Benchmark

Command executed:

```sh
max loadtest --agents 5000 --wakeups 100000 --slots 8 --sleep-min-ms 1 --sleep-max-ms 5
```

### Measured Results

| Metric | Target | Measured | Status |
|---|---|---|---|
| Agents seeded | `5,000` | `5,000` | PASS |
| Wakeups queued & coalesced | `100,000` | `100,000` (`done=100000`, `dropped=0`, `pending=0`) | PASS |
| Runs executed (`fakecli` ACP) | - | `5,000` | PASS |
| Wall-clock time | - | `69.89 s` | PASS |
| Wakeup throughput | - | `1,430.9 wakeups/s` | PASS |
| `ClaimBatch` latency `p50` | - | `7.89 ms` | PASS |
| `ClaimBatch` latency `p95` (`I8`) | `< 20 ms` | `13.70 ms` | PASS |
| `ClaimBatch` latency `p99` (`I8`) | `< 50 ms` | `23.82 ms` | PASS |
| Go runtime `Sys` memory | `<= 512 MiB` | `26.0 MiB` | PASS |
| Invariant violations (`I1`-`I12`) | `0` | `0` | PASS |

Key SQLite optimizations validated under load:
- Batched `DrainDueWakeupsForConv` update (`WHERE agent_id = ? AND id IN (...)`) on the `WITHOUT ROWID` `wakeups` table clustered by `(agent_id, id)`.
- Post-seed `ANALYZE` so SQLite's query planner selects `agents_ready` and `wakeups_mailbox` partial indexes.

## 2. 2-Minute Crash-Recovery Chaos Harness (`scripts/chaos.sh`)

Command executed:

```sh
bash scripts/chaos.sh
# Equivalent parameters:
# agents=6000, wakeups=60000, slots=4, kill-every=5s, rounds=24 (120s kill phase + drain)
```

### Measured Results

| Metric | Target | Measured | Status |
|---|---|---|---|
| Abrupt worker kills (`SIGKILL` / `TerminateProcess`) | `24` (every `5s` for `2m`) | `24` kills | PASS |
| Post-recovery `I7` checks (after every restart) | `0` violations | `0` violations | PASS |
| Final wakeups accounting (`I3`) | `done + dropped = 60,000`, `pending = 0` | `done=60000`, `dropped=0`, `pending=0` | PASS |
| Drain phase `ClaimBatch` `p50` / `p95` / `p99` | `p95 < 20 ms`, `p99 < 50 ms` | `2.39 ms` / `6.94 ms` / `15.44 ms` | PASS |
| Go runtime `Sys` memory | `<= 512 MiB` | `17.6 MiB` | PASS |
| Total invariant violations (`I1`-`I12`) | `0` | `0` | PASS |

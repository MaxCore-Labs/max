#!/usr/bin/env bash
set -euo pipefail

AGENTS="${CHAOS_AGENTS:-6000}"
WAKEUPS="${CHAOS_WAKEUPS:-60000}"
SLOTS="${CHAOS_SLOTS:-4}"
KILL_EVERY="${CHAOS_KILL_EVERY:-5s}"
ROUNDS="${CHAOS_ROUNDS:-24}"
SLEEP_MIN="${CHAOS_SLEEP_MIN_MS:-40}"
SLEEP_MAX="${CHAOS_SLEEP_MAX_MS:-80}"

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

EXT=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) EXT=".exe" ;;
esac

MAX_BIN="${WORK_DIR}/max${EXT}"
FAKECLI_BIN="${WORK_DIR}/fakecli${EXT}"

echo "[chaos] building max and fakecli..."
CGO_ENABLED=0 go build -o "${MAX_BIN}" ./cmd/max
CGO_ENABLED=0 go build -o "${FAKECLI_BIN}" ./internal/testutil/fakecli

echo "[chaos] running chaos harness (agents=${AGENTS}, wakeups=${WAKEUPS}, slots=${SLOTS}, kill-every=${KILL_EVERY}, rounds=${ROUNDS})..."
"${MAX_BIN}" loadtest \
  --data-dir "${WORK_DIR}/data" \
  --fakecli-bin "${FAKECLI_BIN}" \
  --agents "${AGENTS}" \
  --wakeups "${WAKEUPS}" \
  --slots "${SLOTS}" \
  --sleep-min-ms "${SLEEP_MIN}" \
  --sleep-max-ms "${SLEEP_MAX}" \
  --kill-every "${KILL_EVERY}" \
  --chaos-rounds "${ROUNDS}"

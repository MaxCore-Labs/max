#!/usr/bin/env bash
set -euo pipefail

DEST_DIR="${1:-./backups}"
DATA_DIR="${MAX_DATA_DIR:-./data}"
MAX_BIN="${MAX_BIN:-max}"
DB_FILE="${DATA_DIR}/max.sqlite3"

if [[ ! -f "${DB_FILE}" ]]; then
  echo "error: database file not found at ${DB_FILE}" >&2
  exit 1
fi

mkdir -p "${DEST_DIR}"
STAGE_DIR="$(mktemp -d)"
trap 'rm -rf "${STAGE_DIR}"' EXIT

SNAP_DB="${STAGE_DIR}/max.sqlite3"
if command -v sqlite3 >/dev/null 2>&1; then
  ESC_SNAP="${SNAP_DB//\'/\'\'}"
  sqlite3 "${DB_FILE}" "VACUUM INTO '${ESC_SNAP}';"
else
  MAX_DATA_DIR="${DATA_DIR}" "${MAX_BIN}" admin backup-db "${SNAP_DB}" >/dev/null
fi
chmod 600 "${SNAP_DB}"

if [[ -d "${DATA_DIR}/agent-home" ]]; then
  (cd "${DATA_DIR}" && tar -czf - agent-home) > "${STAGE_DIR}/agent-home.tar.gz"
  chmod 600 "${STAGE_DIR}/agent-home.tar.gz"
fi

TS="$(date -u +%Y%m%dT%H%M%SZ)"
printf 'created_at=%s\nformat=v1\n' "${TS}" > "${STAGE_DIR}/manifest.txt"

umask 077
OUT_ARCHIVE="${DEST_DIR}/max-backup-${TS}.tar.gz"
(cd "${STAGE_DIR}" && tar -czf - .) > "${OUT_ARCHIVE}"
echo "${OUT_ARCHIVE}"

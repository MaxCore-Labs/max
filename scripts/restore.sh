#!/usr/bin/env bash
set -euo pipefail

FORCE=0
if [[ "${1:-}" == "--force" ]]; then
  FORCE=1
  shift
fi

ARCHIVE="${1:-}"
if [[ -z "${ARCHIVE}" || ! -f "${ARCHIVE}" ]]; then
  echo "usage: restore.sh [--force] <backup-archive.tar.gz>" >&2
  exit 2
fi

DATA_DIR="${MAX_DATA_DIR:-./data}"
MAX_BIN="${MAX_BIN:-max}"

if [[ "${FORCE}" -eq 0 && -f "${DATA_DIR}/max.sqlite3" ]]; then
  echo "error: ${DATA_DIR}/max.sqlite3 already exists (pass --force to overwrite)" >&2
  exit 1
fi

UNPACK_DIR="$(mktemp -d)"
trap 'rm -rf "${UNPACK_DIR}"' EXIT

(cd "${UNPACK_DIR}" && tar -xzf - < "${ARCHIVE}")

if [[ ! -f "${UNPACK_DIR}/max.sqlite3" ]]; then
  echo "error: backup archive does not contain max.sqlite3" >&2
  exit 1
fi

if command -v sqlite3 >/dev/null 2>&1; then
  CHK="$(sqlite3 "${UNPACK_DIR}/max.sqlite3" 'PRAGMA integrity_check;' | head -n 1)"
  if [[ "${CHK}" != "ok" ]]; then
    echo "error: restored sqlite failed integrity_check: ${CHK}" >&2
    exit 1
  fi
fi

mkdir -p "${DATA_DIR}" "${DATA_DIR}/run"
chmod 700 "${DATA_DIR}" "${DATA_DIR}/run"
rm -f "${DATA_DIR}/max.sqlite3" "${DATA_DIR}/max.sqlite3-wal" "${DATA_DIR}/max.sqlite3-shm"
cp "${UNPACK_DIR}/max.sqlite3" "${DATA_DIR}/max.sqlite3"
chmod 600 "${DATA_DIR}/max.sqlite3"

if [[ -f "${UNPACK_DIR}/agent-home.tar.gz" ]]; then
  rm -rf "${DATA_DIR}/agent-home"
  (cd "${DATA_DIR}" && tar -xzf - < "${UNPACK_DIR}/agent-home.tar.gz")
  chmod 700 "${DATA_DIR}/agent-home"
  find "${DATA_DIR}/agent-home" -maxdepth 1 -mindepth 1 -type d -exec chmod 700 {} +
fi

if command -v "${MAX_BIN}" >/dev/null 2>&1 || [[ -x "${MAX_BIN}" ]]; then
  MAX_DATA_DIR="${DATA_DIR}" "${MAX_BIN}" migrate >/dev/null
fi

echo "restored ${ARCHIVE} into ${DATA_DIR}"

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LINECOUNT_SH="${SCRIPT_DIR}/linecount.sh"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/pkg"

# 1. 401-line Go file must fail (exit 1) and print filename
awk 'BEGIN { for (i = 1; i <= 401; i++) print "// line " i }' > "${TMP_DIR}/pkg/too_long.go"
set +e
OUT="$(bash "${LINECOUNT_SH}" "${TMP_DIR}" 2>&1)"
CODE=$?
set -e
if [[ "${CODE}" -ne 1 ]]; then
  echo "FAIL: expected exit code 1 for 401-line Go file, got ${CODE}" >&2
  exit 1
fi
if [[ "${OUT}" != *"too_long.go"* ]]; then
  echo "FAIL: expected output to mention too_long.go, got: ${OUT}" >&2
  exit 1
fi

# 2. 400-line Go file must pass (exit 0)
rm -f "${TMP_DIR}/pkg/too_long.go"
awk 'BEGIN { for (i = 1; i <= 400; i++) print "// line " i }' > "${TMP_DIR}/pkg/ok.go"
bash "${LINECOUNT_SH}" "${TMP_DIR}" >/dev/null

# 3. 301-line Svelte file must fail (exit 1) and print filename
awk 'BEGIN { for (i = 1; i <= 301; i++) print "<!-- line " i " -->" }' > "${TMP_DIR}/pkg/Bad.svelte"
set +e
OUT_SV="$(bash "${LINECOUNT_SH}" "${TMP_DIR}" 2>&1)"
CODE_SV=$?
set -e
if [[ "${CODE_SV}" -ne 1 ]] || [[ "${OUT_SV}" != *"Bad.svelte"* ]]; then
  echo "FAIL: expected exit 1 mentioning Bad.svelte, got code=${CODE_SV} out=${OUT_SV}" >&2
  exit 1
fi

# 4. Generated directories (gen/, db/, schema.d.ts) must be ignored
rm -f "${TMP_DIR}/pkg/Bad.svelte"
mkdir -p "${TMP_DIR}/internal/adapter/httpapi/gen" "${TMP_DIR}/internal/adapter/sqlite/db"
awk 'BEGIN { for (i = 1; i <= 900; i++) print "// generated " i }' > "${TMP_DIR}/internal/adapter/httpapi/gen/server.go"
awk 'BEGIN { for (i = 1; i <= 900; i++) print "// generated " i }' > "${TMP_DIR}/internal/adapter/sqlite/db/models.go"
bash "${LINECOUNT_SH}" "${TMP_DIR}" >/dev/null

echo "PASS: linecount_test.sh"

#!/usr/bin/env bash
set -euo pipefail

TARGET_DIR="${1:-.}"

GO_FILE_CAP=400
WEB_FILE_CAP=300
GO_TOTAL_WARN=10500
WEB_TOTAL_WARN=6000
WEB_UI_WARN=1200

fail=0
go_total=0
web_total=0
web_ui_total=0

while IFS= read -r -d '' file; do
  norm="${file//\\//}"

  # Skip generated directories/files, third-party modules, worktrees, and Phase 0 spikes
  case "${norm}" in
    */.git/*|*/.worktrees/*|*/spikes/*|*/node_modules/*|*/.svelte-kit/*|*/web/build/*|*/internal/adapter/httpapi/gen/*|*/internal/adapter/sqlite/db/*|*/gen/*|*/db/*|*/schema.d.ts)
      continue
      ;;
  esac

  lines="$(awk 'END { print NR }' "${file}")"

  case "${norm}" in
    *.go)
      if (( lines > GO_FILE_CAP )); then
        echo "ERROR: Go file exceeds ${GO_FILE_CAP} lines (${lines}): ${file}" >&2
        fail=1
      fi
      if [[ "${norm}" != *_test.go ]]; then
        go_total=$(( go_total + lines ))
      fi
      ;;
    *.svelte|*.ts)
      if [[ "${norm}" == *.d.ts ]]; then
        continue
      fi
      if (( lines > WEB_FILE_CAP )); then
        echo "ERROR: Web file exceeds ${WEB_FILE_CAP} lines (${lines}): ${file}" >&2
        fail=1
      fi
      if [[ "${norm}" != *.test.ts && "${norm}" != *.spec.ts ]]; then
        web_total=$(( web_total + lines ))
        if [[ "${norm}" == */web/src/lib/ui/* ]]; then
          web_ui_total=$(( web_ui_total + lines ))
        fi
      fi
      ;;
  esac
done < <(find "${TARGET_DIR}" -type f \( -name '*.go' -o -name '*.svelte' -o -name '*.ts' \) -print0)

if (( go_total > GO_TOTAL_WARN )); then
  echo "WARN: Total hand-written Go lines (${go_total}) > ${GO_TOTAL_WARN}" >&2
fi
if (( web_total > WEB_TOTAL_WARN )); then
  echo "WARN: Total hand-written Svelte/TS lines (${web_total}) > ${WEB_TOTAL_WARN}" >&2
fi
if (( web_ui_total > WEB_UI_WARN )); then
  echo "WARN: Total web/src/lib/ui lines (${web_ui_total}) > ${WEB_UI_WARN}" >&2
fi

exit "${fail}"

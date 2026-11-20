#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

EXT=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) EXT=".exe" ;;
esac

MAX_BIN="${WORK_DIR}/max${EXT}"
FAKECLI_BIN="${WORK_DIR}/fakecli${EXT}"
DATA_DIR="${WORK_DIR}/data"
VAULT_DIR="${WORK_DIR}/vault"
BACKUP_DIR="${WORK_DIR}/backups"

(
  cd "${ROOT_DIR}"
  CGO_ENABLED=0 go build -o "${MAX_BIN}" ./cmd/max
  CGO_ENABLED=0 go build -o "${FAKECLI_BIN}" ./internal/testutil/fakecli
)

mkdir -p "${DATA_DIR}/agent-home/agy/.config/agy" "${VAULT_DIR}" "${BACKUP_DIR}"
chmod 700 "${DATA_DIR}/agent-home/agy"
printf '{"token":"sample-session-marker"}\n' > "${DATA_DIR}/agent-home/agy/.config/agy/auth.json"

export MAX_DATA_DIR="${DATA_DIR}"
export MAX_VAULT_DIR="${VAULT_DIR}"
export MAX_AGY_BIN="${FAKECLI_BIN}"
export MAX_ENGINES="agy"

"${MAX_BIN}" migrate >/dev/null

SEED_GO="${WORK_DIR}/seed.go"
cat > "${SEED_GO}" <<'EOF'
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := filepath.ToSlash(filepath.Join(os.Getenv("MAX_DATA_DIR"), "max.sqlite3"))
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	if os.Args[1] == "seed" {
		stmts := []string{
			`INSERT INTO teams (id, slug, name, max_running, status) VALUES ('t1', 'core', 'Core', 2, 'active')`,
			`INSERT INTO agents (id, slug, name, team_id, engines, engine_mask, autonomy, browser, config_rev, status) VALUES ('a1', 'chief-of-staff', 'Chief', 't1', '["agy"]', 4, 'auto', 'none', 'r1', 'active')`,
			`INSERT INTO threads (id, agent_id, title, created_at, updated_at) VALUES ('th1', 'a1', 'Main', 1000, 1000)`,
			`INSERT INTO messages (id, thread_id, role, content, created_at) VALUES ('m1', 'th1', 'user', 'hello 1', 1001)`,
			`INSERT INTO messages (id, thread_id, role, content, created_at) VALUES ('m2', 'th1', 'assistant', 'reply 1', 1002)`,
			`INSERT INTO messages (id, thread_id, role, content, created_at) VALUES ('m3', 'th1', 'user', 'hello 2', 1003)`,
		}
		for _, q := range stmts {
			if _, err := db.Exec(q); err != nil {
				panic(err)
			}
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil {
		panic(err)
	}
	fmt.Println(count)
}
EOF

BEFORE_COUNT="$(cd "${ROOT_DIR}" && CGO_ENABLED=0 go run "${SEED_GO}" seed)"
if [[ "${BEFORE_COUNT}" != "3" ]]; then
  echo "expected 3 seeded messages, got ${BEFORE_COUNT}" >&2
  exit 1
fi

# Run backup.sh (uses VACUUM INTO for SQLite + tar for agent-home)
MAX_BIN="${MAX_BIN}" MAX_DATA_DIR="${DATA_DIR}" MAX_VAULT_DIR="${VAULT_DIR}" \
  bash "${ROOT_DIR}/scripts/backup.sh" "${BACKUP_DIR}"

ARCHIVE="$(ls -1 "${BACKUP_DIR}"/max-backup-*.tar.gz | head -n 1)"
if [[ -z "${ARCHIVE}" || ! -f "${ARCHIVE}" ]]; then
  echo "backup archive not created" >&2
  exit 1
fi

# Delete data directory completely
rm -rf "${DATA_DIR}"

# Run restore.sh into clean data directory
MAX_BIN="${MAX_BIN}" MAX_DATA_DIR="${DATA_DIR}" MAX_VAULT_DIR="${VAULT_DIR}" \
  bash "${ROOT_DIR}/scripts/restore.sh" "${ARCHIVE}"

# Verify agent-home was restored
if [[ ! -f "${DATA_DIR}/agent-home/agy/.config/agy/auth.json" ]]; then
  echo "agent-home/agy/.config/agy/auth.json missing after restore" >&2
  exit 1
fi

# Verify messages row count matches
AFTER_COUNT="$(cd "${ROOT_DIR}" && CGO_ENABLED=0 go run "${SEED_GO}" count)"
if [[ "${AFTER_COUNT}" != "${BEFORE_COUNT}" ]]; then
  echo "messages count mismatch: before=${BEFORE_COUNT}, after=${AFTER_COUNT}" >&2
  exit 1
fi

# Verify max doctor exits 0 with fake engine
"${MAX_BIN}" doctor
echo "PASS: backup_restore_test.sh verified (${AFTER_COUNT} messages, agent-home restored, max doctor=0)"

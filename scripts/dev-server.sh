#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/dev-server.sh [--dry-run | --help]

Run the local Go server from any working directory. Defaults bind to
127.0.0.1:8080 and store data in server/data/. Existing LINKNESTX_* settings
are preserved. This script never supplies administrator credentials or JWT keys.
EOF
}

dry_run=false
case "${1:-}" in
  --help|-h) usage; exit 0 ;;
  --dry-run) dry_run=true; shift ;;
  "") ;;
  *) usage >&2; exit 2 ;;
esac
if [[ $# -ne 0 ]]; then usage >&2; exit 2; fi

repo_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
if $dry_run; then
  printf 'Working directory: %s\nCommand: go run ./server/cmd/server\n' "$repo_dir"
  printf 'Local defaults only; configured credentials are neither generated nor printed.\n'
  exit 0
fi

command -v go >/dev/null 2>&1 || { printf 'Go is required; see README.md\n' >&2; exit 127; }
umask 077
export LINKNESTX_BIND_ADDRESS="${LINKNESTX_BIND_ADDRESS:-127.0.0.1:8080}"
export LINKNESTX_STORAGE_ROOT="${LINKNESTX_STORAGE_ROOT:-$repo_dir/server/data}"
export LINKNESTX_SQLITE_PATH="${LINKNESTX_SQLITE_PATH:-$LINKNESTX_STORAGE_ROOT/linknestx.sqlite3}"
export GOCACHE="${GOCACHE:-$repo_dir/.cache/go}"
cd -- "$repo_dir"
exec go run ./server/cmd/server

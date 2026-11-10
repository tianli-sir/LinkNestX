#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ $# -eq 1 && "$1" == --export-only ]]; then
  set -- actual_go_native_audit_export
elif [[ $# -ne 0 ]]; then
  printf 'Usage: scripts/smoke-test.sh [--export-only]\nBuilds a fresh Go server and runs the real native integration and 20GB geometry tests.\nUse --export-only for a focused native audit export rerun.\n' >&2
  exit 2
fi
command -v "${GO:-go}" >/dev/null 2>&1 || { printf 'Go is required for the native/server smoke test.\n' >&2; exit 127; }
smoke_cargo="${CARGO:-cargo}"
if ! command -v "$smoke_cargo" >/dev/null 2>&1; then
  if [[ -x "$repo_dir/.cache/cargo/bin/cargo" && -d "$repo_dir/.cache/rustup" ]]; then
    export CARGO_HOME="$repo_dir/.cache/cargo"
    export RUSTUP_HOME="$repo_dir/.cache/rustup"
    export PATH="$repo_dir/.cache/cargo/bin:$PATH"
    smoke_cargo="$repo_dir/.cache/cargo/bin/cargo"
  else
    printf 'Cargo and a Rust toolchain are required for the smoke test.\n' >&2
    exit 127
  fi
fi
"$smoke_cargo" --version >/dev/null
smoke_build_dir="$(mktemp -d "${TMPDIR:-/tmp}/linknestx-smoke-build.XXXXXX")"
smoke_windows=false
smoke_native_dir="$smoke_build_dir"
smoke_server="$smoke_build_dir/linknestx-server"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    smoke_windows=true
    smoke_native_dir="$(cygpath -w "$smoke_build_dir")"
    smoke_server="$smoke_build_dir/linknestx-server.exe"
    ;;
esac
smoke_native_server="$smoke_server"
smoke_native_marker="$smoke_build_dir/child-pids"
smoke_go_cache="${GOCACHE:-$repo_dir/.cache/go}"
if [[ "$smoke_windows" == true ]]; then
  smoke_native_server="$(cygpath -w "$smoke_server")"
  smoke_native_marker="$(cygpath -w "$smoke_native_marker")"
  smoke_go_cache="$(cygpath -w "$smoke_go_cache")"
fi
smoke_cargo_pid=""
cleanup() {
  local test_pid="" server_pid="" test_parent="" server_command=""
  if [[ -n "$smoke_cargo_pid" && -f "$smoke_build_dir/child-pids" ]]; then
    read -r test_pid server_pid < "$smoke_build_dir/child-pids" || true
    if [[ "$test_pid" =~ ^[0-9]+$ && "$server_pid" =~ ^[0-9]+$ && "$test_pid" -gt 1 && "$server_pid" -gt 1 ]]; then
      if [[ "$smoke_windows" == true ]]; then
        # Native Windows process IDs differ from Git Bash's IDs. Check the exact
        # unique executable and parent before stopping either recorded process.
        LINKNESTX_SMOKE_OWN_SERVER="$smoke_native_server" LINKNESTX_SMOKE_OWN_SERVER_PID="$server_pid" \
          LINKNESTX_SMOKE_OWN_TEST_PID="$test_pid" powershell.exe -NoProfile -NonInteractive -Command '
          $ownedServer = Get-CimInstance Win32_Process -Filter ("ProcessId = " + $env:LINKNESTX_SMOKE_OWN_SERVER_PID) -ErrorAction SilentlyContinue
          if ($ownedServer -and $ownedServer.ExecutablePath -eq $env:LINKNESTX_SMOKE_OWN_SERVER) {
            $ownedTest = $ownedServer.ParentProcessId -eq [int]$env:LINKNESTX_SMOKE_OWN_TEST_PID
            Stop-Process -Id $ownedServer.ProcessId -Force -ErrorAction SilentlyContinue
            if ($ownedTest) { Stop-Process -Id ([int]$env:LINKNESTX_SMOKE_OWN_TEST_PID) -Force -ErrorAction SilentlyContinue }
          }' >/dev/null 2>&1 || true
      else
        test_parent="$(ps -o ppid= -p "$test_pid" 2>/dev/null | tr -d ' ')" || true
        server_command="$(ps -o comm= -p "$server_pid" 2>/dev/null)" || true
        # A unique binary path also identifies our server if the test already exited.
        if [[ "$server_command" == "$smoke_server" ]]; then
          kill "$server_pid" 2>/dev/null || true
        fi
        # Never signal another test process that could have reused the recorded PID.
        if [[ "$test_parent" == "$smoke_cargo_pid" ]]; then
          kill "$test_pid" 2>/dev/null || true
        fi
      fi
    fi
  fi
  if [[ -n "$smoke_cargo_pid" ]]; then
    kill "$smoke_cargo_pid" 2>/dev/null || true
    wait "$smoke_cargo_pid" 2>/dev/null || true
  fi
  rm -rf -- "$smoke_build_dir"
}
trap 'smoke_exit_status=$?; cleanup; exit "$smoke_exit_status"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cd -- "$repo_dir"
GOCACHE="$smoke_go_cache" "${GO:-go}" build -mod=readonly -o "$smoke_server" ./server/cmd/server
(
  # Only the parent owns cleanup. Bash 3.2 can mask an asynchronous expansion
  # failure when the child inherits its EXIT trap. exec keeps Cargo's PID stable.
  trap - EXIT
  TMPDIR="$smoke_native_dir" TEMP="$smoke_native_dir" TMP="$smoke_native_dir" LINKNESTX_SMOKE_SERVER="$smoke_native_server" \
    LINKNESTX_SMOKE_PID_MARKER="$smoke_native_marker" exec "$smoke_cargo" test --locked \
    --manifest-path "$repo_dir/client/src-tauri/Cargo.toml" --test server_smoke \
    "$@" -- --include-ignored --nocapture --test-threads=1
) &
smoke_cargo_pid=$!
wait "$smoke_cargo_pid"
smoke_cargo_pid=""

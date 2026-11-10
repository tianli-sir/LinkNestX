#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/package-client.sh [--unsigned | --signed] [--bundles LIST]
                                 [--target TRIPLE] [--dry-run] [--help]

Build with the locked local Tauri CLI and Cargo lockfile. The default is a
macOS app + dmg with a local ad-hoc signature or unsigned Windows NSIS
installer. No Developer ID/notarization is supplied. Windows uses Git Bash.
--bundles accepts the platform's Tauri bundle list, e.g. app or nsis,msi.
--target accepts a Rust target already installed for the current platform.

Signed macOS builds require APPLE_SIGNING_IDENTITY and the usual Tauri Apple
notarization environment variables. Signed Windows builds require
LINKNESTX_SIGNING_CONFIG to point to an existing Tauri JSON signing override;
the certificate or signing service must already be available to the runner.
No credentials are created or printed. See docs/deployment.md.
EOF
}

dry_run=false
signed=false
bundles=""
target=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --dry-run) dry_run=true; shift ;;
    --unsigned) signed=false; shift ;;
    --signed) signed=true; shift ;;
    --bundles|--target)
      if [[ $# -lt 2 || -z "$2" || "$2" == --* ]]; then
        printf 'Missing value for %s\n' "$1" >&2; exit 2
      fi
      if [[ "$1" == --bundles ]]; then bundles="$2"; else target="$2"; fi
      shift 2 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

repo_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
case "$(uname -s)" in
  Darwin) platform=macos; bundles="${bundles:-app,dmg}" ;;
  MINGW*|MSYS*|CYGWIN*) platform=windows; bundles="${bundles:-nsis}" ;;
  *) printf 'Desktop packages must be built on macOS or Windows.\n' >&2; exit 2 ;;
esac

args=(run tauri -- build --ci --bundles "$bundles")
if [[ -n "$target" ]]; then args+=(--target "$target"); fi
if ! $signed && [[ "$platform" == windows ]]; then args+=(--no-sign); fi
if $dry_run; then
  printf 'Working directory: %s/client\nCommand: npm' "$repo_dir"
  printf ' %q' "${args[@]}"
  if $signed && [[ -n "${LINKNESTX_SIGNING_CONFIG:-}" ]]; then
    printf ' --config <LINKNESTX_SIGNING_CONFIG>'
  fi
  printf ' -- --locked\n'
  printf 'Signing: %s (credentials and signing configuration are not printed)\n' "$signed"
  if ! $signed && [[ "$platform" == macos ]]; then
    printf 'macOS uses a local ad-hoc resource seal for update integrity, without notarization.\n'
  fi
  exit 0
fi

command -v node >/dev/null 2>&1 || { printf 'Node.js is required; see README.md\n' >&2; exit 127; }
command -v npm >/dev/null 2>&1 || { printf 'npm is required; see README.md\n' >&2; exit 127; }
if ! command -v cargo >/dev/null 2>&1; then
  if [[ -x "$repo_dir/.cache/cargo/bin/cargo" && -d "$repo_dir/.cache/rustup" ]]; then
    export CARGO_HOME="$repo_dir/.cache/cargo"
    export RUSTUP_HOME="$repo_dir/.cache/rustup"
    export PATH="$repo_dir/.cache/cargo/bin:$PATH"
  else
    printf 'Cargo and a Rust toolchain are required; see README.md\n' >&2; exit 127
  fi
fi
cargo --version >/dev/null
if [[ ! -f "$repo_dir/client/node_modules/@tauri-apps/cli/tauri.js" ]]; then
  printf 'Run npm ci in client/ before packaging.\n' >&2; exit 1
fi

if ! $signed && [[ "$platform" == macos ]]; then
  # Seal the whole bundle before DMG creation. A linker-only executable signature
  # is insufficient for the updater's codesign --verify --deep --strict check.
  export APPLE_SIGNING_IDENTITY=-
  unset APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID
  unset APPLE_API_ISSUER APPLE_API_KEY APPLE_API_KEY_PATH
fi

if $signed; then
  if [[ "$platform" == macos && ( -z "${APPLE_SIGNING_IDENTITY:-}" || "${APPLE_SIGNING_IDENTITY:-}" == - ) ]]; then
    printf 'Signed macOS builds require APPLE_SIGNING_IDENTITY.\n' >&2; exit 2
  fi
  if [[ "$platform" == windows && -z "${LINKNESTX_SIGNING_CONFIG:-}" ]]; then
    printf 'Signed Windows builds require LINKNESTX_SIGNING_CONFIG.\n' >&2; exit 2
  fi
  if [[ -n "${LINKNESTX_SIGNING_CONFIG:-}" ]]; then
    if [[ ! -f "$LINKNESTX_SIGNING_CONFIG" ]]; then
      printf 'LINKNESTX_SIGNING_CONFIG must name an existing file.\n' >&2; exit 2
    fi
    # Resolve before changing directory; pass a filename, never evaluate its text.
    signing_config="$(CDPATH= cd -- "$(dirname -- "$LINKNESTX_SIGNING_CONFIG")" && pwd)/$(basename -- "$LINKNESTX_SIGNING_CONFIG")"
    args+=(--config "$signing_config")
  fi
fi

cd -- "$repo_dir/client"
exec npm "${args[@]}" -- --locked

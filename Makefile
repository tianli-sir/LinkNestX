SHELL := /bin/sh
.DEFAULT_GOAL := test

ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
GO ?= go
NPM ?= npm
CARGO ?= cargo

# Prefer an installed toolchain. The repository-local fallback is scoped to
# each command and must use both homes so rustup finds its installed toolchain.
ifeq ($(shell command -v $(CARGO) 2>/dev/null),)
ifneq ($(wildcard $(ROOT)/.cache/cargo/bin/cargo),)
CARGO_ENV := env CARGO_HOME="$(ROOT)/.cache/cargo" RUSTUP_HOME="$(ROOT)/.cache/rustup" PATH="$(ROOT)/.cache/cargo/bin:$(PATH)"
endif
endif

.PHONY: test check smoke test-smoke-script test-server test-client test-rust check-client check-go check-node check-rust dev-server dev-client package-client

# Every suite is required: a missing tool or any failing command fails the target.
test: test-server test-client test-rust

check: test-smoke-script test check-client smoke

test-smoke-script:
	cd "$(ROOT)" && node --test scripts/test-smoke-script.mjs

smoke: check-go check-rust
	cd "$(ROOT)" && $(CARGO_ENV) GO="$(GO)" CARGO="$(CARGO)" "$(ROOT)/scripts/smoke-test.sh"

check-go:
	@command -v $(GO) >/dev/null 2>&1 || { echo "Go is required; see README.md" >&2; exit 127; }

check-node:
	@command -v node >/dev/null 2>&1 || { echo "Node.js is required; see README.md" >&2; exit 127; }
	@command -v $(NPM) >/dev/null 2>&1 || { echo "npm is required; see README.md" >&2; exit 127; }
	@test -d "$(ROOT)/client/node_modules" || { echo "Run npm ci in client/ first" >&2; exit 1; }

check-rust:
	@$(CARGO_ENV) $(CARGO) --version >/dev/null 2>&1 || { echo "Cargo and a Rust toolchain are required; see README.md" >&2; exit 127; }

test-server: check-go
	cd "$(ROOT)" && GOCACHE="$(ROOT)/.cache/go" $(GO) test -mod=readonly ./server/...

test-client: check-node
	cd "$(ROOT)/client" && $(NPM) test

test-rust: check-rust
	cd "$(ROOT)" && $(CARGO_ENV) $(CARGO) test --locked --manifest-path client/src-tauri/Cargo.toml

# Type checking and production frontend output are an explicit standard check.
check-client: check-node
	cd "$(ROOT)/client" && $(NPM) run build

dev-server: check-go
	"$(ROOT)/scripts/dev-server.sh"

dev-client: check-node check-rust
	cd "$(ROOT)/client" && $(CARGO_ENV) $(NPM) run desktop:dev

package-client:
	"$(ROOT)/scripts/package-client.sh"

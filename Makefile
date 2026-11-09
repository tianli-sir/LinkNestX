.PHONY: test test-server test-client test-rust

# Run every available package check. The Rust check is skipped with a clear
# message on hosts that do not have cargo installed (for example CI jobs that
# only exercise the server and web shell).
test: test-server test-client test-rust

test-server:
	GOCACHE=$$(pwd)/.cache/go go test ./server/...

test-client:
	cd client && npm test -- --run

test-rust:
	@if command -v cargo >/dev/null 2>&1; then \
		cargo test --manifest-path client/src-tauri/Cargo.toml; \
	else \
		echo "cargo not installed; skipping Rust tests"; \
	fi

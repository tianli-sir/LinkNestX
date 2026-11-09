# LinkNestX

LinkNestX is a self-hosted collaboration server and Windows/macOS desktop client for employee group chat and authenticated file transfer. The first milestone keeps the server and Tauri client small enough to run on a single machine while leaving storage, retention, and transfer policies configurable.

## Repository layout

- `api/openapi.yaml` documents the HTTP contract.
- `server/` contains the Go API server.
- `client/` contains the React/Vite shell and Tauri bootstrap.

## Run the health check

Start the server with:

```sh
cd server
go run ./cmd/server
```

Then request `http://127.0.0.1:8080/healthz` to receive a JSON object with `status: "ok"` and the running version.

Configuration is supplied through `LINKNESTX_*` environment variables. The defaults are suitable for local development; set `LINKNESTX_JWT_SIGNING_KEY` and `LINKNESTX_JWT_REFRESH_KEY` before a shared deployment.

## Set up local accounts

On the first server start, supply `LINKNESTX_BOOTSTRAP_ADMIN_USERNAME` and `LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD` in the server environment. The password must contain at least eight characters. Startup creates the initial administrator once; keeping or changing these values on later starts does not replace an existing account or reset its password. Remove the bootstrap password from the environment after setup.

Sign in at `POST /api/v1/auth/login`, then use the access token as `Authorization: Bearer <token>`. Administrators create employee accounts through `POST /api/v1/users` with `username`, `password`, optional `email`, and optional `roles` (default `["user"]`). The response contains the account details and `mustChangePassword: true` for accounts with an administrator-provided initial password. Bootstrap is available only through server startup configuration.

## Run checks

From the repository root:

```sh
make test
```

The target runs Go tests, the Vitest client tests, and Rust tests when the Rust toolchain is installed.

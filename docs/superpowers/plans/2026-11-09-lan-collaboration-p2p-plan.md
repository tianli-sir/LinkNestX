# LAN Collaboration and P2P Transfer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a self-hosted Windows/macOS desktop collaboration app with group chat, a permanent administrator-managed file pool, 30-day temporary files, and direct-first multi-source P2P transfers with server fallback.

**Architecture:** A Go server exposes authenticated REST APIs and a WebSocket channel for chat and P2P signaling. It stores metadata in SQLite and content-addressed chunks on the local filesystem. A Tauri 2 desktop app uses React/TypeScript for the UI and a Rust background transfer service for chunk scheduling, WebRTC data channels, local caching, tray behavior, and startup. The browser is not a user-facing login or transfer client.

**Tech Stack:** Go, chi, SQLite (WAL), WebSocket, Argon2id, short-lived access tokens plus refresh sessions, Tauri 2, React, TypeScript, Rust/Tokio, `webrtc-rs`, Vitest, Go tests, Rust tests, and Playwright for server-backed smoke flows.

**Spec:** [docs/superpowers/specs/2026-11-09-lan-collaboration-p2p-design.md](/Users/another/projects/LinkNestX/docs/superpowers/specs/2026-11-09-lan-collaboration-p2p-design.md)

## Global Constraints

- Support Windows and macOS desktop clients; do not require a browser login.
- The first-scale baseline is approximately 100–200 employee accounts with a maximum single file size of 20 GB.
- The server is self-hosted and initially single-node; storage paths, chunk size, concurrency, cache limits, and relay settings are configuration values.
- All public-pool and group-file access requires an authenticated employee account.
- Ordinary group files expire 30 days after upload and are deleted directly; administrator-promoted public-pool files do not expire automatically.
- Chat messages, file metadata, transfer records, permission changes, and audit records are retained for two years.
- Administrators create accounts and groups; group owners manage group membership; administrators manage the public pool and all files.
- Clients default to background sharing while the app is running; closing the main window leaves the tray/background transfer process active.
- P2P is direct-first, then relay, then server; P2P failure must never block a server-backed download.
- The server may read content; transport is encrypted and local storage must be access-controlled.
- No anonymous download, no first-phase private chat or announcements, and no first-phase external identity provider integration.

## Review Focus

- **20 GB streaming and resume:** the server and client must stream chunks without loading whole files into memory and must resume after interruption; cover this in the chunk storage and transfer tests.
- **Permission revocation during transfer:** a removed member or expired file must stop new chunk requests and invalidate outstanding transfer authorization; cover this in authorization and lifecycle tests.
- **Source failure and bad data:** a peer that disconnects or sends a mismatched hash must be quarantined for that task and replaced without corrupting the final file; cover this in scheduler and P2P tests.
- **Background lifetime:** closing the Tauri window must leave the transfer service running, while explicit app exit must stop sharing cleanly; cover this in desktop transfer service tests.
- **Retention boundaries:** 30-day file cleanup and two-year message/audit cleanup must be timezone-stable and idempotent; cover this in server job tests.

## File Structure

Create the following monorepo layout before feature work:

```text
api/openapi.yaml
server/
  cmd/server/main.go
  internal/{config,db,auth,groups,chat,files,p2p,audit,retention,http}
  migrations/
  tests/
client/
  src/
  src-tauri/src/{auth,chat,files,transfer,p2p,admin,main.rs}
  src-tauri/tests/
  tests/
scripts/
docs/
```

### Task 1: Scaffold the monorepo and executable health checks

**Files:**
- Create: `README.md`, `.gitignore`, `Makefile`, `api/openapi.yaml`
- Create: `server/go.mod`, `server/cmd/server/main.go`, `server/internal/config/config.go`
- Create: `client/package.json`, `client/tsconfig.json`, `client/vite.config.ts`, `client/src/main.tsx`
- Create: `client/src-tauri/Cargo.toml`, `client/src-tauri/src/main.rs`
- Test: `server/internal/http/health_test.go`, `client/tests/app-shell.test.tsx`

**Interfaces:**
- Produces `GET /healthz -> {"status":"ok","version":string}`.
- Produces a Tauri app shell with `window.__APP_VERSION__` and a typed `invoke` wrapper.
- Produces a Make target named `test` that runs server, client, and Rust tests.

- [ ] **Step 1: Write the failing server health test** asserting status 200, JSON content type, and the exact `status` value `ok`.
- [ ] **Step 2: Run `go test ./server/internal/http -run TestHealth -v` and verify it fails because the server package does not exist.**
- [ ] **Step 3: Implement the server bootstrap, config loader, router, and health handler.** Keep bind address, storage root, SQLite path, JWT keys, and retention intervals in `server/internal/config/config.go`.
- [ ] **Step 4: Write the failing React shell test** asserting the app renders the login route placeholder and app version.
- [ ] **Step 5: Run `npm test -- --run client/tests/app-shell.test.tsx` and verify it fails before the shell exists.**
- [ ] **Step 6: Implement the React/Vite shell and Tauri bootstrap.** Keep all native calls behind `client/src/lib/tauri.ts`.
- [ ] **Step 7: Run `make test` and verify the health and shell tests pass.**
- [ ] **Step 8: Commit the scaffold as `chore: scaffold server and desktop client`.**

### Task 2: Add SQLite schema, local accounts, sessions, and roles

**Files:**
- Create: `server/migrations/001_initial.sql`, `server/internal/db/db.go`, `server/internal/db/migrations.go`
- Create: `server/internal/auth/password.go`, `server/internal/auth/service.go`, `server/internal/auth/repository.go`
- Create: `server/internal/http/auth_handlers.go`, `server/internal/http/middleware.go`
- Modify: `api/openapi.yaml`
- Test: `server/internal/auth/service_test.go`, `server/internal/http/auth_handlers_test.go`

**Interfaces:**
- `AuthService.CreateUser(ctx, CreateUserInput) (User, error)`
- `AuthService.Login(ctx, LoginInput) (Session, error)`
- `AuthService.Refresh(ctx, refreshToken string) (Session, error)`
- `AuthService.RevokeSession(ctx, sessionID string) error`
- `Session` contains `AccessToken`, `RefreshToken`, `ExpiresAt`, `User`, and `DeviceID`.
- REST endpoints: `POST /api/v1/auth/login`, `POST /api/v1/auth/refresh`, `POST /api/v1/auth/logout`, `GET /api/v1/me`.

- [ ] **Step 1: Write tests for Argon2id password verification, disabled accounts, refresh-token rotation, revoked sessions, and admin-only account creation.**
- [ ] **Step 2: Run the auth tests and verify they fail because the schema and service are missing.**
- [ ] **Step 3: Implement migrations for `users`, `devices`, `sessions`, `roles`, and `user_roles`, using UUID identifiers and timestamps in UTC.** Enable SQLite WAL mode and foreign keys.
- [ ] **Step 4: Implement Argon2id hashing and `AuthService`.** Access tokens expire after 15 minutes; refresh sessions expire after 30 days and rotate on use.
- [ ] **Step 5: Implement bearer-token middleware and the auth handlers.** Never return password hashes or refresh tokens except in the login/refresh response.
- [ ] **Step 6: Run `go test ./server/internal/auth ./server/internal/http -v` and verify all auth tests pass.**
- [ ] **Step 7: Commit as `feat: add local accounts and sessions`.**

### Task 3: Implement groups, membership, ordered group chat, and two-year retention

**Files:**
- Create: `server/migrations/002_groups_chat.sql`
- Create: `server/internal/groups/service.go`, `server/internal/groups/repository.go`
- Create: `server/internal/chat/service.go`, `server/internal/chat/repository.go`, `server/internal/chat/ws.go`
- Create: `server/internal/http/group_handlers.go`, `server/internal/http/chat_handlers.go`
- Test: `server/internal/groups/service_test.go`, `server/internal/chat/service_test.go`, `server/internal/chat/ws_test.go`

**Interfaces:**
- `GroupService.Create(ctx, actorID, input) (Group, error)`
- `GroupService.AddMember(ctx, actorID, groupID, userID, role) error`
- `GroupService.RemoveMember(ctx, actorID, groupID, userID) error`
- `ChatService.Append(ctx, actorID, input) (Message, error)` where `input` has `GroupID`, `ClientMessageID`, and `Body`.
- `ChatService.List(ctx, actorID, groupID, cursor, limit) (MessagePage, error)`.
- WebSocket events: `message.created`, `message.ack`, `presence.changed`.

- [ ] **Step 1: Write tests for administrator-created groups, group-owner membership changes, denied cross-group reads, idempotent `ClientMessageID`, cursor pagination, and stable message ordering.**
- [ ] **Step 2: Run the tests and verify they fail before schema and services exist.**
- [ ] **Step 3: Add `groups`, `group_members`, and `messages` tables with indexes for `(group_id, created_at, id)` and a unique `(sender_id, client_message_id)` constraint.**
- [ ] **Step 4: Implement group ACL checks and chat services.** A removed member loses new reads immediately; existing messages remain in the group history for members who still have access.
- [ ] **Step 5: Implement REST handlers and the authenticated WebSocket channel.** Broadcast only to current group members and acknowledge client message IDs.
- [ ] **Step 6: Implement a retention query that deletes message bodies and attachment references older than two years without deleting audit rows.**
- [ ] **Step 7: Run the group/chat test suite and verify all tests pass.**
- [ ] **Step 8: Commit as `feat: add groups and group chat`.**

### Task 4: Implement file metadata, versions, permissions, public-pool promotion, and expiry

**Files:**
- Create: `server/migrations/003_files.sql`
- Create: `server/internal/files/service.go`, `server/internal/files/repository.go`, `server/internal/files/permissions.go`
- Create: `server/internal/http/file_handlers.go`
- Test: `server/internal/files/service_test.go`, `server/internal/files/permissions_test.go`

**Interfaces:**
- `FileService.StartUpload(ctx, actorID, StartUploadInput) (UploadSession, error)`
- `FileService.PromoteToPool(ctx, adminID, fileID, input) (File, error)`
- `FileService.CreateVersion(ctx, actorID, fileID, input) (FileVersion, error)`
- `FileService.AuthorizeChunk(ctx, actorID, fileVersionID, chunkHash) (ChunkGrant, error)`
- `FileService.DeleteExpired(ctx, now time.Time) (DeletionReport, error)`
- REST endpoints: `POST /api/v1/files/uploads`, `GET /api/v1/files`, `POST /api/v1/files/{id}/promote`, `POST /api/v1/files/{id}/versions`, `DELETE /api/v1/files/{id}`.

- [ ] **Step 1: Write tests for group-member access, uploader/group-owner/admin delete rules, public-pool access for every authenticated user, 20 GB size validation, promotion clearing expiry, and 30-day expiry from upload time.**
- [ ] **Step 2: Run the tests and verify they fail before the schema exists.**
- [ ] **Step 3: Add `files`, `file_versions`, `file_chunks`, `upload_sessions`, and `file_memberships` tables.** Keep a stable logical file ID across versions; the latest version is the default, and older versions remain admin-managed until deleted.
- [ ] **Step 4: Implement permission checks and file lifecycle services.** A public-pool promotion preserves the file object and audit history, sets `visibility=pool`, clears automatic expiry, and makes the file available to all authenticated users.
- [ ] **Step 5: Implement metadata endpoints and short-lived per-file chunk grants bound to user, device, version, and expiry.**
- [ ] **Step 6: Run the file service tests and verify all tests pass.**
- [ ] **Step 7: Commit as `feat: add file metadata and lifecycle rules`.**

### Task 5: Add content-addressed chunk storage and server fallback transfer

**Files:**
- Create: `server/internal/files/chunk_store.go`, `server/internal/files/upload.go`, `server/internal/files/download.go`
- Create: `server/internal/retention/jobs.go`, `server/internal/audit/service.go`
- Modify: `server/internal/http/file_handlers.go`, `server/migrations/003_files.sql`
- Test: `server/internal/files/chunk_store_test.go`, `server/internal/files/transfer_test.go`, `server/internal/retention/jobs_test.go`

**Interfaces:**
- `ChunkStore.Put(ctx, hash string, r io.Reader, size int64) error`
- `ChunkStore.Open(ctx, hash string) (io.ReadCloser, ChunkInfo, error)`
- `ChunkStore.Delete(ctx, hash string) error`
- `UploadManager.PutChunk(ctx, grant ChunkGrant, r io.Reader) (ChunkReceipt, error)`
- `DownloadManager.StreamChunk(ctx, actorID, grant ChunkGrant, w io.Writer) error`
- Endpoints: `PUT /api/v1/uploads/{uploadID}/chunks/{index}`, `GET /api/v1/files/{versionID}/chunks/{hash}`.

- [ ] **Step 1: Write tests for streaming a chunk larger than the configured buffer, hash mismatch rejection, idempotent re-upload, range-free full-chunk reads, and missing-chunk fallback errors.**
- [ ] **Step 2: Run the tests and verify they fail before the store exists.**
- [ ] **Step 3: Implement content-addressed storage at `<storageRoot>/chunks/<first-two>/<next-two>/<hash>`.** Write to a temporary file, fsync, rename atomically, and never trust client-provided size or hash without validation.
- [ ] **Step 4: Implement upload-session chunk acceptance and server streaming.** Use bounded buffers so a 20 GB file never becomes an in-memory object.
- [ ] **Step 5: Add scheduled cleanup for expired ordinary files, unreferenced chunks, and two-year audit rows; make each job idempotent and observable.**
- [ ] **Step 6: Run `go test ./server/internal/files ./server/internal/retention -v` and verify all tests pass.**
- [ ] **Step 7: Commit as `feat: add chunk storage and server transfer`.**

### Task 6: Build the desktop background transfer service and local cache

**Files:**
- Create: `client/src-tauri/src/transfer/{models.rs,db.rs,cache.rs,scheduler.rs,service.rs}`
- Create: `client/src-tauri/src/auth/session.rs`, `client/src-tauri/src/commands.rs`
- Create: `client/src-tauri/src/tray.rs`, `client/src-tauri/src/autostart.rs`
- Create: `client/src/lib/api.ts`, `client/src/lib/tauri.ts`, `client/src/features/transfers/store.ts`
- Test: `client/src-tauri/tests/transfer_service.rs`, `client/tests/transfer-store.test.ts`

**Interfaces:**
- Rust command `start_download(version_id: String, destination: String) -> Result<String, TransferError>`.
- Rust command `pause_transfer(task_id: String) -> Result<(), TransferError>`.
- Rust command `resume_transfer(task_id: String) -> Result<(), TransferError>`.
- Rust command `set_sharing_policy(policy: SharingPolicy) -> Result<(), TransferError>`.
- Event `transfer.updated` with task ID, bytes completed, total bytes, active sources, and state.

- [ ] **Step 1: Write Rust tests for persisted task recovery, chunk bitmap updates, bounded cache allocation, pause/resume, and explicit shutdown.**
- [ ] **Step 2: Run `cargo test --manifest-path client/src-tauri/Cargo.toml` and verify the tests fail before the service exists.**
- [ ] **Step 3: Implement the local SQLite transfer database and cache manager.** Store only verified chunks; enforce a configurable cache quota and evict least-recently-used chunks after a file expires or access is revoked.
- [ ] **Step 4: Implement the scheduler with server-source support first.** It must request chunk grants, persist progress after every verified chunk, and resume after app restart.
- [ ] **Step 5: Add Tauri commands, tray state, autostart, and window-hide behavior.** Closing the main window hides it; explicit quit stops the service after persisting state.
- [ ] **Step 6: Add React transfer state bindings and tests for task progress and error display.**
- [ ] **Step 7: Run Rust and TypeScript tests and verify all pass.**
- [ ] **Step 8: Commit as `feat: add background transfer service`.**

### Task 7: Add signaling, device identity, and P2P authorization

**Files:**
- Create: `server/migrations/004_devices_peers.sql`
- Create: `server/internal/p2p/service.go`, `server/internal/p2p/signaling.go`, `server/internal/p2p/peer_repository.go`
- Create: `client/src-tauri/src/p2p/{identity.rs,signaling.rs,authorization.rs}`
- Modify: `server/internal/chat/ws.go`, `api/openapi.yaml`
- Test: `server/internal/p2p/signaling_test.go`, `client/src-tauri/tests/p2p_authorization.rs`

**Interfaces:**
- `PeerService.RegisterDevice(ctx, userID, devicePublicKey, capabilities) (Device, error)`
- `PeerService.ListSources(ctx, actorID, versionID) ([]PeerSource, error)`
- `PeerService.CreateChunkGrant(ctx, actorID, deviceID, versionID, chunkHash) (ChunkGrant, error)`
- WebSocket events: `peer.offer`, `peer.answer`, `peer.ice`, `peer.close`.

- [ ] **Step 1: Write tests for device registration, revoked-device denial, source filtering by group/public-pool access, short-lived grants, and signaling messages that never reveal unauthorized file IDs.**
- [ ] **Step 2: Run the tests and verify they fail before peer services exist.**
- [ ] **Step 3: Add device public keys, last-seen status, capabilities, and peer-heartbeat tables.**
- [ ] **Step 4: Implement server-side source selection and signaling authorization.** The server returns only peers that report a verified chunk and are authorized for the requesting user and version.
- [ ] **Step 5: Implement a per-device keypair in the Tauri data directory and signed session registration.** Device revocation must invalidate future grants.
- [ ] **Step 6: Run server and Rust P2P authorization tests and verify all pass.**
- [ ] **Step 7: Commit as `feat: add peer identity and signaling`.**

### Task 8: Implement WebRTC multi-source transfer and fallback scheduling

**Files:**
- Create: `client/src-tauri/src/p2p/{webrtc.rs,channel.rs,peer_pool.rs}`
- Modify: `client/src-tauri/src/transfer/scheduler.rs`, `client/src-tauri/src/transfer/service.rs`
- Create: `server/internal/p2p/relay.go`, `server/internal/http/relay_handlers.go`
- Test: `client/src-tauri/tests/p2p_transfer.rs`, `server/internal/p2p/relay_test.go`

**Interfaces:**
- `PeerPool.connect(source: PeerSource) -> Result<PeerConnection, PeerError>`.
- `PeerConnection.request_chunk(hash: String) -> Result<ChunkBytes, PeerError>`.
- `Scheduler.assign(version_id: String, sources: Vec<Source>) -> Result<TransferPlan, TransferError>`.
- `Relay.Stream(ctx, grant ChunkGrant, peerID string) -> io.ReadWriteCloser`.

- [ ] **Step 1: Write a two-peer in-process test that transfers verified chunks, retries a dropped peer, and rejects a bad hash.**
- [ ] **Step 2: Run the test and verify it fails before WebRTC and scheduler integration exist.**
- [ ] **Step 3: Implement WebRTC ICE/DataChannel setup with direct-first ordering and configurable STUN/TURN servers.** Use bounded messages and explicit backpressure; never send an entire file as one DataChannel message.
- [ ] **Step 4: Implement peer-pool scheduling.** Start with multiple sources, reassign missing chunks on disconnect, reduce priority for slow/failing peers, and keep one server source available as a safety path.
- [ ] **Step 5: Implement encrypted server relay as the second path and server chunk streaming as the final path.** Record source type and bytes in the audit/transfer record.
- [ ] **Step 6: Add tests for LAN direct, simulated public direct, relay, server fallback, cancellation, and resume after process restart.**
- [ ] **Step 7: Run `cargo test` and the relay tests; verify all pass.**
- [ ] **Step 8: Commit as `feat: add direct-first multi-source transfer`.**

### Task 9: Build the React desktop UI for employee and administrator workflows

**Files:**
- Create: `client/src/features/auth/*`, `client/src/features/groups/*`, `client/src/features/chat/*`, `client/src/features/files/*`, `client/src/features/admin/*`
- Create: `client/src/routes.tsx`, `client/src/components/*`, `client/src/styles/tokens.css`
- Test: `client/tests/auth-flow.test.tsx`, `client/tests/group-chat.test.tsx`, `client/tests/file-pool.test.tsx`, `client/tests/admin-flow.test.tsx`

**Interfaces:**
- UI calls the typed REST client and Tauri commands only; React must not open file sockets directly.
- Screens: login, group list, group chat, group files, public pool, transfer queue, account/device settings, and administrator management.

- [ ] **Step 1: Write component tests for login, group membership visibility, ordered message rendering, upload/download task creation, public-pool promotion, and admin-only controls.**
- [ ] **Step 2: Run Vitest and verify the tests fail before feature components exist.**
- [ ] **Step 3: Implement the authenticated app shell, group navigation, message pagination, and reconnect states.**
- [ ] **Step 4: Implement file lists, version display, upload/promotion actions, download queue, progress, pause/resume, and expiry labels.**
- [ ] **Step 5: Implement administrator screens for accounts, groups, public-pool files, sessions/devices, and audit filters.**
- [ ] **Step 6: Add tray and desktop notifications for task completion, errors, and membership changes without exposing file contents in notification text.**
- [ ] **Step 7: Run UI tests and verify all pass.**
- [ ] **Step 8: Commit as `feat: add desktop collaboration UI`.**

### Task 10: Integrate retention, revocation, audit export, packaging, and release checks

**Files:**
- Modify: `server/internal/retention/jobs.go`, `server/internal/audit/service.go`, `server/internal/http/*`
- Create: `server/internal/http/audit_handlers.go`, `server/internal/http/admin_handlers.go`
- Create: `scripts/dev-server.sh`, `scripts/package-client.sh`, `scripts/smoke-test.sh`
- Modify: `client/src-tauri/tauri.conf.json`, `client/package.json`, `README.md`
- Test: `server/tests/system_test.go`, `client/tests/smoke.spec.ts`

**Interfaces:**
- `AuditService.Append(ctx, AuditEvent) error` and `AuditService.Query(ctx, actorID, AuditFilter) (AuditPage, error)`.
- `RetentionRunner.RunOnce(ctx, now time.Time) (RetentionReport, error)`.
- Admin endpoints: `GET /api/v1/admin/audit`, `POST /api/v1/admin/users/{id}/disable`, `POST /api/v1/admin/devices/{id}/revoke`.

- [ ] **Step 1: Write a server-backed system test covering account creation, login, group creation, message send, file upload, promotion to public pool, multi-source download, expiry cleanup, and audit query.**
- [ ] **Step 2: Run the system test and verify it fails before wiring all services together.**
- [ ] **Step 3: Connect retention and revocation to every read/write path.** A disabled account, removed member, expired file, or revoked device must fail new chunk grants immediately.
- [ ] **Step 4: Implement audit filtering and CSV/JSON export with stable UTC timestamps and actor/device fields.**
- [ ] **Step 5: Add Tauri Windows/macOS bundle configuration, tray/autostart permissions, and signed-build placeholders.** Keep code signing/notarization configuration environment-driven.
- [ ] **Step 6: Add a smoke script that starts the server, creates a temporary account, launches a local transfer, verifies the hash, and shuts everything down cleanly.**
- [ ] **Step 7: Run `make test`, the system test, and the smoke script; verify all pass on the development host.**
- [ ] **Step 8: Commit as `test: add end-to-end smoke coverage and packaging checks`.**

## Self-Review

The plan covers the approved design: local accounts and roles (Tasks 2–3), group chat and retention (Task 3), temporary files and the administrator public pool (Task 4), server storage and expiry cleanup (Task 5), desktop-only background transfers (Task 6), authenticated direct-first P2P with relay/server fallback (Tasks 7–8), Windows/macOS UI (Task 9), and audit/release verification (Task 10). The shared API and database contracts are established before the client depends on them. The review-focus failure modes each have an owning test task.

The plan deliberately leaves STUN/TURN vendor choice, exact chunk size, and Tauri versus Electron alternatives to implementation validation; those are configuration or compatibility choices that do not change the approved product behavior.

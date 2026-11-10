# LinkNestX 0.2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a locally verified Web/desktop messaging redesign with contacts, read receipts, visible file transfers, fixed server binding, and signed incremental client updates; leave production deployment for the user's return.

**Architecture:** Go remains the authoritative session, ACL, social, message and file service. React shares conversation UI through desktop/browser adapters; Rust owns native files, metrics, P2P and update installation. Explicit capability boundaries prevent Web clients bypassing their restrictions.

**Tech Stack:** Existing Go/SQLite, React/TypeScript/CSS, Rust/Tauri; use cached dependencies where available.

**Spec:** `docs/superpowers/specs/2026-11-10-collaboration-v2-design.md`

## Global Constraints

- No connections, deployment or edits to `192.168.31.41` in this work; no production writes, publication, global tools, or original Nginx changes.
- Preserve earlier staged/unstaged changes; baseline saved under `.cache/next-release/baseline.tar.gz` and diff/manifest files. Work on existing `feature/lan-collaboration` checkout without resetting it; this is the user's continuing workspace and avoids losing the uncommitted shipping baseline.
- Windows x64/macOS desktop, browser same origin; private HTTP exception/public HTTPS checks remain. Desktop login target fixed in Rust at `http://192.168.31.41:18080`, not user/JS/storage configurable.
- Web max file `1_000_000_000` bytes, one active server download/session, no file uploads, P2P, native task manager or updates; avatar editing desktop only.
- Normal files30days, pool forever, messages/audit2calendar years, max desktop file20GB. Maintain auth/disabled/revoked/expired ACLs.
- Friend approval precedes new DM sends. Digital ID stable/unique. Visible-message exact read IDs, max100/batch; no cursor jump marking unseen messages.
- User cannot disable P2P participation; no sharing/P2P UI. Server fallback, authorization, disk limits and logout still work.
- Build honest evidence: signed real block reuse, no unsafe update execution; no Windows runtime claims without a Windows runtime.

## Review Focus

- Browser cookies replayed through native endpoints must not unlock uploads/P2P or admin writes (Task1 HTTP tests).
- Admin and outsider access to internally group-backed DM must not bypass pair membership, including files, sources and readers (Task1 composed ACL tests).
- Visible-message holes, account switching, old asynchronous responses and socket reconnect must not mark unread messages or leak private state (Tasks1/3).
- Pause/restart/cache/retry/parallel tasks must not inflate speed, elapsed time or progress; clipboard sources must not grant arbitrary path reads (Task2).
- Corrupt signatures/blocks, expired/wrong platform releases, active-transfer race and interrupted installation must leave old executable usable (Task4).

## Shared Interfaces (freeze before implementation)

Server owner writes final checked contract to `docs/next-release-contract.md` before UI consumes it. Paths below are the initial concrete contract; refinements must be sent to dependent owner and recorded, not guessed independently.

- User/Profile extends `{id,username,email,roles,disabled,mustChangePassword}` with `{accountNumber:string,displayName:string,avatarUrl?:string}`. Avatar URL is same-origin authenticated path or data supplied through native profile API, never arbitrary remote URI. Profile lookup `GET /api/v1/profiles/{userID}`, own patch `PATCH /api/v1/profile`, own avatar `PUT /api/v1/profile/avatar`, number lookup `GET /api/v1/contacts/search?accountNumber=...`.
- `GET /api/v1/contacts` returns `{friends:Profile[],incoming:FriendRequest[],outgoing:FriendRequest[]}`; request POST `/api/v1/friend-requests {accountNumber}`; action POST `/api/v1/friend-requests/{id}/{accept|reject|cancel}`; DELETE `/api/v1/contacts/{userID}`. Request contains `{id,from:Profile,to:Profile,status,createdAt}`.
- `GET /api/v1/conversations` returns `Conversation[]`: `{id,kind:'group'|'direct',title,avatarUrl?,peer?:Profile,memberCount,unreadCount,lastMessage?,canSend}`. Direct open `POST /api/v1/conversations/direct {userId}` returns Conversation. Internal id may equal groupId with strict direct isolation.
- Conversation messages `GET /api/v1/conversations/{id}/messages?before=&after=&limit=50` returns `{messages:Message[],nextCursor,hasMore}`; absent cursor gives newest page in ascending order. `POST .../messages {clientMessageId,body}`; fields retain existing groupId for compatibility plus conversationId, sender profile, readCount/readByMe, attachment metadata. Files continue existing upload by internal groupId, but DM upload requires current friendship and pair-only ACL.
- `POST /api/v1/conversations/{id}/read {messageIds:string[]}` returns confirmed read state; `GET .../messages/{messageId}/readers` returns `{readers:ProfileWithReadAt[],count}`. Events add `conversation.changed`, `contacts.changed`, `profile.changed`, `read.changed`, limited to entitled users. Old message.created remains usable with both IDs.
- Browser auth `POST /api/v1/web/login`, `POST /api/v1/web/logout`, `POST /api/v1/web/refresh`, `GET /api/v1/web/session`; response sanitized session (no persistent bearer in JS) plus CSRF token/capabilities. Browser API uses same-origin credentials+CSRF; existing `/api/v1/ws` supports same-origin cookie. `GET /api/v1/web/files/{versionID}/download` streams file and enforces size/concurrency/ACL.
- Public `/downloads` page and `GET /api/v1/releases` list actual published artifacts. `/releases/...` static allowlisted files and `/updates/{platform}/{arch}/latest.json` signed release endpoint owned Task4. Web build served from configured static directory, unknown API remains404; tests use temporary asset dir.
- Native `client_configuration()` returns `{serverOrigin,version,platform,capabilities}`; production login ignores/rejects any attempted custom URL. Native attachment commands `pick_upload_files`, `read_clipboard_attachments`, `discard_attachment`, `start_attachment_upload` use bounded one-use `{id,name,size,mimeType?}` handles; trusted native drop emits `attachments.prepared`. `start_attachment_upload {groupId,sourceHandle,clientAttachmentId}` returns taskId. Test-only arbitrary-origin auth constructor remains separate from shipped command.
- Existing TaskSnapshot adds optional `groupId`, `clientAttachmentId`, `metrics` object: `{measuredSince,activeDurationMs,transferDurationMs,wallDurationMs,networkBytes,creditedNetworkBytes,currentBytesPerSecond,averageBytesPerSecond,etaSeconds,revision}`; unknown times/rates nullable, extra cache/resumed data allowed. Generation-safe events use same `transfer.updated` route. UI native task data remains in one store shared by chat/center.
- Update commands `get_update_status`, `check_for_updates`, `install_ready_update`, `defer_update`; event `update.status`, status `{state,version?,downloadedBytes,reusedBytes,totalBytes,error?}`. Fixed current-user install policy and signed manifest path, no arbitrary URL/path/shell in IPC.

### Task 1: Server identity, conversations, receipts and restricted Web

**Files:** new `server/internal/social/*`, `server/internal/http/{social_handlers,web_handlers,web_download,web_assets}.go`, new migration(s) `008_*`; modify auth/repository/service, groups/chat/files ACLs, HTTP middleware/router, config/startup as required; `docs/next-release-contract.md`, `api/openapi.yaml`; tests beside features.

**Interfaces:** Produces exact server DTOs/routes above and browser auth contract. Consumes existing auth/files/chat/hub, public release handlers registered later by Task4.

- [x] Add failing migration/profile/number-collision tests and implement stable IDs/profile/avatar normalization with limits.
- [x] Add request/accept/race/delete-friend tests and implement pair uniqueness and DM strict isolation, including admin and file/P2P paths; compatibility group tests stay green.
- [x] Add exact read IDs holes/duplicate/unauthorized/current-member/list-count tests; implement newest-first paging, conversation summaries/unread/receipt events and retention cleanup.
- [x] Add browser auth/Origin-CSRF/revocation/cookie-vs-native endpoint tests, implement persisted capability restrictions and real same-origin WebSocket auth.
- [x] Add streaming download threshold/one-active/cancellation/revoke tests, configured SPA assets with API404, and browser session DTOs for frontend.
- [x] Run focused normal/race tests, then complete server normal/race/vet when frozen. Write report with commands/results and diff against baseline; no server connections.

### Task 2: Native transport statistics, trusted attachments and defaults

**Files:** new `client/src-tauri/src/{deployment,attachments}.rs`, `transfer/metrics.rs`; modify transfer models/db/service/upload/download/scheduler, auth session streaming helpers, peer payload observers as needed; commands/build/main/lib/permissions; native regression tests. Own Cargo manifest/lock additions for this task with coordination.

**Interfaces:** Produces configuration/attachment/metrics IPC above. Defines activity snapshot/gate interface for Task4 after transfer work freezes. Existing upload by groupId preserved for Task1 DM mapping.

- [x] Write deterministic clock tests: pause/restart/zero/cache/retries/concurrent chunks; implement persisted metrics and bounded real payload sampling without per-buffer DB writes or stale worker overwrite.
- [x] Verify socket/stream upload replay and download partial progress, existing P2P/resume/integrity suites; native snapshot revision guards stale progress.
- [x] Test old policy false/zero migration and setter rejection; implement permanent authorized participation while preserving grant/disk/logout boundaries.
- [x] Test fixed production server overriding legacy saved origin, expose config; keep integration tests able to use loopback without shipping a bypass.
- [x] Test one-use owner-scoped handle, expiry/cancel/logout/temp cleanup/path rejection; implement picker, native drop, Windows/macOS clipboard file/image ingress and new upload command. Native hooks only publish after trusted input.
- [x] Run focused meaningful tests, fmt and all native compile/tests when frozen; report platform runtime gaps. Do not edit React UI owned Task3.

### Task 3: Shared browser/desktop messaging UI redesign

**Files:** new runtime/browser adapter, capability/config and transfer store modules; contacts/conversation/profile/composer/read-receipt/transfer-card components; modify AppShell/auth/main/features/CSS; frontend tests and preview fixtures; use existing UI packages.

**Interfaces:** Consumes frozen server contract from Task1, native Task2 IPC, Task4 update IPC. Can implement layout/fixtures in parallel; adapt exact endpoints after contract is written.

- [x] Meaningful frontend red tests for no server field, capability restrictions, friend workflow, exact visible reads, attachment drafts/inline task progress and same-store center; no need tests that mirror CSS.
- [x] Build shared transport selection with cookie/browser socket and native adapter; no long-term browser JWT storage. Web file link streams without 1GB Blob, accurately describes browser-managed save.
- [x] Implement compact IM shell, conversation sidebar, avatars/profile/friend requests/direct chats, read status/readers, newest history pagination and stale-request protection.
- [x] Implement composer multi-file draft/paste/drop via authorized native handles, keyboard alternatives, proper text paste, cancel/retry and upload task placement; file downloads show inline metrics and central queue.
- [x] Remove all sharing/P2P settings/status/source counts and server editing. Keep valid transfer preferences. Add real download-client page/action, desktop update controls and future module registration boundary without fake knowledgebase UI.
- [x] Run full frontend tests, tsc/build; render desktop+mobile states with browser and inspect screenshots/a11y/overflow. Record data source fixture vs real backend.

### Task 4: Signed incremental updater and release pipeline

**Files:** new native `update/*`, early helper mode, `scripts/releases/*`, `server/internal/releases/*`, release HTTP routes/tests; build version0.2 files and update pubkey resource. Coordinate native main/commands/state with Task2 after freeze; config/router with Task1. New docs update/deployment prep.

**Interfaces:** Implements update IPC/status above and ActivityGate integrated with active native transfers/exports/peer sends; release service static manifest+artifact catalog; consumes compiled server origin and existing shutdown.

- [x] Write fixture proving identical target from COPY/DATA with reusedBytes>0 and downloadedBytes<fullSize. Test corrupted base/chunk, wrong signature/product/platform/version/expiry/path/size/range and verified full fallback.
- [x] Implement bounded block recipe, pinned Ed25519 key verification before network payload, persistent resume/monotonic versions, background check/download and ready/deferred statuses. Production private key only ignored0600 local path, public key build resource; test key separate.
- [x] Implement offline release generator/signing/verification with real two-version fixture and actual artifact manifest; public server artifact routes cannot traverse or expose unrelated data and only advertise existing verified packages.
- [x] Test activity check/install races and interrupted swap journal, duplicate helper, unhealthy new app rollback; implement early trusted helper mode with revalidation/current-user targets/no shell strings, safe restart and local health acknowledgment. Preserve active transfers by deferring by default.
- [x] Test Mac temporary bundle replacement and Windows-specific code compilation where available. No fake successful install on unsupported/read-only path; retain explicit Windows runtime limitation. Build0.2 mac app and verify updates with local artifacts.
- [x] Document first0.2 manual install, next signed release process, key custody and tomorrow's deployment bundle; no publication or deployment.

### Task 5: Integration review, regression and delivery

**Files:** local integration test/harness scripts and documentation as required; no broad unrelated refactors.

- [x] Review scoped diffs from Tasks1–4 against this spec; fix concrete blocker failures with original owners and scoped rechecks. Preserve baseline evidence and do not reset earlier staged changes.
- [x] Run complete Go normal/race/vet, frontend tests/tsc/build, native tests/fmt/build, real local browser login/group+DM/read/download and native transfer/update fixture paths. Run each broad gate once per relevant source freeze, repeat only failures or changed scope.
- [x] Verify old schema→new migration retains users/files/messages and approved rights; Web and desktop auth/events share server state correctly.
- [x] Capture polished UI screenshots from actual rendered components; produce local 0.2 app/artifacts that current environment supports, signed update fixture/release files and tomorrow deployment checklist with exact validation gaps.
- [x] Final report lists completed behavior and material limits, links artifacts/docs, explicitly server untouched. Do not turn remaining Windows/offline-server validation into a false completed claim.

## Local execution closure — 2026-11-10

The checked steps cover the authorized local implementation, reviews and supported-environment verification. Final results and evidence are in `docs/verification/v2/final-report.md` and `.superpowers/sdd/2026-11-10-collaboration-v2-plan/progress.md`. Windows 0.2 compilation/runtime, interactive native OS acceptance, complete installed GUI upgrade and production deployment remain explicit release gates. The server was not contacted; existing staging and the current branch are retained without committing or merging.

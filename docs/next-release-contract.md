# LinkNestX 0.2 shared API contract

Frozen server contract for Tasks 1–4, 2026-11-10. All routes are same-origin `/api/v1`. JSON errors use `{error:string}`. Protected requests use desktop Bearer or browser cookie; never both.

## Identity and contacts

`Profile` is `{id,username,email?,roles?:string[],disabled,createdAt,mustChangePassword,accountNumber:string,displayName:string,avatarUrl?:string}`. Account numbers are immutable unique decimal strings, at least eight digits. `GET /profiles/{userID}` returns Profile; `PATCH /profile {displayName}` returns own Profile (desktop only); `PUT /profile/avatar` accepts raw PNG/JPEG, at most 2 MiB, returns own Profile (desktop only). `GET /profiles/{userID}/avatar` returns normalized PNG under authentication. Avatar URLs always use that same-origin path.

`GET /contacts/search?accountNumber=...` returns Profile or 404. `GET /contacts` returns `{friends:Profile[],incoming:FriendRequest[],outgoing:FriendRequest[]}`; pending requests only in incoming/outgoing. `FriendRequest = {id,from:Profile,to:Profile,status:'pending'|'accepted'|'rejected'|'cancelled',createdAt}`. `POST /friend-requests {accountNumber}` returns FriendRequest (201, including retry). `POST /friend-requests/{id}/{accept|reject|cancel}` takes `{}` and returns FriendRequest (200). `DELETE /contacts/{userID}` returns 204 and preserves old direct history.

## Conversations and receipts

`GET /conversations` returns `Conversation[]`: `{id,kind:'group'|'direct',title,avatarUrl?,peer?:Profile,memberCount,unreadCount,lastMessage?:Message,canSend:boolean}`. Only joined groups and own direct pairs appear. `POST /conversations/direct {userId}` returns Conversation (200), requires current accepted friendship. Direct id equals internal groupId; ordinary group management cannot list or mutate direct pairs. File upload uses this id; current friendship required for new direct messages/uploads, while existing history remains accessible after deleting friendship.

`GET /conversations/{id}/messages?before=&after=&limit=50` returns `{messages:Message[],nextCursor:string,hasMore:boolean}`. Opaque cursors match `Message.cursor`; before/after are mutually exclusive. No cursor selects newest page; messages within every page are ascending. Before page nextCursor is its oldest item; after page nextCursor its newest. Limit 1–200. `POST /conversations/{id}/messages {clientMessageId,body}` returns Message (201).

Message preserves `{id,sequence,groupId,senderId,clientMessageId,body:string|null,createdAt,expiredAt?,cursor,fileId?,fileVersionId?,attachmentName?,attachmentSize?,attachmentStatus?}` and adds `{conversationId,sender:Profile,readCount:number,readByMe:boolean}`. Expired messages expose no content. Own messages are excluded from readCount and unreadCount.

`POST /conversations/{id}/read {messageIds:string[]}` (1–100 IDs, duplicates allowed) returns `{messageIds:string[]}` of confirmed unique IDs. Validation is atomic: foreign/expired/unknown IDs reject whole batch. Exact IDs, no implicit sequence frontier. `GET /conversations/{id}/messages/{messageId}/readers` returns `{readers:(Profile & {readAt:string})[],count:number}`. Count and list use current enabled entitled members and exclude sender. Reads survive leave/rejoin; expired-message records are removed during retention.

WebSocket `/ws` automatically pushes every currently entitled conversation; accepts `ping` and `message.send`, and keeps existing `{type:'message.created',groupId,message}`. Add `conversationId` to message payload. New invalidations `{type:'contacts.changed'|'profile.changed'|'conversation.changed'|'read.changed',groupId?,conversationId?,userId?,messageIds?}` require REST reload. Contact/profile changes target entitled users; conversation/read events target currently entitled members. Browser socket uses same-origin cookie and mandatory exact Origin, no tokens in URL.

## Browser session

`POST /web/login {username,password}` (username also accepts accountNumber) requires exact same-origin `Origin`; sets `lnx_web` HttpOnly SameSite=Strict Path=/ cookie (Secure on HTTPS; trusted configured proxies may supply exactly one X-Forwarded-Proto, otherwise forwarded protocol is ignored). Returns `WebSession = {user:Profile,expiresAt:string,csrfToken:string,capabilities:{upload:false,p2p:false,adminWrite:false,profileEdit:false,nativeTransfers:false,updates:false,maxDownloadBytes:1000000000}}`. No bearer or refresh token in response JSON. `GET /web/session` returns same WebSession. `POST /web/refresh {}` rotates cookie and CSRF, returning same DTO. `POST /web/logout {}` revokes session, clears cookie, returns 204. Web session / logout / refresh remain available before initial password change; `/auth/password` is permitted with browser auth and CSRF.

All browser writes require exact same-origin Origin and `X-CSRF-Token`; refresh/logout included. Browser credentials persist a server-side web capability type, and native token/refresh routes cannot elevate it. Browser requests may read conversations/contacts/profiles/files/groups/me and own devices, send messages/friend actions/read receipts, change their own password, revoke their own devices. Upload/P2P/administrative writes/avatar/profile edits are denied server-side. Native manifest/chunk/grant routes are denied to web sessions; file metadata remains readable.

`GET /web/files/{versionID}/download` streams an authenticated file with Content-Disposition and known length, requiring size <= 1,000,000,000. One active stream per browser session; a concurrent stream returns 409, oversize returns 413. No Range support (Range rejected 416). HEAD checks metadata without claiming transfer. ACL/session checked repeatedly while streaming; cancellation releases stream slot. Browser saves are managed by the browser, and server completion does not assert a local save.

## Reserved release integration

Task4 owns `/downloads`, `/api/v1/releases`, `/releases/...`, `/updates/{platform}/{arch}/latest.json`, and public root content. Task1 exposes `config.Config.WebAssetsDir` (environment `LINKNESTX_WEB_ASSETS_DIR`) and SPA `/app/` serving. Unknown `/api/...` always returns 404. Static directory contains built frontend only; missing assets return 404 and non-asset application paths fall back to index.html.

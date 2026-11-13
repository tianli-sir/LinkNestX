# 批次 2 设计：内联图片 / 管理员改用户名 / 真删除账号 / 打开文件

**日期：** 2026-11-13
**状态：** 已获用户确认
**前置：** 批次 1（群昵称/未读角标/图标统一）已合并并部署 0.4.0

## 用户确认的关键决策

| 决策点 | 选择 |
|---|---|
| 真删除时历史消息 | **匿名保留**（显示"已注销用户"），不删消息与文件 |
| 匿名实现方式 | **匿名行方案**：全局共享占位 users 行吸收所有外键引用，被删 users 行真删除 |
| 内联图片上限 | **不设上限**，超大图走缩略图+点开原图 |
| 图片渲染 | **缩略图（最长边 480）+ 灯箱原图懒加载** |
| 覆盖范围 | **含 Win7 兼容包**（legacy Electron 壳四处同步） |

---

## A. 聊天内联图片

### 现状
粘贴图片与普通文件走完全相同路径：`read_clipboard_attachments` → 附件上传 → `AppendAttachment` → `FileMessage` 文件卡片（无图片区分）。file_versions 无 mime 列；头像（`/api/v1/profiles/{id}/avatar`）与 documents 预览（`documents.go:128`，10MiB 上限）是现成的"鉴权取回图片"双端先例。

### 服务端

**消息模型**：`messages` 加列 `attachment_kind TEXT NULL`（迁移 013，值 `image` 或 NULL）。`AppendAttachment` 按 `attachmentName` 扩展名（png/jpg/jpeg/gif/webp/bmp，大小写不敏感）设置；`messageColumns`/`Message` 结构带出 `attachmentKind`。openapi `Message` schema 同步。**不引 mime 列**——扩展名判断 + 服务端 preview 路由的 `http.DetectContentType` 双重嗅探兜底（preview 时扩展名说图片但内容不是 → 415，客户端回退文件卡片）。

**原图路由** `GET /api/v1/files/{versionID}/preview`（新）：
- 鉴权：登录会话 + 该版本所在文件对当前用户可见（照抄 documents `Preview` 的 manifest 校验与群文件 ACL 逐块重鉴权模式 `documentImageReader`）
- 流式（不确定 Content-Length，chunked），`http.DetectContentType` 嗅探，仅放行 image/png|jpeg|gif|webp，否则 415
- 响应头：`no-store, nosniff, CSP sandbox`，无大小上限
- 文件状态 unlisted/expired/deleted → 404/410 与现有下载语义一致

**缩略图路由** `GET /api/v1/files/{versionID}/thumb`（新）：
- 同上鉴权；解码原图（Go 标准库 `image`+`image/jpeg`+`image/png`+`image/gif`，gif 取首帧）→ 等比缩放最长边 480 → 编码 png（无透明通道损失，gif/webp 转码 png）
- **进程内缓存**：`sync.Map` keyed by versionID，上限 256 条 LRU 淘汰（够单企业部署）；解码失败（含 webp：标准库不支持）→ 302 到原图路由由前端自己缩放（CSS max-width/height）
- 解码内存防护：`image.DecodeConfig` 预检像素数 > 8e8（约 800MP）拒绝生成缩略图，直接 302 原图

**事件**：无新事件（图片消息即普通 file 消息）。

### 桌面端（Tauri）

**新命令 `file_attachment_preview {versionId}`**（仿 `document_attachment_preview`）：鉴权（任务清单查到该 version 的可见性）→ 经已鉴权 HTTP 客户端拉 `/api/v1/files/{versionID}/preview` → 校验总字节数 ≤ 256MiB（防内存炸；超过返回错误，前端回退提示"图片过大，请下载查看"）→ `data:image/...;base64` 返回。缩略图同型命令 `file_attachment_thumb`（≤20MiB 校验）。
- 权限三件套：`permissions/native-service.toml` commands.allow、`capabilities/main.json`、`main.rs generate_handler!`

**网页端**：直接 `<img src="/api/v1/files/{vid}/thumb">`（cookie 会话），失败 onerror 回退文件卡片。

### 客户端渲染

`FileMessage` 分流（`messaging/types.ts` 加 `attachmentKind?:'image'|null`）：
- `attachmentKind==='image'`：`<img>` 缩略图（懒加载 `loading="lazy"`，CSS `max-width:320px;max-height:240px;object-fit:contain`，圆角+点击样式）；点击 → **灯箱**（fixed 全屏遮罩、原图 `<img loading="lazy"` 居中、点击遮罩/Esc 关闭、原图加载中显示 spinner、加载失败显示"图片加载失败"+下载按钮）
- 非图片：现有文件卡片不变
- `AttachmentImage` 复用点：桌面 `desktop.invoke('file_attachment_thumb')` / 网页直接 src；结构与 documents 的 `SafeMarkdown.tsx AttachmentImage` 一致

**Win7 壳**：网页路径天然可用（cookie 会话），无需改 legacy。

### 测试
- 服务端：kind 列迁移测试；preview/thumb 鉴权矩阵（成员/非成员/直聊双方/unlisted）；415（伪装图片）；thumb LRU 与 302 回退；DetectContentType
- 客户端：FileMessage 分流渲染（图片/非图片/onerror 回退）；灯箱开关与 Esc；vitest

---

## B. 管理员改用户名

### 服务端
`PATCH /api/v1/admin/users/{id}/username`（body `{username}`）：
- 管理员事务（仿 `UpdateAccountNumber`/`SetUserDisabled` 的 `accountTransaction` 模式）
- 校验：非空/trim/≤256 rune/utf8 有效（同 `validateCreateUser`）；`SELECT 1 FROM users WHERE lower(username)=lower(?) AND id<>?` 防大小写变体重名
- UPDATE（既有 `username_number_collision` UPDATE 触发器自动防护数字账号撞号，ABORT 错误映射 409）
- 审计 `account.username_changed`（detail 记旧名→新名）
- 广播 `profile.changed{userId}`（现有事件，客户端各监听处自动刷新）
- 404 目标不存在；403 非管理员；409 重名；400 格式非法

### 客户端
`AdminView` 新 action kind `'username'`：行按钮"改用户名" → 表单输入新名 → 确认文案"旧名 → 新名。改名后用户需用新名或数字账号登录。" → PATCH → 列表刷新。
openapi 同步 + user-guide 管理小节补一句。

### 测试
- 服务端：改名校验矩阵（成功/空/超长/重名大小写变体/自身可改/触发器撞号 409/审计落库/事件广播）
- 客户端：AdminView 流程 vitest

---

## C. 真删除账号（匿名行方案）

### 占位用户
全局唯一占位行（lazy 创建于首次删除事务内）：`id=<固定 UUID 常量 DELETED_USER_ID>`、`username='deleted-user'`、`disabled=1`、`must_change_password=1`（永不登录：登录路径 `ValidateActor` 拒绝 disabled）、profile 行 `display_name='已注销用户'`。审计与 UI 无需特判——disabled 用户在成员列表已有"（已停用）"渲染路径，ProfilePanel 对 disabled 头像无资料按钮（批次 1 已有）。

### 服务端
`POST /api/v1/admin/users/{id}/delete`（无 body）：
- 管理员事务：
  1. 保护：不能删自己；不能删其他管理员（**简化：仅允许删除非 admin 角色用户**，比"最后一名管理员"判定更保守且满足企业场景——删管理员先停用其角色）；目标不存在 404；目标已是占位行 400
  2. lazy 确保占位行存在（`INSERT OR IGNORE` users+profiles）
  3. `UPDATE messages SET sender_id=DELETED_USER_ID WHERE sender_id=?`
  4. `UPDATE files SET created_by=DELETED ...`、`file_versions`、`documents.author_id`、`upload_sessions.user_id`（进行中会话作废：同时 `UPDATE upload_sessions SET status='cancelled' WHERE user_id=? AND status IN ('active','pending')`）
  5. `DELETE FROM group_members WHERE user_id=?`（既有 changeMember 路径会写系统消息——这里直接删行，删除账号本身广播事件让客户端刷新）
  6. `DELETE FROM friend_requests WHERE from_id=? OR to_id=?`；`DELETE FROM friendships WHERE pair_low=? OR pair_high=?`（对端联系人列表经 contacts.changed 刷新）
  7. `DELETE FROM direct_conversations WHERE pair_low=? OR pair_high=?`（对端的私聊会话消失——**设计决策：私聊双方共组一个 group，删除一侧即解除整个私聊会话**；对端历史随会话消失，这与"移除好友"的现有行为一致）
  8. `DELETE FROM users WHERE id=?`（profiles/sessions/devices/user_roles/message_reads/peer_sources 经 FK CASCADE 清掉）
  9. 审计 `account.deleted`（actor_id=admin，detail 记被删 username）
  10. 广播：`profile.changed{userId}` + 每个受影响会话 `conversation.changed`
- 群主处理：目标是群主（groups.created_by 指向）→ `UPDATE groups SET created_by=DELETED_USER_ID`（群主位显示"已注销用户"；**不做群主转移**——企业场景管理员可另行管理群）

### 客户端
AdminView 行按钮"删除账号"（红色危险样式，仅非 admin 用户行显示）→ 确认弹窗要求**输入该用户名**确认 → POST → 列表刷新。user-guide 增补"删除账号"小节（说明匿名保留与不可恢复）。

### 测试
- 服务端：删除矩阵（普通用户成功/消息匿名化/文件归属/好友双向清理/私聊双侧消失/群主群保留/不能删自己/不能删管理员/占位行幂等/审计/事件）；占位用户登录被拒
- 客户端：确认弹窗流程 vitest

---

## D. 打开文件/文件夹（含 Win7 壳）

### Rust（Tauri）
- `TaskSnapshot`（`models.rs`）加 `local_path: Option<String>`（从任务库 `data.path` 带出；旧任务无此字段反序列化为 None，向后兼容）
- 新命令（**只接受 taskId，服务端反查路径，绝不接受前端传路径**）：
  - `open_downloaded_file {taskId}`：任务库反查 → 校验 direction=download、state=completed → `symlink_metadata` 存在性检查（不存在返回 `FileMissing` 错误码）→ Windows `ShellExecuteW "open"` / macOS `open`
  - `reveal_downloaded_file {taskId}`：同上 → Windows `explorer.exe /select,<path>` / macOS `/usr/bin/open -R`
  - `check_downloaded_files {taskIds}`：批量存在性检查返回 `{taskId: exists}`
- external.rs 的 checked_url 安全策略**不动**（它拒绝本地路径是正确的）；新命令走独立路径校验（任务库反查即是白名单）
- 权限三件套同步

### Win7 壳（legacy Electron）
- `boundary.cjs COMMANDS` + `preload.cjs` 加 `open_downloaded_file`/`reveal_downloaded_file`/`check_downloaded_files`
- `main.cjs` invoke 分支：Electron 内置 `shell.openPath(path)` / `shell.showItemInFolder(path)`（无需新 IPC；路径同样由 native host 的任务库反查——**native host.rs 协议分支新增三个命令，逻辑与 Tauri 侧共享同一 transfer 模块**）

### 前端
`FileMessage`（聊天）/`TransfersView`（传输页）/`FilesView`（群文件表）三处统一分流：
- 有本地 completed 下载任务（按 fileId/versionID 匹配）且文件存在 → 「打开文件」「打开文件夹」按钮
- `open/reveal` 返回 `FileMissing` → 前端立即回退「下载」按钮并提示"文件已不在原位置"
- 挂载/传输完成时经 `check_downloaded_files` 批量刷新存在性；不轮询
- 网页版（无桌面能力）：保持下载按钮不变（浏览器无法可靠打开本地文件）

### 测试
- Rust：命令矩阵（存在/缺失/非下载任务/未完成/伪造 taskId 反查不到）；权限清单
- 客户端：三视图按钮分流与 FileMissing 回退 vitest
- legacy：boundary 白名单单测（现有 smoke 模式）

---

## 实施批次划分（并行）

| 批次 | 内容 | 主要文件域 |
|---|---|---|
| S（服务端） | 迁移 013 + preview/thumb 路由 + 改名 + 真删除（A/B/C 服务端全部） | server/ |
| C1（现代客户端） | FileMessage 分流+灯箱 + AdminView 两操作 + 三视图打开按钮 + Tauri 命令 | client/src, src-tauri |
| L（Win7 壳） | boundary/preload/main.cjs/native host 四处 + smoke | client/legacy |

依赖：S 先行（API 契约 openapi 先定）；C1 与 L 可与 S 并行（mock 契约先行），最后合流全量验证。

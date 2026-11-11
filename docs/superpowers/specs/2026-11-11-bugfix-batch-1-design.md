# 设计：Bug 批 1 —— 图标统一 / 群昵称 / 未读角标

日期：2026-11-11
状态：已确认（用户逐节批准）
范围：用户报告的 9 个问题中的第一批（纯 Bug 修复）。后续批次：群组功能批（群解散、图片消息）、账号注册批（管理员删号、开放注册、管理员改名）、下载来源显示。

## 背景

LinkNestX 是自托管团队聊天 + 文件协作平台（Go 服务端 + SQLite、React 网页版、Tauri 2 桌面端、Electron 22 Win7 兼容包）。本批修复三个日常使用中最影响体验的缺陷：

1. **#1 图标不统一**：桌面端图标（蓝底白"L"）与官网品牌标记（线条立方体）完全不一致。
2. **#3 群昵称污染全局名**：群聊中点自己头像改"展示名"实际改写全局 `profiles.display_name`，导致加好友、其他群、全局资料全部跟着变。系统本无群昵称概念。
3. **#8 未读角标异常**：桌面端会话列表角标显示约等于全部消息数的数字，看完消息也不消失。

---

## 第 1 节：图标统一（#1）

### 现状（已逐项核实）

| 触点 | 现状 |
|------|------|
| `client/src-tauri/icons/`（icon.png/.ico/.icns） | 128px 单尺寸蓝底白"L"占位图，是仓库唯一图标源 |
| `client/src-tauri/src/tray.rs:38-52` | Rust 程序化绘制的另一个蓝"L"（32×32 RGBA 缓冲） |
| 仓库全域 | **无任何 favicon**（路由、HTML `<link>` 均无） |
| `client/src/components/Icon.tsx` `nest` 键 | Phosphor "Stack" 图标，靛蓝底（`desktop.css` `.ln-brand-mark` #4059e8） |
| Win7 兼容包 | exe 为**原生 Electron 图标**（打包仅改名，无 rcedit）；NSIS 无 MUI_ICON；托盘图标打包时从 `client/src-tauri/icons/icon.png` 拷贝（`scripts/package-legacy-windows.ps1:47`） |

### 目标品牌规范

- 唯一标记：官网 `landing.html:373` 的 `<symbol id="mark">` 线条立方体（viewBox `0 0 32 36`，stroke-width 2；downloads.html:178 有逐字副本，同步替换为同源引用）。
- 配色（landing.html:10 调色板）：绿 `#17644e`、薄荷 `#b4f3d5`、夜蓝 `#101e2b`、纸白 `#f7f9f8`。
- 应用图标呈现：纸白圆角方底（圆角绘制在源图内，保证 Windows/macOS 呈现一致）+ 绿色线条立方体（忠实官网页头观感）；小尺寸（≤32px）变体加粗描边保证可辨。

### 改动清单

1. **新增 `brand/mark.svg`**：唯一图标源（含小尺寸加粗变体说明）。
2. **重生成 `client/src-tauri/icons/` 全套**：用 `tauri icon`（Tauri CLI v2）从 ≥1024px 源图生成各尺寸 PNG、`.ico`、`.icns`，产物提交仓库。`tauri.conf.json` 的 `bundle.icon` 引用路径不变。
3. **重写 `tray.rs`**：删除程序化绘制的蓝"L"，改为 `include_bytes!` 内嵌同一源生成的 32px PNG（与图标源一致，避免手写光栅逻辑）。
4. **服务端 favicon 路由**：`server/internal/http` 新增 `GET /favicon.ico`、`GET /favicon.svg`（go:embed 二进制），landing.html 与 downloads.html `<head>` 加 `<link rel="icon">`。
5. **网页版**：新建 `client/public/favicon.svg`（vite 复制到 dist 根 → `/app/favicon.svg`），`client/index.html` 加 `<link rel="icon" href="./favicon.svg" type="image/svg+xml">`。
6. **React 应用内品牌**：`Icon.tsx` `nest` 键替换为线条立方体 SVG 组件；`desktop.css` `.ln-brand-mark` 配色改官网绿系（登录页绿标纸底、深色侧栏薄荷亮标）。
7. **Win7 兼容包**：托盘图标随 icon.png 拷贝自动修复；`package-legacy-windows.ps1` 加 rcedit 步骤替换 exe 图标；`legacy.nsi` 加 `MUI_ICON`/`MUI_UNICON` 与快捷方式图标；`main.cjs` BrowserWindow 加 `icon:` 选项。
8. **文档与快照**：`docs/development.md` 记录图标再生成命令；`client/tests/visual` 快照更新。

### 明确不做

- README 界面截图重拍（与代码无关，顺手时再做）。

---

## 第 2 节：群昵称（#3）

### 现状（已逐项核实）

- 群内点自己头像 → `ProfilePanel`（`client/src/features/messaging/Profile.tsx`）→ `PATCH /api/v1/profile` → `social/service.go:60 UpdateProfile` 直接改全局 `profiles.display_name`。无群昵称概念。
- 服务端名字解析漏斗：消息发送者名单点在 `chat.project()`；已读名单在 `chat/receipts.go` entitledReaders 查询；成员列表在 `groups/service.go ListMembers`。
- 客户端渲染：消息 meta 发送者名、会话预览、已读名单弹窗、成员列表行（每行单独拉全局资料的 N+1）。

### 设计

**数据库** — 迁移 `server/migrations/012_group_nicknames.sql`：

```sql
ALTER TABLE group_members ADD COLUMN nickname TEXT NULL;
```

（migrations/embed.go 自动加载；`db/migrations_test.go` 按既有模式补测试。）

**服务端名字解析（仅群上下文生效，私聊不变）**

生效名 = `COALESCE(NULLIF(gm.nickname,''), p.display_name, u.username)`：

1. `chat.project()`：群消息发送者 DisplayName 改为生效名。`ConversationPage` / 消息列表查询**一次预取整群昵称映射**（`SELECT user_id, nickname FROM group_members WHERE group_id=?`），project() 查内存 map，避免每条消息一次查询。私聊（direct_conversations 存在的 group）不套用。
2. 已读名单查询（entitledReaders）：SELECT 列加同样的 COALESCE。
3. `groups/service.go` 成员列表：Member 结构增加 `nickname`（原始值，可空，供编辑回显）与 `displayName`（生效名，供展示），查询 COALESCE。
4. 群系统消息（`groups/system_messages.go`）：**新插入**的系统消息正文使用群内生效名；历史系统消息正文中的名字是写入时固化文本，**不回溯**（已知限制；"历史跟随当前昵称"的选择仅针对消息发送者标签）。

**新接口**

```
PATCH /api/v1/groups/{groupID}/members/{userID}   body: {"nickname": string}
```

- 仅限本人（`userID` 必须等于 actor，否则 403）；空串或省略 = 清除昵称回落全局名。
- 校验同 `UpdateProfile`：trim、非空时 ≤80 runes、UTF-8 合法、无控制字符。
- 拒绝私聊会话（group 必须是真实群组，即 direct_conversations 无记录）。
- 成功后经 hub 发**会话级新事件 `nickname.changed`**（`{groupId, userId, nickname}`）；客户端在现有 `server.event` 监听处（MessagingWorkspace 会话/成员刷新、ConversationView 同步、ProfilePanel 资料刷新）增加对该类型的处理。不复用 `membership.changed`——它会触发无关的"群组动态"系统通知。
- openapi.yaml 同步登记。

**客户端**

1. `ProfilePanel` 增加可选群上下文（`group?: {id, kind}` prop，由 MessagingWorkspace 在群会话内打开时传入）：
   - 群内点**自己**头像 → 只显示"群昵称"编辑（保存调新接口；上方只读展示全局展示名），不再误改全局名。
   - 群内点**他人**头像 → 展示"本群昵称"（若有）与全局展示名。
   - 私聊/通讯录入口 → 行为不变（编辑全局展示名）。
2. 成员列表行改用服务端 Member 生效名（顺带消除 N+1 资料请求）。
3. 消息 meta、会话预览、已读名单弹窗无需改动（服务端已按群解析）。

**已被污染的数据**：不写数据修复迁移（无法猜测原值）；用户在个人资料中自行改回全局名，修复后群内入口不会再误改。

**文档**：`docs/user-guide.md` 增加群昵称说明。

---

## 第 3 节：未读角标（#8）

### 现状与根因（按证据强度排名）

已读管线：IntersectionObserver（ConversationView）→ `VisibleReadTracker`（400ms 停留）→ `POST /api/v1/conversations/{id}/read`（`chat/receipts.go:107`，**整批校验**：批内任一 ID 非法/过期则整批 400）→ `message_reads` 行 → `read.changed` + `conversation.changed` 事件 → 会话列表刷新 → 角标（`social/service.go:500` 未读 SQL 本身正确：只数"他人的、未标记已读的"消息）。

1. **主因**：`ConversationView.tsx:30` 门控含 `document.hasFocus()`。WebView2 中该值可**卡死 false**（焦点落在非客户区、托盘恢复等），而门控仅在 document focus/blur/visibilitychange 时重查 → desync 后永不恢复 → 已读永不记录 → 未读 ≈ 全部消息数且不消失。与用户反馈症状（看完不清、数字≈总数、桌面端）吻合。
2. 次因：`DesktopProvider.tsx` native 适配器 `window.foreground` 监听中 `onResized` 注册失败会抛出并放弃整个监听，`foreground` 永卡 false。
3. 帮凶：整批 400 + 客户端每 ID 重试 3 次后**静默放弃**且无再触发 → 一条过期消息可毒死整批已读。

### 修复集（保留"只有真正看到的消息才标记已读"的产品规则）

1. **门控条件**（`ConversationView.tsx:30` 门控与 `:23` announce 保持一致）：去掉桌面端 `document.hasFocus()` 依赖，改为 `active && foreground && !obscured && !readers && document.visibilityState==='visible'`。Tauri `foreground` 已是 OS 级窗口聚焦信号。
2. **浏览器端等价语义**：`browserAdapter.ts` 实现真实 `window.foreground` 事件（window focus/blur + visibilitychange 上报），使网页版在窗口失焦时同样暂停上报——语义与现状等价，不再依赖 `hasFocus`。
3. **门控重查触发**：会话滚动容器加捕获阶段 `pointerdown`/`keydown` 监听调 `gate()`（desync 自愈）；`connection` 恢复为 `connected` 时重查并重排待上报。
4. **native 适配器健壮性**：`onFocusChanged` 为主体信号先行注册；`onResized` 失败仅降级不致整体失败（foreground 不再可能卡 false）。
5. **上报重试**（`readTracker.ts`）：
   - `attempts` 计数在不可见→可见、禁用→启用转换时归零；
   - 启用期间对未确认的可见 ID 保留 30s 慢重试（不再永久放弃）；
   - 收到 400 时剔除毒丸：先整批重试一次（排除瞬态错误）；仍 400 则二分定位非法 ID 并从集合中剔除，其余 ID 继续正常上报。
6. **服务端不改**：保持整批严格语义（`social_handlers_test.go` 已覆盖），毒丸剔除在客户端完成。

### 新增测试

现有测试恰好缺少以下场景（探查报告逐项确认）：

- (a) desync 回归：`foreground=true` 且 `document.hasFocus()=false` 时仍正常上报已读；
- (b) pointerdown/keydown 触发门控重查后恢复上报；
- (c) attempts 归零逻辑 + 毒丸 ID 不引发无限重试；
- (d) 服务端 400 后客户端剔毒丸继续上报其余 ID；
- (e) native `onResized` 失败不卡死 `foreground`；
- (f) 端到端：上报成功 → conversations 刷新 → `unreadCount` 归零 → 角标消失。

### 验收

Windows 桌面端手动复现原场景：启动后不点击页面（焦点 desync 状态）滚动读完会话 → 返回列表角标为 0；切窗口/最小化回来 → 角标数字准确为未读数。

---

## 实施与测试总览

| 项 | 服务端 | 客户端 | 测试 |
|----|--------|--------|------|
| #1 图标 | favicon 路由（Go 单测：路由可达、Content-Type） | tauri 图标、tray.rs、React 品牌、Win7 打包脚本 | 视觉快照更新；构建产物检查 |
| #3 群昵称 | 迁移 012、project() 预取、readers/members COALESCE、新 PATCH 接口、nickname.changed 事件 | ProfilePanel 群上下文、成员列表生效名、监听新事件 | Go：迁移、接口权限（仅本人/拒私聊/校验）、名字解析（昵称/回落/私聊不套用）；客户端：面板交互、事件刷新 |
| #8 未读角标 | 不改 | 门控、重查触发、browserAdapter foreground、readTracker 重试 | 上述 (a)–(f) |

回滚策略：三项相互独立，可分别 revert。不涉及破坏性 schema 变更（仅加可空列）。

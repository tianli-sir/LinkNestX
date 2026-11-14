# 批次 3 设计：删除好友（连带私聊）/ 免登录 / 全局动画体系

**日期：** 2026-11-14
**状态：** 已获用户确认
**前置：** 批次 2 已合并 main（`7ffc445`）；本批完成后与批次 2 一起发 0.5.0

## 用户确认的关键决策

| 决策点 | 选择 |
|---|---|
| 删除好友后私聊会话 | **连带删除会话+消息**（双方列表消失，消息记录删除） |
| 免登录覆盖范围 | **双壳都做**（现代 DPAPI / legacy+macOS 0600 明文降级） |
| 动画技术路线 | **framer-motion**（唯一动画库，~40KB） |
| 滑动删除 | 桌面 hover 按钮 + drag 手势双入口；reduced-motion 全局降级 |

---

## A. 删除好友（连带删私聊会话与消息）

### 服务端（改 `social.Delete`，单事务）

现状 `server/internal/social/service.go:365-375` 只删 `friendships`。新事务顺序（复用批次 2 删账号验证过的清理顺序）：
1. `DELETE FROM friendships WHERE pair_low=? AND pair_high=?`（不变）
2. 查该好友对的 `direct_conversations` 行（pair_low/pair_high 命中）得 `group_id` 列表
3. 对每个 direct group：`DELETE FROM messages WHERE group_id=?`（message_reads 随 FK CASCADE）、`DELETE FROM group_members WHERE group_id=?`、`DELETE FROM direct_conversations WHERE group_id=?`
4. 提交后广播（现状 `contactsChanged` 已做）：双方 `contacts.changed` + 每个被删会话 `conversation.changed`
5. 语义：只删**私聊**（direct_conversations 有记录的群）；普通群的共同成员关系不受影响

**保护**：删除不存在的友谊 → 幂等 204（现状语义保持）；事务失败整体回滚。

**测试**（social_handlers_test.go 追加）：
- 删除后双方 `GET /api/v1/conversations` 不含该私聊；消息 `GET /api/v1/conversations/{id}/messages` 404
- 共同普通群成员关系保留（fixtureGroup 不受影响）
- 重新加好友 → 新 direct 会话可重新创建并发消息
- 既有用例（删除后发消息 403）更新：会话已删 → 404

### 客户端

- **ContactsView** 好友行：hover（触屏常显）「删除」红色文字按钮 → 行下展开 `.ln-confirm` 内联确认块（复用 GroupsView:131 先例）：文案「删除后将同时删除你们的私聊聊天记录，确定？」+「确认删除」danger +「取消」→ `DELETE /api/v1/contacts/{id}`（走既有 `action()` 包装）→ `contacts.changed` 事件自动刷新
- **ProfilePanel** friend 分支：「发送消息」旁「删除好友」次要按钮 → 面板内同一确认块 → 删除成功后 `onClose`（好友已非 friend，面板状态刷新）
- **测试**：v031 夹具扩展（DELETE 分支 mock、确认流程、删除后行消失、取消不删）

---

## B. 免登录（双壳）

### session.rs（共享源码，两壳自动同得）

- **落盘**：`login()` 成功后写 `session-{owner()}.json`（owner = hash(origin\0userId)，与 device 文件同模式；NamedTempFile+sync_all+persist 原子写先例）。内容 `{origin, session}` JSON，写前加密：
  - Windows 现代壳：DPAPI `CryptProtectData`（windows crate 加 `Win32_Security_Cryptography` feature；`#[cfg(all(windows, feature="dpapi"))]` 或直接 cfg(windows)——legacy crate 无 windows crate，unix/legacy 编译分支走明文）
  - 其他（macOS、legacy）：明文 JSON + 文件 0600 + 目录 0700（既有权限先例；企业内网边界可接受，与 device 文件同级保护）
- **刷新同步**：`rotated()` 成功后重写落盘文件（否则重启用过期 refresh_token）
- **登出清理**：`logout()` 删除落盘文件
- **新方法 `restore()`**：读盘 → 解密 → 反序列化 → 校验（user.id 非空、token 非空、disabled 拒绝）→ 调 `rotated()` 主动验证/刷新（服务器拒绝则删文件并返回 Err，前端静默回登录页）→ 填 `state` → 返回 Session
- **新命令 `session_restore`**（无参）：`restore()` 成功后复刻 login 后置流程——emit `session.identity` → `socket.start` → `transfers.restore_after_login()`（commands.rs:85-113 既有序列）

**安全边界（不变）**：token 只存在于 native 进程与落盘文件；renderer 经 IPC 收到的 Session 与 login 命令返回完全等价（现状即含 token 字段）；`desktop-auth.test.tsx:169` 的"renderer 零凭据存储"断言维持。

### 前端

- `createNativeTransport` 补 `restore: () => invoke<Session>('session_restore')` —— AuthProvider:13 已预留消费（存在即自动调）
- **防登录页闪烁**：AuthProvider 加内部 `restoring` 态（native transport 初始 true；restore resolve/reject 置 false）→ 向下提供 `useAuth().restoring`；WorkspaceRouter 在 `!session && restoring` 时渲染轻量品牌加载屏（复用 .ln-brand-mark 旋转），不渲染 LoginScreen
- **legacy 四件套**：host.rs dispatch `session_restore` 分支 + boundary COMMANDS + preload.cjs 白名单 + main.cjs 无需（透传）

### 测试

- `native_auth.rs`（axum 假服务器）：落盘→新 AuthManager 实例（模拟重启）→restore 成功且 user 正确；过期 refresh_token → restore Err + 文件删除；logout → 文件不存在；disabled 用户拒绝
- `desktop-auth.test.tsx`：新增 restore 触发断言（transport.restore 被调、session 建立）；既有断言不破
- restore 期间不闪登录页：WorkspaceRouter 级用例（restoring=true 渲染加载屏）

---

## C. 全局动画体系（framer-motion）

**依赖**：`framer-motion`（client package.json dependencies）。

**基础设施**（新 `client/src/motion/` + `styles/motion.css`）：

1. **MotionConfig 根组件**：App 外层包 `<MotionConfig reducedMotion="user" transition={{duration:.18,ease:'easeOut'}}>`——reduced-motion 用户自动禁 transform 保 opacity；全局默认时长 180ms
2. **视图切换**：WorkspaceRouter 条件渲染页（pool/admin/settings/documents 首挂）包 `<AnimatePresence mode="wait">` + `motion.div`（initial opacity:0/y:8 → animate → exit opacity:0）；`hidden` 常驻页（MessagingWorkspace/Documents）改 `data-active` 属性 + CSS `opacity/visibility transition`（visibility 可过渡，display 不可）
3. **列表项**：`motion.div` + `layout`（重排平滑）：会话列表、联系人列表、传输卡片、群成员行；`AnimatePresence` 包裹容器使删除项离场（opacity→0 + height 收缩 160ms）——好友删除、传输完成迁移立即平滑
4. **stagger 入场**：列表项 `variants` 依次 20ms 延迟（cap 8 项）
5. **对话框**：radix Dialog 加 CSS `data-[state=open]:animate-in` keyframes（scale .96→1 + fade）；自研 im-modal-shade 系（Profile/ReadersDialog/ImageLightbox）外层 `motion.div` opacity、内容 `motion.div` scale
6. **滑动删除**：好友行与会话行 `motion.div drag="x"` `dragConstraints={{left:-96,right:0}}` `dragElastic={.1}`，松手 `info.offset.x < -64` 触发删除确认（好友）或删除会话入口（若既有功能），否则弹回；行右缘 hover 常显删除按钮（`@media(hover:none)` 常显）
7. **微交互**：`.ln-button` 全局 `whileTap scale .97`（motion.css 用 CSS `:active` 实现等价，不逐个包组件）；卡片 `whileHover y:-2`（会话卡/联系人行/文档卡）；消息气泡入场 scale .92→1 +角标数字 AnimatePresence 翻换
8. **motion.css**：keyframes 定义 + `@media(hover:none)` 规则 + 与 tokens.css 的 `--ln-motion-fast` 对齐；CSS 层动画与 framer-motion 并存（CSS 管 hover/active，motion 管进出/拖拽）

**渐进接入**：基础设施 + 高频界面（会话列表/联系人/消息气泡/传输卡/全部对话框/视图切换）本批做完；管理页/审计页靠视图切换兜底。

**Win7 壳**：同一 React 前端自动生效（构建 target es2019 兼容；Electron Chromium 支持 framer-motion 全特性）。

**测试**：jsdom 下 framer-motion 跳过动画渲染（同步初始态）→ 现有 254 用例应全绿；新增行为用例：滑动阈值触发确认（pointer 事件模拟 drag）、AnimatePresence 离场后数据回调、restoring 态渲染；视觉验证靠 Playwright 截图（animations:'disabled' 不受影响）。

---

## 实施批次划分（并行）

| 批次 | 内容 | 文件域 |
|---|---|---|
| S3（服务端） | A 全部（Delete 事务+测试） | server/internal/social/、social_handlers_test.go、openapi（DELETE 描述更新）、user-guide |
| C3（现代客户端+动画） | A 客户端 + B 前端/Tauri 命令 + C 全部 | client/src、src-tauri（session/commands/feature）、styles、package.json |
| L3（Win7 壳） | B legacy 四件套 + C 验证 | client/legacy/ |

依赖：S3 独立；C3 的 A 部分依赖 S3 契约（DELETE 语义变更：403→404）可 mock 先行；L3 依赖 C3 的 session.rs 落盘实现（host dispatch 调同名方法）——L3 排最后或与 C3 尾部汇合。

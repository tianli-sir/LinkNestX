# Bug 批 1 实施计划（图标统一 / 群昵称 / 未读角标）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复用户报告的三个缺陷：三端（Tauri 桌面、网页、Win7 兼容包）图标统一为官网线条立方体标记；群聊内改"展示名"只影响本群（新增群昵称体系）；桌面端未读角标不再显示总数、看完消息能清零。

**Architecture:** 服务端（Go + SQLite）新增 `group_members.nickname` 可空列与"仅限本人"的 PATCH 接口，消息发送者 / 已读名单 / 成员名单 / 系统消息在群上下文统一解析生效名（私聊永不套用）；客户端（React）资料面板获得可选群上下文，已读上报管线移除 `document.hasFocus()` 依赖、增加重触发与毒丸剔除；图标以 `brand/mark.svg` 为唯一源重新生成 Tauri 图标、托盘、favicon 与 Win7 打包。

**Tech Stack:** Go 1.26（net/http ServeMux、SQLite、go:embed）、React 18 + vitest + @testing-library/react、Tauri 2（@tauri-apps/cli 2.12.4）、NSIS + rcedit（Win7 兼容包）。

---

## 背景与约定（执行任何任务前先读）

- 设计文档：`docs/superpowers/specs/2026-11-11-bugfix-batch-1-design.md`（已获用户确认）。
- **Go 测试**（仓库根目录运行，经 go.work 解析到 ./server）：全量 `go test ./server/...`；单个：`go test ./server/internal/http/ -run TestXxx -v`。
- **客户端测试**：`cd client && npx vitest run tests/<文件名>`；类型/构建检查：`cd client && npm run check`。
- **Rust 测试**（仅图标任务涉及）：`cargo test --manifest-path client/src-tauri/Cargo.toml`。
- 服务端 http 测试夹具：`newSocialFixture(t)`（`server/internal/http/social_handlers_test.go`）提供 `admin/alice/bob` 三个已登录会话（`f.admin/f.alice/f.bob`，各含 `.UserID`/`.AccessToken`）、数据库（`f.a.Database()`）与 `f.req(method, path, body, session)`、`expectCode(t, w, code)` 帮助函数。新建测试文件放在同一 `http` 包内即可复用。
- 客户端源码是紧凑单行风格（一个函数/组件常在一行内）。编辑用唯一锚点字符串做精确替换；改前先 Read 目标行确认。
- **名字解析约定（全篇一致）**：群内生效名 = `COALESCE(NULLIF(gm.nickname,''), NULLIF(p.display_name,''), u.username)`；仅群上下文生效；私聊（`direct_conversations` 有记录的 group）永不套用昵称。
- 提交信息用英文，遵循仓库现有风格（`feat:`/`fix:`/`test:`/`docs:`/`chore:`）。

## 文件总览

| 区域 | 新建 | 修改 |
|------|------|------|
| 群昵称-服务端 | `server/migrations/012_group_nicknames.sql`、`server/internal/http/group_nickname_test.go` | `groups/service.go`、`groups/repository.go`、`groups/system_messages.go`、`http/group_handlers.go`、`http/health.go`、`chat/receipts.go`、`chat/ws.go`、`db/migrations_test.go`、`api/openapi.yaml`、`docs/user-guide.md` |
| 群昵称-客户端 | `client/tests/v032-group-nickname.test.tsx` | `client/src/features/GroupsView.tsx`、`messaging/Profile.tsx`、`messaging/MessagingWorkspace.tsx`、`messaging/ConversationView.tsx`、`client/tests/v2-member-profile.test.tsx` |
| 未读角标 | （并入现有测试文件） | `client/src/api/rest.ts`、`api/browser.ts`、`features/messaging/readTracker.ts`、`messaging/ConversationView.tsx`、`runtime/browserAdapter.ts`、`runtime/DesktopProvider.tsx`、`client/tests/v2-runtime.test.tsx`、`v2-messaging.test.tsx`、`desktop-events.test.tsx` |
| 图标 | `brand/mark.svg`、`server/internal/http/favicon.go`、`client/public/favicon.svg` | `client/src-tauri/icons/*`（重生成）、`src/tray.rs`、`landing.html`、`releases` 下载页、`client/index.html`、`components/Icon.tsx`、`styles/desktop.css`、`scripts/package-legacy-windows.ps1`、`legacy/installer/legacy.nsi`、`legacy/shell/main.cjs`、`docs/development.md` |

三个部分相互独立、可分别回滚（设计文档承诺）。任务顺序：A 群昵称（Task 1–7）→ B 未读角标（Task 8–13）→ C 图标（Task 14–17）→ 收尾（Task 18）。

---

# Part A：群昵称（Bug #3）

## Task 1: 迁移 012 —— group_members.nickname 可空列

**Files:**
- Create: `server/migrations/012_group_nicknames.sql`
- Test: `server/internal/db/migrations_test.go`（追加）

- [ ] **Step 1: 写失败测试**

在 `server/internal/db/migrations_test.go` 末尾追加（该文件已有 `context`、`path/filepath`、`testing` 导入，无需新增）：

```go
func TestGroupNicknameColumnMigration(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "nickname.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM pragma_table_info('group_members') WHERE name='nickname' AND "notnull"=0`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("nullable nickname column missing: n=%d err=%v", n, err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./server/internal/db/ -run TestGroupNicknameColumnMigration -v`
Expected: FAIL（nickname 列不存在，n=0）

- [ ] **Step 3: 创建迁移文件**

`server/migrations/012_group_nicknames.sql`：

```sql
-- Group-scoped display names override the global profile name inside one group
-- only. NULL falls back to profiles.display_name; direct conversations never
-- read this column (enforced in application queries, not by the schema).
ALTER TABLE group_members ADD COLUMN nickname TEXT NULL;
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./server/internal/db/ -v`
Expected: PASS（含既有的打包/幂等测试——迁移由 embed 自动加载）

- [ ] **Step 5: 提交**

```bash
git add server/migrations/012_group_nicknames.sql server/internal/db/migrations_test.go
git commit -m "feat: add nullable group nickname column (migration 012)"
```

## Task 2: 成员名单携带昵称与生效名

**Files:**
- Create: `server/internal/http/group_nickname_test.go`
- Modify: `server/internal/groups/service.go`（Member 结构，第 33-37 行）
- Modify: `server/internal/groups/repository.go`（members() 查询，第 95-116 行）

- [ ] **Step 1: 写失败测试**

新建 `server/internal/http/group_nickname_test.go`：

```go
package http

import (
	"encoding/json"
	"testing"
)

// fixtureGroup 建一个含 admin(创建者)/alice/bob 的群，返回群 ID。
func fixtureGroup(t *testing.T, f *socialFixture) string {
	t.Helper()
	w := f.req("POST", "/api/v1/groups", `{"name":"项目群"}`, f.admin)
	expectCode(t, w, 201)
	var g struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&g); err != nil || g.ID == "" {
		t.Fatalf("group not created: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	for _, s := range []auth.Session{f.alice, f.bob} {
		w = f.req("POST", "/api/v1/groups/"+g.ID+"/members", `{"userId":"`+s.UserID+`","role":"member"}`, f.admin)
		expectCode(t, w, 204)
	}
	return g.ID
}

func TestGroupMemberListCarriesNicknameFields(t *testing.T) {
	f := newSocialFixture(t)
	f.a.Database().Exec(`UPDATE profiles SET display_name='爱丽丝' WHERE user_id=?`, f.alice.UserID)
	group := fixtureGroup(t, f)
	w := f.req("GET", "/api/v1/groups/"+group+"/members", "", f.alice)
	expectCode(t, w, 200)
	var members []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&members); err != nil {
		t.Fatal(err)
	}
	var mine map[string]any
	for _, m := range members {
		if m["userId"] == f.alice.UserID {
			mine = m
		}
	}
	if mine == nil {
		t.Fatalf("alice missing from member list: %v", members)
	}
	if mine["displayName"] != "爱丽丝" {
		t.Fatalf("effective displayName = %v, want 爱丽丝", mine["displayName"])
	}
	if v, ok := mine["nickname"].(string); ok {
		t.Fatalf("nickname = %v, want null/absent", v)
	}
	if num, _ := mine["accountNumber"].(string); num == "" {
		t.Fatalf("accountNumber missing: %v", mine)
	}
	if mine["hasAvatar"] != false {
		t.Fatalf("hasAvatar = %v, want false", mine["hasAvatar"])
	}
	if mine["disabled"] != false {
		t.Fatalf("disabled = %v, want false", mine["disabled"])
	}
}
```

注意：import 块需含 `"github.com/linknestx/linknestx/server/internal/auth"`（fixtureGroup 用 `auth.Session`）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./server/internal/http/ -run TestGroupMemberListCarriesNicknameFields -v`
Expected: FAIL（displayName 为 nil，缺少 accountNumber/hasAvatar/disabled）

- [ ] **Step 3: 扩展 Member 结构与查询**

`server/internal/groups/service.go` 第 33-37 行，把：

```go
type Member struct {
	UserID   string    `json:"userId"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}
```

替换为：

```go
type Member struct {
	UserID        string     `json:"userId"`
	Role          string     `json:"role"`
	JoinedAt      time.Time  `json:"joinedAt"`
	Nickname      *string    `json:"nickname"`
	DisplayName   string     `json:"displayName"`
	AccountNumber string     `json:"accountNumber"`
	HasAvatar     bool       `json:"hasAvatar"`
	Disabled      bool       `json:"disabled"`
}
```

`server/internal/groups/repository.go` 的 `members()`：把查询

```go
`SELECT user_id,role,joined_at FROM group_members WHERE group_id=? ORDER BY joined_at,user_id`
```

替换为：

```go
`SELECT gm.user_id,gm.role,gm.joined_at,gm.nickname,COALESCE(NULLIF(gm.nickname,''),NULLIF(p.display_name,''),u.username),p.account_number,p.avatar IS NOT NULL,u.disabled=1 FROM group_members gm JOIN users u ON u.id=gm.user_id JOIN profiles p ON p.user_id=gm.user_id WHERE gm.group_id=? ORDER BY gm.joined_at,gm.user_id`
```

把循环里的扫描

```go
		var m Member
		var at string
		if err := rows.Scan(&m.UserID, &m.Role, &at); err != nil {
			return nil, err
		}
```

替换为：

```go
		var m Member
		var at string
		var nickname, account sql.NullString
		if err := rows.Scan(&m.UserID, &m.Role, &at, &nickname, &m.DisplayName, &account, &m.HasAvatar, &m.Disabled); err != nil {
			return nil, err
		}
		if nickname.Valid {
			value := nickname.String
			m.Nickname = &value
		}
		m.AccountNumber = account.String
```

（`database/sql` 已在该文件导入。）

- [ ] **Step 4: 运行确认通过**

Run: `go test ./server/internal/http/ -run TestGroupMemberList -v && go test ./server/...`
Expected: PASS（既有成员名单测试若断言了精确字段数需按新字段补齐——只可能多字段，不会少）

- [ ] **Step 5: 提交**

```bash
git add server/internal/groups/service.go server/internal/groups/repository.go server/internal/http/group_nickname_test.go
git commit -m "feat: member list exposes nickname, effective name and account fields"
```

## Task 3: SetMemberNickname 服务 + PATCH 路由 + nickname.changed 事件

**Files:**
- Modify: `server/internal/groups/service.go`（GroupService 结构 + 新方法）
- Modify: `server/internal/http/group_handlers.go`（Register + SetMemberNickname handler）
- Modify: `server/internal/chat/ws.go`（Hub.NicknameChanged）
- Modify: `server/internal/http/health.go`（NewRouterWithServices 接线）
- Test: `server/internal/http/group_nickname_test.go`（追加）

- [ ] **Step 1: 写失败测试**

在 `group_nickname_test.go` 追加（import 增加 `"strings"`）：

```go
func TestSetMemberNicknameSelfOnlyAndValidated(t *testing.T) {
	f := newSocialFixture(t)
	group := fixtureGroup(t, f)
	self := "/api/v1/groups/" + group + "/members/" + f.alice.UserID

	var events [][2]string
	f.g.SetNicknameObserver(func(groupID, userID string) { events = append(events, [2]string{groupID, userID}) })

	// 本人可设置；观察者收到 (group, user)
	expectCode(t, f.req("PATCH", self, `{"nickname":"群里的我"}`, f.alice), 204)
	if len(events) != 1 || events[0] != [2]string{group, f.alice.UserID} {
		t.Fatalf("observer events = %v", events)
	}

	// 成员名单生效名变为昵称
	w := f.req("GET", "/api/v1/groups/"+group+"/members", "", f.alice)
	expectCode(t, w, 200)
	var members []struct {
		UserID      string  `json:"userId"`
		Nickname    *string `json:"nickname"`
		DisplayName string  `json:"displayName"`
	}
	if err := json.NewDecoder(w.Body).Decode(&members); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members {
		if m.UserID == f.alice.UserID {
			found = true
			if m.Nickname == nil || *m.Nickname != "群里的我" || m.DisplayName != "群里的我" {
				t.Fatalf("nickname resolution wrong: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("alice missing from member list")
	}

	// 空串清除昵称
	expectCode(t, f.req("PATCH", self, `{"nickname":""}`, f.alice), 204)
	w = f.req("GET", "/api/v1/groups/"+group+"/members", "", f.alice)
	expectCode(t, w, 200)
	members = nil
	if err := json.NewDecoder(w.Body).Decode(&members); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.UserID == f.alice.UserID && (m.Nickname != nil || m.DisplayName != "爱丽丝") {
			t.Fatalf("nickname not cleared: %+v", m)
		}
	}

	// 仅限本人：管理员和其他成员都不能替 alice 改
	expectCode(t, f.req("PATCH", self, `{"nickname":"越权"}`, f.admin), 403)
	expectCode(t, f.req("PATCH", self, `{"nickname":"越权"}`, f.bob), 403)
	// 校验：超长、控制字符
	expectCode(t, f.req("PATCH", self, `{"nickname":"`+strings.Repeat("长", 81)+`"}`, f.alice), 400)
	expectCode(t, f.req("PATCH", self, `{"nickname":"坏 字符"}`, f.alice), 400)
	// 群不存在
	expectCode(t, f.req("PATCH", "/api/v1/groups/00000000-0000-0000-0000-000000000000/members/"+f.alice.UserID, `{"nickname":"x"}`, f.alice), 404)
	// 非成员（无该群成员关系的账号）→ 403
	if _, e := f.a.CreateUser(context.Background(), auth.CreateUserInput{Username: "carol", Password: "password123"}); e != nil {
		t.Fatal(e)
	}
	f.a.Database().Exec(`UPDATE users SET must_change_password=0 WHERE username='carol'`)
	carol, e := f.a.Login(context.Background(), auth.LoginInput{Username: "carol", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	expectCode(t, f.req("PATCH", self, `{"nickname":"外人"}`, carol), 403)
}

func TestSetMemberNicknameRejectedForDirectConversation(t *testing.T) {
	f := newSocialFixture(t)
	w := f.req("POST", "/api/v1/conversations/direct", `{"userId":"`+f.bob.UserID+`"}`, f.alice)
	var c struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&c); err != nil || c.ID == "" {
		t.Fatalf("direct conversation not created: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	expectCode(t, f.req("PATCH", "/api/v1/groups/"+c.ID+"/members/"+f.alice.UserID, `{"nickname":"私聊不许"}`, f.alice), 403)
}
```

注意：第一个测试里"清除后 displayName=爱丽丝"依赖上一个测试全局名设置——同一测试内先执行 `f.a.Database().Exec(`UPDATE profiles SET display_name='爱丽丝' WHERE user_id=?`, f.alice.UserID)`（在 TestSetMemberNicknameSelfOnlyAndValidated 开头加一行，与 Task 2 测试相同写法）。import 增加 `"context"`。

（清除昵称断言补充说明：`m.DisplayName != "爱丽丝"` 仅对 alice 的行生效。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./server/internal/http/ -run TestSetMemberNickname -v`
Expected: FAIL（编译错误：SetNicknameObserver 未定义）

- [ ] **Step 3: 实现 service**

`server/internal/groups/service.go`：

(a) `GroupService` 结构（第 38-42 行）增加一个字段：

```go
type GroupService struct {
	repo            *Repository
	auth            *auth.AuthService
	observer        func(string, string)
	nicknameObserver func(string, string)
	systemObserver  func(context.Context, string)
}
```

(b) import 块增加 `"unicode"`（已有 strings、unicode/utf8）。

(c) 文件末尾（SetMemberObserver 旁）追加：

```go
// SetMemberNickname stores the actor's own group-scoped display name. An empty
// nickname clears the override so the global profile name applies again. Only
// the member themselves may change it; direct conversations never carry
// nicknames.
func (s *GroupService) SetMemberNickname(ctx context.Context, actorID, groupID, nickname string) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	var exists int
	if err := s.repo.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id=?)`, groupID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	if direct, err := IsDirect(ctx, s.repo.db, groupID); err != nil {
		return err
	} else if direct {
		return ErrForbidden
	}
	if err := s.RequireMember(ctx, actorID, groupID); err != nil {
		return err
	}
	nickname = strings.TrimSpace(nickname)
	if nickname != "" && (!utf8.ValidString(nickname) || utf8.RuneCountInString(nickname) > 80 || strings.IndexFunc(nickname, unicode.IsControl) >= 0) {
		return ErrInvalidInput
	}
	result, err := s.repo.db.ExecContext(ctx, `UPDATE group_members SET nickname=? WHERE group_id=? AND user_id=?`, sql.NullString{String: nickname, Valid: nickname != ""}, groupID, actorID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if s.nicknameObserver != nil {
		s.nicknameObserver(groupID, actorID)
	}
	return nil
}
func (s *GroupService) SetNicknameObserver(observer func(string, string)) { s.nicknameObserver = observer }
```

- [ ] **Step 4: 实现 handler 与路由**

`server/internal/http/group_handlers.go`：

(a) `Register` 中现有成员路由行（`DELETE /api/v1/groups/{groupID}/members/{userID}` 那条）之后增加同风格的 PATCH 注册（与现有行完全同构，handler 换名）：

```go
mux.Handle("PATCH /api/v1/groups/{groupID}/members/{userID}", RequireAuth(a, http.HandlerFunc(h.SetMemberNickname)))
```

（若 Register 使用的是路由表 map 风格，则添加键值对 `"PATCH /api/v1/groups/{groupID}/members/{userID}": h.SetMemberNickname`——以文件内现有写法为准，逐字模仿。）

(b) 追加 handler：

```go
func (h *GroupHandlers) SetMemberNickname(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("userID") != UserIDFromContext(r.Context()) {
		writeError(w, 403, groups.ErrForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in struct {
		Nickname string `json:"nickname"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := h.Service.SetMemberNickname(r.Context(), UserIDFromContext(r.Context()), r.PathValue("groupID"), in.Nickname); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(204)
}
```

- [ ] **Step 5: Hub 事件与接线**

`server/internal/chat/ws.go` 在 `MembershipChanged` 之后追加：

```go
// nickname.changed refreshes one member's group-scoped display name without
// the unrelated membership notices.
func (h *Hub) NicknameChanged(group, user string) {
	id := uuid.NewString()
	for _, p := range h.snapshot() {
		h.send(p, Event{Type: "nickname.changed", EventID: id, GroupID: group, UserID: user})
	}
}
```

`server/internal/http/health.go` `NewRouterWithServices` 中，在 `g.SetMemberObserver(hub.MembershipChanged)` 一行之后增加：

```go
	g.SetNicknameObserver(hub.NicknameChanged)
```

- [ ] **Step 6: 运行确认通过**

Run: `go test ./server/internal/http/ -run 'TestSetMemberNickname' -v && go test ./server/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add server/internal/groups/service.go server/internal/http/group_handlers.go server/internal/chat/ws.go server/internal/http/health.go server/internal/http/group_nickname_test.go
git commit -m "feat: self-service group nickname endpoint with nickname.changed event"
```

## Task 4: 聊天名字解析套用昵称（消息 / 已读名单 / 系统消息）

**Files:**
- Modify: `server/internal/chat/receipts.go`（project 拆分 + 预取 + Readers 列）
- Modify: `server/internal/groups/system_messages.go`（name() 群感知）
- Test: `server/internal/http/group_nickname_test.go`（追加）

- [ ] **Step 1: 写失败测试**

在 `group_nickname_test.go` 追加（import 增加 `"io"` 不需要；只需已有 json/strings/context）：

```go
func TestChatNamesResolveGroupNickname(t *testing.T) {
	f := newSocialFixture(t)
	f.a.Database().Exec(`UPDATE profiles SET display_name='爱丽丝' WHERE user_id=?`, f.alice.UserID)
	group := fixtureGroup(t, f)
	expectCode(t, f.req("PATCH", "/api/v1/groups/"+group+"/members/"+f.alice.UserID, `{"nickname":"小爱"}`, f.alice), 204)
	expectCode(t, f.req("PATCH", "/api/v1/groups/"+group+"/members/"+f.bob.UserID, `{"nickname":"老鲍"}`, f.bob), 204)

	// 1) 群消息发送者显示昵称
	w := f.req("POST", "/api/v1/conversations/"+group+"/messages", `{"body":"大家好","clientMessageId":"c-nick-1"}`, f.alice)
	var posted struct {
		ID     string `json:"id"`
		Sender struct {
			DisplayName string `json:"displayName"`
		} `json:"sender"`
	}
	if err := json.NewDecoder(w.Body).Decode(&posted); err != nil || posted.ID == "" {
		t.Fatalf("message not sent: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	if posted.Sender.DisplayName != "小爱" {
		t.Fatalf("posted sender name = %q, want 小爱", posted.Sender.DisplayName)
	}
	w = f.req("GET", "/api/v1/conversations/"+group+"/messages?limit=50", "", f.bob)
	var page struct {
		Messages []struct {
			ID     string `json:"id"`
			Sender struct {
				DisplayName string `json:"displayName"`
			} `json:"sender"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, m := range page.Messages {
		if m.ID == posted.ID {
			seen = true
			if m.Sender.DisplayName != "小爱" {
				t.Fatalf("listed sender name = %q, want 小爱", m.Sender.DisplayName)
			}
		}
	}
	if !seen {
		t.Fatal("posted message missing from page")
	}

	// 2) 已读名单显示昵称
	expectCode(t, f.req("POST", "/api/v1/conversations/"+group+"/read", `{"messageIds":["`+posted.ID+`"]}`, f.bob), 200)
	w = f.req("GET", "/api/v1/conversations/"+group+"/messages/"+posted.ID+"/readers", "", f.alice)
	var readers struct {
		Readers []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"readers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&readers); err != nil {
		t.Fatal(err)
	}
	foundReader := false
	for _, r := range readers.Readers {
		if r.ID == f.bob.UserID {
			foundReader = true
			if r.DisplayName != "老鲍" {
				t.Fatalf("reader name = %q, want 老鲍", r.DisplayName)
			}
		}
	}
	if !foundReader {
		t.Fatalf("bob missing from readers: %+v", readers)
	}

	// 3) 新系统消息正文用群内生效名（移出老鲍）
	expectCode(t, f.req("DELETE", "/api/v1/groups/"+group+"/members/"+f.bob.UserID, "", f.admin), 204)
	w = f.req("GET", "/api/v1/conversations/"+group+"/messages?limit=50", "", f.alice)
	var raw struct {
		Messages []struct {
			Kind string  `json:"kind"`
			Body *string `json:"body"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	sysNamed := false
	for _, m := range raw.Messages {
		if m.Kind == "system" && m.Body != nil && strings.Contains(*m.Body, "老鲍") {
			sysNamed = true
		}
	}
	if !sysNamed {
		t.Fatalf("system message does not use nickname: %+v", raw.Messages)
	}
}

func TestDirectConversationNamesIgnoreNicknames(t *testing.T) {
	f := newSocialFixture(t)
	f.a.Database().Exec(`UPDATE profiles SET display_name='全局爱丽丝' WHERE user_id=?`, f.alice.UserID)
	// 先在有昵称的群里设置昵称，证明私聊不受污染
	group := fixtureGroup(t, f)
	expectCode(t, f.req("PATCH", "/api/v1/groups/"+group+"/members/"+f.alice.UserID, `{"nickname":"小爱"}`, f.alice), 204)
	w := f.req("POST", "/api/v1/conversations/direct", `{"userId":"`+f.bob.UserID+`"}`, f.alice)
	var c struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&c); err != nil || c.ID == "" {
		t.Fatalf("direct conversation not created: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	w = f.req("POST", "/api/v1/conversations/"+c.ID+"/messages", `{"body":"私聊","clientMessageId":"c-nick-2"}`, f.alice)
	var posted struct {
		ID     string `json:"id"`
		Sender struct {
			DisplayName string `json:"displayName"`
		} `json:"sender"`
	}
	if err := json.NewDecoder(w.Body).Decode(&posted); err != nil || posted.ID == "" {
		t.Fatalf("message not sent: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	if posted.Sender.DisplayName != "全局爱丽丝" {
		t.Fatalf("direct sender name = %q, want 全局爱丽丝", posted.Sender.DisplayName)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./server/internal/http/ -run 'TestChatNamesResolveGroupNickname|TestDirectConversationNamesIgnoreNicknames' -v`
Expected: FAIL（发送者显示"爱丽丝"/"全局爱丽丝"以外的全局名逻辑未生效：第一项得到全局名而非昵称）

- [ ] **Step 3: 实现 receipts.go**

`server/internal/chat/receipts.go`：

(a) import 块增加 `"database/sql"` 与 `"errors"`。

(b) 把现有 `project`（第 12-25 行）：

```go
func (s *ChatService) project(ctx context.Context, actor string, m *Message) error {
	m.ConversationID = m.GroupID
	u, e := s.auth.User(ctx, m.SenderID)
	if e != nil {
		return e
	}
	m.Sender = &u
	if e = s.repo.db.QueryRowContext(ctx, `SELECT count(*)`+entitledReaders, m.ID).Scan(&m.ReadCount); e != nil {
		return e
	}
	return s.repo.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_reads WHERE message_id=? AND user_id=?)`, m.ID, actor).Scan(&m.ReadByMe)
}
```

替换为：

```go
func (s *ChatService) project(ctx context.Context, actor string, m *Message) error {
	return s.projectWith(ctx, actor, m, nil)
}

// projectWith resolves the sender and read state. nicknames is nil for
// single-message paths (one extra lookup per message) or a prefetched map for
// page projection (missing key = no nickname, no extra query).
func (s *ChatService) projectWith(ctx context.Context, actor string, m *Message, nicknames map[string]string) error {
	m.ConversationID = m.GroupID
	u, e := s.auth.User(ctx, m.SenderID)
	if e != nil {
		return e
	}
	m.Sender = &u
	if e = s.applyGroupNickname(ctx, m, nicknames); e != nil {
		return e
	}
	if e = s.repo.db.QueryRowContext(ctx, `SELECT count(*)`+entitledReaders, m.ID).Scan(&m.ReadCount); e != nil {
		return e
	}
	return s.repo.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_reads WHERE message_id=? AND user_id=?)`, m.ID, actor).Scan(&m.ReadByMe)
}

// applyGroupNickname overlays the sender's group nickname on the global profile
// name. Direct conversations never carry nicknames.
func (s *ChatService) applyGroupNickname(ctx context.Context, m *Message, nicknames map[string]string) error {
	if m.Sender == nil {
		return nil
	}
	nick, present := nicknames[m.SenderID]
	if !present && nicknames == nil {
		var value string
		e := s.repo.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(gm.nickname,''),'') FROM group_members gm WHERE gm.group_id=? AND gm.user_id=? AND NOT EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=gm.group_id)`, m.GroupID, m.SenderID).Scan(&value)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		nick = value
	}
	if nick != "" {
		m.Sender.DisplayName = nick
	}
	return nil
}

// prefetchNicknames loads a group's non-empty nicknames in one query so page
// projection avoids a per-message lookup.
func (s *ChatService) prefetchNicknames(ctx context.Context, groupID string) (map[string]string, error) {
	rows, e := s.repo.db.QueryContext(ctx, `SELECT gm.user_id,gm.nickname FROM group_members gm WHERE gm.group_id=? AND COALESCE(gm.nickname,'')<>'' AND NOT EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=gm.group_id)`, groupID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	nicknames := map[string]string{}
	for rows.Next() {
		var id, nickname string
		if e = rows.Scan(&id, &nickname); e != nil {
			return nil, e
		}
		nicknames[id] = nickname
	}
	return nicknames, rows.Err()
}
```

(c) `ConversationPage` 的投影循环（`sort.Slice(...)` 之后）把：

```go
	for i := range page.Messages {
		if e = s.project(ctx, actor, &page.Messages[i]); e != nil {
			return page, e
		}
	}
```

替换为：

```go
	nicknames, e := s.prefetchNicknames(ctx, id)
	if e != nil {
		return page, e
	}
	for i := range page.Messages {
		if e = s.projectWith(ctx, actor, &page.Messages[i], nicknames); e != nil {
			return page, e
		}
	}
```

(d) `Readers()` 的 SELECT 列表中把 `p.display_name` 替换为：

```sql
CASE WHEN EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=gm.group_id) THEN COALESCE(NULLIF(p.display_name,''),u.username) ELSE COALESCE(NULLIF(gm.nickname,''),NULLIF(p.display_name,''),u.username) END
```

（entitledReaders 常量已 JOIN `group_members gm`，无需改 FROM。）

- [ ] **Step 4: 实现 system_messages.go**

`server/internal/groups/system_messages.go` 把 `name` 闭包：

```go
	name := func(id string) (string, error) {
		var value string
		err := tx.QueryRowContext(ctx, "SELECT CASE WHEN p.display_name='' THEN u.username ELSE p.display_name END FROM users u JOIN profiles p ON p.user_id=u.id WHERE u.id=?", id).Scan(&value)
		return value, err
	}
```

替换为：

```go
	// Group-scoped: the effective name freezes into the system message body at
	// insert time; historical bodies are never rewritten.
	name := func(id string) (string, error) {
		var value string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(gm.nickname,''),NULLIF(p.display_name,''),u.username) FROM users u JOIN profiles p ON p.user_id=u.id LEFT JOIN group_members gm ON gm.group_id=? AND gm.user_id=u.id WHERE u.id=?`, group, id).Scan(&value)
		return value, err
	}
```

- [ ] **Step 5: 运行确认通过**

Run: `go test ./server/internal/http/ -run 'Nickname|NamesResolve' -v && go test ./server/...`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add server/internal/chat/receipts.go server/internal/groups/system_messages.go server/internal/http/group_nickname_test.go
git commit -m "feat: chat sender, readers and system messages resolve group nicknames"
```

## Task 5: openapi 与用户文档

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `docs/user-guide.md`

- [ ] **Step 1: openapi.yaml —— GroupMember schema 补字段**

在 `/components/schemas/GroupMember:`（约第 1435 行）的 `properties:` 下，紧跟 `joinedAt:` 之后增加：

```yaml
        nickname: { type: string, nullable: true, description: Group-scoped display name; null falls back to the profile name }
        displayName: { type: string, description: Effective name inside this group (nickname ?? profile display name ?? username) }
        accountNumber: { type: string }
        hasAvatar: { type: boolean }
        disabled: { type: boolean }
```

- [ ] **Step 2: openapi.yaml —— PATCH 操作**

在 `/api/v1/groups/{groupId}/members/{userId}:` 路径（约第 152 行）的 `delete:` 操作之后、下一个路径之前增加：

```yaml
    patch:
      operationId: setGroupMemberNickname
      summary: Set the caller's own group-scoped display name (self only)
      security: [{ bearerAuth: [] }]
      requestBody:
        required: true
        content:
          application/json: { schema: { $ref: '#/components/schemas/GroupNicknameRequest' } }
      responses:
        '204': { description: Nickname stored; an empty string clears the override }
        '400': { description: Nickname longer than 80 runes or contains control characters }
        '401': { description: Authentication required }
        '403': { description: Path userId is not the caller, the conversation is direct, or the caller is not a member }
        '404': { description: Group or membership does not exist }
```

并在 `/components/schemas/GroupMemberRequest:` 之后新增：

```yaml
    GroupNicknameRequest:
      type: object
      additionalProperties: false
      required: [nickname]
      properties:
        nickname: { type: string, maxLength: 80, description: Empty string clears the group nickname }
```

- [ ] **Step 3: 用户文档**

在 `docs/user-guide.md` 中找到群聊/成员管理相关小节（按标题定位），在其后插入：

```markdown
### 群昵称

在群聊里点自己的头像，可以设置一个**只在本群显示**的昵称：消息、已读名单、群成员列表和后续的系统提示都会使用它。留空保存即可恢复使用全局展示名。私聊会话和其他群不受影响；全局展示名仍在「我的资料」里修改。
```

- [ ] **Step 4: 提交**

```bash
git add api/openapi.yaml docs/user-guide.md
git commit -m "docs: document group nickname endpoint and behavior"
```

## Task 6: 客户端 Member 类型与成员列表行

**Files:**
- Modify: `client/src/features/GroupsView.tsx`（interface Member + MemberProfileRow + 调用处）
- Test: `client/tests/v2-member-profile.test.tsx`（更新既有断言）

- [ ] **Step 1: 更新既有测试（先改测试再改实现）**

打开 `client/tests/v2-member-profile.test.tsx`。其 setup 第 8 行的成员 mock：

```ts
let members=[{userId:peer.id,role:'member',joinedAt:'2026-11-10'}];
```

改为：

```ts
let members=[{userId:peer.id,role:'member',joinedAt:'2026-11-10',displayName:'周予',nickname:null,accountNumber:'10000002',hasAvatar:false,disabled:false}];
```

并把"等待成员资料加载"类断言（出现 `正在加载资料…`、`GET /api/v1/profiles/...` 的等待）改为直接断言成员行显示服务端生效名。具体地，凡是 `await waitFor(() => expect(h.request).toHaveBeenCalledWith('GET', `/api/v1/profiles/${peer.id}`, undefined))` 的用例：成员行不再发起每成员资料请求；改断言 `expect(h.request.mock.calls.some(([method, path]) => method === 'GET' && path === `/api/v1/profiles/${peer.id}`)).toBe(false)`，并以 `await screen.findByText('周予')` 验证行渲染。第二个用例（点开资料面板）保留 `fireEvent.click(await screen.findByRole('button',{name:'查看周予的资料'}))` 与 `await screen.findByRole('dialog',{name:'个人资料'})`，删除对 loadProfile Promise 的门控等待，改为面板打开后 `await waitFor(() => expect(h.request).toHaveBeenCalledWith('GET', `/api/v1/profiles/${peer.id}`, undefined))`（面板内部仍会拉取完整资料）。

- [ ] **Step 2: 运行确认失败**

Run: `cd client && npx vitest run tests/v2-member-profile.test.tsx`
Expected: FAIL（行内仍在等资料加载才显示名字）

- [ ] **Step 3: 实现**

`client/src/features/GroupsView.tsx`：

(a) `interface Member`（第 22-26 行）替换为：

```ts
export interface Member {
  userId: string;
  role: "owner" | "member";
  joinedAt: string;
  nickname?: string | null;
  displayName: string;
  accountNumber?: string;
  hasAvatar?: boolean;
  disabled?: boolean;
}
```

(b) `MemberProfileRow` 整个函数替换为（保持 `direct`/presence 等原有行为不变——若现有实现与本替换差异超出成员资料部分，仅替换资料/名称相关的行，其余逐字保留）：

```tsx
function MemberProfileRow({ member, group, me, client, online, canManage, onRemove }: {
  member: Member; group: Group; me: User; client: RestClient; online?: boolean; canManage: boolean; onRemove: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const disabled = !!member.disabled;
  const person: User = {
    id: member.userId,
    username: member.displayName,
    displayName: member.displayName,
    email: "",
    roles: ["member"],
    disabled,
    mustChangePassword: false,
    accountNumber: member.accountNumber,
    avatarUrl: member.hasAvatar ? `/api/v1/profiles/${member.userId}/avatar` : undefined,
  };
  async function direct() {
    try {
      const conversation = await client.request<{ id: string }>("POST", "/api/v1/conversations/direct", { userId: member.userId });
      setOpen(false);
      window.dispatchEvent(new CustomEvent("linknestx:open-conversation", { detail: { id: conversation.id } }));
    } catch (e) {
      setError(apiErrorMessage(e));
    }
  }
  return <li><div className="ln-member-identity">{disabled ? <Avatar name={member.displayName}/> : <Avatar profile={person} onClick={() => setOpen(true)}/>}<div><strong>{member.displayName}{disabled ? "（已停用）" : member.nickname ? "" : ""}</strong>{member.nickname ? <p className="ln-muted">群昵称 · 全局名 {member.displayName === member.nickname ? "同上" : displayName(person)}</p> : null}{!disabled && <p className="ln-muted">{member.accountNumber && /^\d+$/.test(member.accountNumber) ? `账号 ${member.accountNumber}` : "账号信息暂不可用"}</p>}<p className="ln-muted">{member.role === "owner" ? "群主" : "成员"}{online !== undefined ? ` · ${online ? "在线" : "离线"}` : ""}</p></div></div>{canManage && <button className="ln-text-button ln-danger" onClick={onRemove}>移除</button>}<ErrorNotice error={error}/>{!disabled && open && <ProfilePanel profile={person} me={me} client={client} group={{ id: group.id }} nickname={member.nickname ?? null} onClose={() => setOpen(false)} onDirect={direct} onChanged={() => {}}/>}</li>;
}
```

注意两个细节：
- 上面的"群昵称"行：当 `member.nickname` 为 null 时整行不渲染；nickname 存在但等于 displayName 时显示"同上"。若觉得冗余可简化为不渲染该行（保留 strong 中的生效名即可）——两种都可，实现时取简。
- `displayName(person)` 引用自 `../messaging/Profile`——确认 GroupsView 顶部已有该导入；没有则加 `import { displayName } from "./messaging/Profile";`（按现有导入风格）。

(c) 成员列表渲染处（`members.data?.map(member => <MemberProfileRow .../>)`）增加 `group={group}` 属性。

- [ ] **Step 4: 运行确认通过**

Run: `cd client && npx vitest run tests/v2-member-profile.test.tsx && npm test`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add client/src/features/GroupsView.tsx client/tests/v2-member-profile.test.tsx
git commit -m "feat: member rows render server-resolved group names without per-member fetches"
```

## Task 7: ProfilePanel 群上下文 + 事件刷新

**Files:**
- Modify: `client/src/features/messaging/Profile.tsx`（ProfilePanel）
- Modify: `client/src/features/messaging/MessagingWorkspace.tsx`（profileGroup 状态 + 传参 + 事件）
- Modify: `client/src/features/messaging/ConversationView.tsx`（server.event 列表加 nickname.changed）
- Modify: `client/src/features/GroupsView.tsx`（事件列表加 nickname.changed）
- Test: `client/tests/v032-group-nickname.test.tsx`（新建）

- [ ] **Step 1: 写失败测试**

新建 `client/tests/v032-group-nickname.test.tsx`（仿 v2-messaging.test.tsx 的 setup 模式）：

```tsx
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { App } from '../src/App';

const me = { id: 'u1', username: 'me', displayName: '我', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000001' };
const peer = { id: 'u2', username: 'peer', displayName: '周予', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000002' };
const groupConversation = { id: 'g1', kind: 'group', title: '项目群', memberCount: 2, unreadCount: 0, canSend: true };

function setup() {
  const listeners = new Map<string, (value: any) => void>();
  const patches: Array<{ path: string; body: any }> = [];
  const request = vi.fn(async (method: string, path: string, body?: any): Promise<any> => {
    if (path === '/api/v1/conversations') return [groupConversation];
    if (path === '/api/v1/groups/g1/members') return [
      { userId: 'u1', role: 'member', joinedAt: '2026-11-10', displayName: '我', nickname: null, accountNumber: '10000001', hasAvatar: false, disabled: false },
      { userId: 'u2', role: 'owner', joinedAt: '2026-11-10', displayName: '群主阿周', nickname: '阿周', accountNumber: '10000002', hasAvatar: false, disabled: false },
    ];
    if (path === '/api/v1/contacts') return { friends: [], incoming: [], outgoing: [] };
    if (path.startsWith('/api/v1/profiles/')) return peer;
    if (path.includes('/messages?')) return { messages: [], hasMore: false, nextCursor: '' };
    if (path.endsWith('/read')) return { messageIds: body.messageIds };
    if (method === 'PATCH') { patches.push({ path, body }); return {}; }
    throw new Error(path);
  });
  const invoke = vi.fn(async (name: string): Promise<any> => (name === 'list_transfers' ? [] : undefined));
  render(<App transport={{ request, login: async () => ({ id: 'session', user: me, deviceId: 'd', expiresAt: '' }), logout: async () => {} }} desktop={{ kind: 'native', invoke, listen: async (name, callback) => { listeners.set(name, callback); return () => listeners.delete(name); } }} />);
  return { request, patches, listeners };
}

test('群内点自己头像只编辑群昵称，不动全局名', async () => {
  const h = setup();
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  // 在会话内点自己头像：ConversationView 渲染 own message avatar —— 这里通过资料面板入口：消息为空时无头像可点，改为从成员管理面板进入（GroupWorkspace）验证相同组件行为
  fireEvent.click(await screen.findByRole('button', { name: '群组与成员管理' }));
  const row = await screen.findByText('我');
  fireEvent.click(row.closest('li')!.querySelector('button[aria-label^="查看"]')!);
  const dialog = await screen.findByRole('dialog', { name: '个人资料' });
  const input = dialog.querySelector('#group-nickname') as HTMLInputElement;
  expect(input).not.toBeNull();
  fireEvent.change(input, { target: { value: '群里的我' } });
  fireEvent.submit(dialog.querySelector('form')!);
  await waitFor(() => expect(h.patches.some(p => p.path === '/api/v1/groups/g1/members/u1' && JSON.stringify(p.body).includes('群里的我'))).toBe(true));
  expect(h.patches.some(p => p.path === '/api/v1/profile')).toBe(false);
});

test('群内他人资料显示本群昵称', async () => {
  const h = setup();
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  fireEvent.click(await screen.findByRole('button', { name: '群组与成员管理' }));
  fireEvent.click(await screen.findByRole('button', { name: '查看群主阿周的资料' }));
  expect(await screen.findByText(/本群昵称/)).toBeInTheDocument();
});

test('nickname.changed 事件刷新会话与成员', async () => {
  const h = setup();
  await screen.findByRole('button', { name: '项目群' });
  await act(async () => { h.listeners.get('server.event')?.({ type: 'nickname.changed', groupId: 'g1', userId: 'u2' }); });
  await waitFor(() => expect(h.request.mock.calls.some(([m, p]) => m === 'GET' && p === '/api/v1/conversations')).toBe(true));
  await waitFor(() => expect(h.request.mock.calls.some(([m, p]) => m === 'GET' && p === '/api/v1/groups/g1/members')).toBe(true));
});
```

（若 `App` 导出名或挂载需要的 props 与上述不同，以 `client/tests/v2-messaging.test.tsx` 的写法为准逐字对齐。）

- [ ] **Step 2: 运行确认失败**

Run: `cd client && npx vitest run tests/v032-group-nickname.test.tsx`
Expected: FAIL（无 #group-nickname 输入、无"本群昵称"文案）

- [ ] **Step 3: 实现 ProfilePanel 群上下文**

`client/src/features/messaging/Profile.tsx` 第 10 行（ProfilePanel 一整行）做以下修改（按锚点精确替换）：

(a) 组件签名：

```
export function ProfilePanel({profile,me,client,onClose,onDirect,onChanged}:{profile:ProfileType;me:ProfileType;client:RestClient;onClose:()=>void;onDirect:(id:string)=>Promise<void>;onChanged:()=>void}){
```

替换为：

```
export function ProfilePanel({profile,me,client,onClose,onDirect,onChanged,group,nickname}:{profile:ProfileType;me:ProfileType;client:RestClient;onClose:()=>void;onDirect:(id:string)=>Promise<void>;onChanged:()=>void;group?:{id:string};nickname?:string|null}){
```

(b) 在 `const [name,setName]=useState(person.displayName??person.username);` 之后增加：

```
const [nick,setNick]=useState(nickname??'');const ownGroup=group&&me.id===person.id;
```

(c) 标题行 `<h3>{displayName(person)}</h3>` 替换为：

```
<h3>{group&&nickname?nickname:displayName(person)}</h3>{group&&nickname&&<p className="ln-muted">全局展示名 {displayName(person)}</p>}
```

(d) 自己的编辑表单分支 `{own&&capabilities.profileEdit?<form onSubmit={e=>{e.preventDefault();void action(async()=>{const updated=await client.request<ProfileType>('PATCH','/api/v1/profile',{displayName:name.trim()});if(alive.current)updateUser(updated)})}}><label htmlFor="profile-display-name">展示名</label>...` —— 在它之前插入群昵称分支（保留原全局表单在非群场景使用）：

```
{ownGroup&&capabilities.profileEdit?<form onSubmit={e=>{e.preventDefault();void action(async()=>{await client.request('PATCH',`/api/v1/groups/${segment(group!.id)}/members/${encodeURIComponent(me.id)}`,{nickname:nick.trim()});onChanged()})}}><label htmlFor="group-nickname">群昵称</label><input id="group-nickname" value={nick} maxLength={80} onChange={e=>setNick(e.target.value)}/><button className="ln-button ln-button-primary" disabled={busy}>保存群昵称</button><p className="ln-muted">仅在本群显示；留空则使用全局展示名「{displayName(person)}」。</p></form>
```

并把原全局表单的开头条件 `{own&&capabilities.profileEdit?<form` 改为 `{own&&!group&&capabilities.profileEdit?<form`，其后对应的 `:own?` 分支同样改为 `:own&&!group?`。

(e) 他人分支（`friend?...` 按钮之前的渲染）增加群昵称展示：在 `{own&&...}` 三元链结束后、`</div></div>}` 前，对非本人且群上下文的情形展示 `本群昵称`：

```
{!own&&group?<p className="ln-muted">{nickname?`本群昵称 ${nickname}`:'本群未设置昵称'}</p>:null}
```

（若锚点定位困难：`!own` 的三段（发送消息/添加好友）保持不动，把上面这段插入在 `<ErrorNotice error={error||resource.error}/>` 之后即可。）

(f) 面板的 server.event 监听（`if(event.type==='profile.changed'){contacts.refresh();resource.refresh()}`）追加：

```
if(event.type==='nickname.changed'){resource.refresh();onChanged()}
```

- [ ] **Step 4: MessagingWorkspace 传群上下文**

`client/src/features/messaging/MessagingWorkspace.tsx`：

(a) 在 `const [profile,setProfile]=useState<Profile|null>(null);` 之后加：

```
const [profileGroup,setProfileGroup]=useState<{id:string}|null>(null);
```

(b) server.event 数组 `['message.created','read.changed','contacts.changed','profile.changed','conversation.changed','membership.changed','session.ready']` 增加 `'nickname.changed'`。

(c) 侧栏自身头像按钮 `onClick={()=>setProfile(me)}` 改为 `onClick={()=>{setProfile(me);setProfileGroup(null)}}`。

(d) `onProfile={setProfile}`（ConversationView 那处）改为：

```
onProfile={p=>{setProfile(p);setProfileGroup(current?.kind==='group'?{id:current.id}:null)}}
```

（ContactsView 的 `onProfile={setProfile}` 改为 `onProfile={p=>{setProfile(p);setProfileGroup(null)}}`。）

(e) 底部 ProfilePanel 渲染：

```
{profile&&active&&<ProfilePanel key={profile.id} profile={profile} me={me} client={client} onClose={()=>setProfile(null)} ...
```

在 `client={client}` 之后插入：

```
group={profileGroup??undefined} nickname={profileGroup&&members.data?.find(m=>m.userId===profile.id)?.nickname??null}
```

- [ ] **Step 5: ConversationView / GroupsView 监听 nickname.changed**

`client/src/features/messaging/ConversationView.tsx` server.event 监听数组的 `'membership.changed','conversation.changed'` 中追加 `'nickname.changed'`（在 `'membership.changed',` 后面插入 `'nickname.changed',`）。

`client/src/features/GroupsView.tsx` 第 40-44 行的事件处理 `["session.ready","membership.changed"]` 数组追加 `"nickname.changed"`。

- [ ] **Step 6: 运行确认通过**

Run: `cd client && npx vitest run tests/v032-group-nickname.test.tsx && npm test && npm run check`
Expected: PASS（类型检查通过）

- [ ] **Step 7: 提交**

```bash
git add client/src/features/messaging/Profile.tsx client/src/features/messaging/MessagingWorkspace.tsx client/src/features/messaging/ConversationView.tsx client/src/features/GroupsView.tsx client/tests/v032-group-nickname.test.tsx
git commit -m "feat: profile panel edits group nickname in group context"
```

---

# Part B：未读角标（Bug #8）

## Task 8: ApiError 携带 HTTP 状态码

**Files:**
- Modify: `client/src/api/rest.ts`
- Modify: `client/src/api/browser.ts`
- Test: `client/tests/v2-runtime.test.tsx`（追加）

- [ ] **Step 1: 写失败测试**

在 `client/tests/v2-runtime.test.tsx` 追加：

```tsx
test('rest errors expose HTTP status for both transports',async()=>{
 const failing=async()=>new Response(JSON.stringify({error:'bad'}),{status:400,headers:{'Content-Type':'application/json'}});
 const browser=createBrowserTransport(failing as typeof fetch);
 const rest=new (await import('../src/api/rest')).RestClient(browser);
 try{await rest.request('POST','/api/v1/conversations/c/read',{messageIds:['m1']});throw new Error('should reject')}catch(e){expect((e as any).status).toBe(400);expect((e as any).message).toBe('bad')}
 const native=(await import('../src/api/rest')).createNativeTransport(async()=>{throw {error:'server rejected',status:422}} as never);
 const rest2=new (await import('../src/api/rest')).RestClient(native);
 try{await rest2.request('POST','/api/v1/conversations/c/read',{});throw new Error('should reject')}catch(e){expect((e as any).status).toBe(422)}
});
```

（文件顶部已有 `createBrowserTransport` 导入；若没有则补 `import { createBrowserTransport } from '../src/api/browser';`。）

- [ ] **Step 2: 运行确认失败**

Run: `cd client && npx vitest run tests/v2-runtime.test.tsx -t 'status'`
Expected: FAIL（ApiError 无 status）

- [ ] **Step 3: 实现**

`client/src/api/rest.ts`：

(a) ApiError：

```ts
export class ApiError extends Error {
  constructor(message: string, public readonly status?: number) {
    super(message);
    this.name = "ApiError";
  }
}
```

(b) `RestClient.request` 的 catch：

```ts
    } catch (error) {
      throw new ApiError(apiErrorMessage(error));
    }
```

替换为：

```ts
    } catch (error) {
      throw new ApiError(apiErrorMessage(error), (error as { status?: number })?.status ?? undefined);
    }
```

`client/src/api/browser.ts` 的 `if(!response.ok){...throw new ApiError(message);}` 改为 `throw new ApiError(message, response.status);`。

（桌面端原生链路：Tauri 命令错误经 serde 序列化携带 `status` 字段——`client/src-tauri/src/lib.rs` 的 `TransferError::http(status)` 已产出 `{error,status}`，无需改 Rust。）

- [ ] **Step 4: 运行确认通过**

Run: `cd client && npm test`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add client/src/api/rest.ts client/src/api/browser.ts client/tests/v2-runtime.test.tsx
git commit -m "feat: api errors carry HTTP status through both transports"
```

## Task 9: readTracker 重试策略（慢重试 + 毒丸剔除）

**Files:**
- Modify: `client/src/features/messaging/readTracker.ts`（整文件重写）
- Test: `client/tests/v2-runtime.test.tsx`（追加）

- [ ] **Step 1: 写失败测试**

在 `client/tests/v2-runtime.test.tsx` 追加：

```tsx
test('read tracker keeps a slow 30s retry after fast attempts',async()=>{
 vi.useFakeTimers();const send=vi.fn().mockRejectedValue(new Error('offline'));
 const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);tracker.visible('m1',true);
 await vi.advanceTimersByTimeAsync(401+1000+2000+4000);
 expect(send).toHaveBeenCalledTimes(3);
 await vi.advanceTimersByTimeAsync(29000);expect(send).toHaveBeenCalledTimes(3);
 await vi.advanceTimersByTimeAsync(1000);expect(send).toHaveBeenCalledTimes(4);
 tracker.dispose();vi.useRealTimers();
});
test('read tracker sheds poison ids on repeated 400 without losing the rest',async()=>{
 vi.useFakeTimers();
 const send=vi.fn(async(ids:string[])=>{if(ids.includes('bad'))throw{status:400};return ids});
 const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);
 tracker.visible('bad',true);tracker.visible('ok1',true);tracker.visible('ok2',true);
 await vi.advanceTimersByTimeAsync(401);
 const flat=()=>send.mock.calls.flatMap(([ids])=>ids as string[]);
 expect(flat()).toContain('ok1');expect(flat()).toContain('ok2');
 expect(flat().filter(id=>id==='bad').length).toBeGreaterThan(0); // 至少尝试过整批
 await vi.advanceTimersByTimeAsync(60000);
 const callsWithOnlyOk=send.mock.calls.filter(([ids])=>(ids as string[]).every(id=>id!=='bad'));
 expect(callsWithOnlyOk.length).toBeGreaterThan(0);
 expect(flat().filter(id=>id==='bad').length).toBeLessThan(10); // 没有无限重试
 tracker.dispose();vi.useRealTimers();
});
test('read tracker resets attempts when an id becomes visible again',async()=>{
 vi.useFakeTimers();let failures=0;
 const send=vi.fn(async(ids:string[])=>{if(ids.includes('m1')&&failures<1){failures++;throw new Error('offline')}return ids});
 const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);tracker.visible('m1',true);
 await vi.advanceTimersByTimeAsync(401);
 tracker.visible('m1',false);tracker.visible('m1',true);
 await vi.advanceTimersByTimeAsync(401);
 expect(failures).toBe(0);tracker.dispose();vi.useRealTimers();
});
```

- [ ] **Step 2: 运行确认失败**

Run: `cd client && npx vitest run tests/v2-runtime.test.tsx -t 'read tracker'`
Expected: 新增用例 FAIL（慢重试/剔除/复位不存在）；既有 read tracker 用例仍 PASS

- [ ] **Step 3: 重写 readTracker.ts**

`client/src/features/messaging/readTracker.ts` 整文件替换为：

```ts
/** Tracks individual visible message IDs, never a sequence frontier. */
export class VisibleReadTracker {
 private enabled=false;private disposed=false;private visibleIds=new Set<string>();private confirmed=new Set<string>();private shedIds=new Set<string>();private pending=new Set<string>();private attempts=new Map<string,number>();private inFlight=new Set<string>();private timers=new Map<string,ReturnType<typeof setTimeout>>();private flushTimer?:ReturnType<typeof setTimeout>;
 constructor(private send:(ids:string[])=>Promise<string[]>){ }
 setEnabled(enabled:boolean){if(this.disposed)return;if(this.enabled===enabled)return;this.enabled=enabled;this.timers.forEach(clearTimeout);this.timers.clear();this.pending.clear();if(enabled){this.attempts.clear();this.visibleIds.forEach(id=>this.schedule(id));}}
 visible(id:string,visible:boolean){if(this.disposed)return;if(visible){if(this.shedIds.has(id))return;const wasVisible=this.visibleIds.has(id);this.visibleIds.add(id);if(!wasVisible)this.attempts.delete(id);this.schedule(id);}else{this.visibleIds.delete(id);const timer=this.timers.get(id);if(timer)clearTimeout(timer);this.timers.delete(id);this.pending.delete(id);}}
 private schedule(id:string){if(!this.enabled||this.confirmed.has(id)||this.timers.has(id)||this.pending.has(id)||this.inFlight.has(id))return;this.timers.set(id,setTimeout(()=>{this.timers.delete(id);if(!this.enabled||!this.visibleIds.has(id))return;this.pending.add(id);if(!this.flushTimer)this.flushTimer=setTimeout(()=>{this.flushTimer=undefined;void this.flush();},0);},400));}
 private isBadRequest(error:unknown){return !!error&&typeof error==='object'&&(error as {status?:unknown}).status===400}
 private async flush(){if(this.disposed||!this.enabled)return;const ids=[...this.pending];this.pending.clear();for(let i=0;i<ids.length;i+=100){if(this.disposed||!this.enabled)return;const batch=ids.slice(i,i+100).filter(id=>this.visibleIds.has(id));if(!batch.length)continue;await this.deliver(batch);}}
 private async deliver(batch:string[]){batch.forEach(id=>this.inFlight.add(id));try{const confirmed=await this.send(batch);if(!this.disposed)confirmed.forEach(id=>this.confirmed.add(id));}catch(error){if(this.disposed)return;if(this.isBadRequest(error)){
  // 服务端整批校验：先原样重试一次排除瞬态 400，再二分定位并剔除毒丸 ID。
  try{const confirmed=await this.send(batch);if(!this.disposed){confirmed.forEach(id=>this.confirmed.add(id));return}}catch{ /* 继续隔离 */ }
  await this.isolate(batch);return;
 }if(this.enabled)this.scheduleRetries(batch);}finally{batch.forEach(id=>this.inFlight.delete(id));}}
 private async isolate(batch:string[]){if(batch.length===1){const id=batch[0];this.visibleIds.delete(id);this.pending.delete(id);this.timers.delete(id);this.attempts.delete(id);this.shedIds.add(id);return}const half=Math.ceil(batch.length/2);await this.deliver(batch.slice(0,half));await this.deliver(batch.slice(half));}
 private scheduleRetries(batch:string[]){for(const id of batch){const attempts=(this.attempts.get(id)??0)+1;this.attempts.set(id,attempts);if(this.visibleIds.has(id)){const delay=attempts<=3?Math.min(4000,1000*2**(attempts-1)):30000;this.timers.set(id,setTimeout(()=>{this.timers.delete(id);this.schedule(id)},delay));}}}
 dispose(){this.disposed=true;this.timers.forEach(clearTimeout);if(this.flushTimer)clearTimeout(this.flushTimer);this.timers.clear();this.pending.clear();this.visibleIds.clear();}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `cd client && npx vitest run tests/v2-runtime.test.tsx`
Expected: PASS（含既有 4 个 read tracker 用例——`'read acknowledgements retry while still visible...'` 仍应通过：禁用后不再重试）

- [ ] **Step 5: 提交**

```bash
git add client/src/features/messaging/readTracker.ts client/tests/v2-runtime.test.tsx
git commit -m "fix: read tracker keeps slow retries and sheds poison ids on 400"
```

## Task 10: ConversationView 门控去 hasFocus + 重查触发

**Files:**
- Modify: `client/src/features/messaging/ConversationView.tsx`
- Test: `client/tests/v2-messaging.test.tsx`（追加）

- [ ] **Step 1: 写失败测试**

在 `client/tests/v2-messaging.test.tsx` 追加（沿用文件既有的 `setup`/`observe` 模式——`observe` 为该文件里向 IntersectionObserver 实例派发条目的既有帮助函数；若无导出则仿第 32-35 行用例中的 `nodes`/`observe` 写法内联构造）：

```tsx
test('reads proceed when native foreground is true even if the webview document reports no focus',async()=>{
 const h=setup({messages:[message('m1')]});
 await login();  // 与文件内其他用例一致的登录辅助（若有不同写法，按文件内既有用例对齐）
 fireEvent.click(screen.getByRole('button',{name:'周予'}));
 await screen.findByText('消息 m1');
 const focus=vi.spyOn(document,'hasFocus').mockReturnValue(false);
 vi.stubGlobal('IntersectionObserver',class{observe=vi.fn();unobserve=vi.fn();disconnect=vi.fn();takeRecords=()=>[];root=null;rootMargin='';thresholds=[];});
 await act(async()=>h.listeners.get('window.foreground')?.(true));
 // 重新挂载消息节点并派发可见
 vi.useFakeTimers();
 const target=document.querySelector('[data-read-id="m1"]');
 const observeCb=(IntersectionObserver as any).prototype?.callback;
 expect(target).not.toBeNull();
 await act(async()=>{await vi.advanceTimersByTimeAsync(1000)});
 expect(h.request.mock.calls.some(([method,path])=>method==='POST'&&path.endsWith('/read'))).toBe(true);
 focus.mockRestore();vi.useRealTimers();vi.unstubAllGlobals();
});
```

（此测试的精确构造以文件内既有用例 `native foreground state prevents receipts even if the webview document reports focus`（第 32-35 行）为模板，把它断言取反：foreground=true、hasFocus=false → read 请求发出。实现者应复制该用例的 setup 细节，只改两处：`h.listeners.get('window.foreground')?.(true)` 与最终 `expect(...).toBe(true)`。）

```tsx
test('pointerdown on the conversation re-evaluates the read gate after a missed visibility change',async()=>{
 const h=setup({messages:[message('m1')]});
 await login();
 fireEvent.click(screen.getByRole('button',{name:'周予'}));
 await screen.findByText('消息 m1');
 const describe=Object.getOwnPropertyDescriptor(document,'visibilityState');
 Object.defineProperty(document,'visibilityState',{get:()=>'hidden',configurable:true});
 await act(async()=>h.listeners.get('window.foreground')?.(true));  // 门控此刻因 hidden 关闭
 Object.defineProperty(document,'visibilityState',{get:()=>'visible',configurable:true});  // 模拟 missed event
 // ……以既有用例方式派发 IntersectionObserver 可见 + 推进计时器后断言无 read……
 fireEvent.pointerDown(screen.getByText('消息 m1'));
 await act(async()=>{await vi.advanceTimersByTimeAsync(1000)});
 expect(h.request.mock.calls.some(([method,path])=>method==='POST'&&path.endsWith('/read'))).toBe(true);
 describe&&Object.defineProperty(document,'visibilityState',describe);
 vi.useRealTimers();
});
```

（实现时注意：两个用例中的 `login`/`observe`/`message` 帮助函数均为该测试文件既有的局部写法——逐字复用第 32-35 行用例的上下文构造，不要新造。）

- [ ] **Step 2: 运行确认失败**

Run: `cd client && npx vitest run tests/v2-messaging.test.tsx -t 'reads proceed'`
Expected: FAIL（hasFocus=false 阻断上报）

- [ ] **Step 3: 实现**

`client/src/features/messaging/ConversationView.tsx`：

(a) announce 效果里把 `document.visibilityState==='visible'&&document.hasFocus()?conversation.id:null` 替换为 `document.visibilityState==='visible'?conversation.id:null`。

(b) 门控效果里把 `const gate=()=>tracker.setEnabled(active&&foreground&&!obscured&&!readers&&document.visibilityState==='visible'&&document.hasFocus());` 替换为：

```
const gate=()=>tracker.setEnabled(active&&foreground&&!obscured&&!readers&&document.visibilityState==='visible');
```

(c) 在 `document.addEventListener('visibilitychange',gate);` 之后增加：

```
const recheck=()=>gate();root.current?.addEventListener('pointerdown',recheck,{capture:true});root.current?.addEventListener('keydown',recheck,{capture:true});
```

(d) 该效果的**两个**清理函数（无 IntersectionObserver 提前返回的清理与完整清理）都在 `document.removeEventListener('visibilitychange',gate);` 之后补：

```
root.current?.removeEventListener('pointerdown',recheck,{capture:true});root.current?.removeEventListener('keydown',recheck,{capture:true});
```

(e) 依赖数组 `[messages,active,foreground,obscured,readers,readRetry]` 追加 `connection.status`（`connection` 已从 useDesktop 解构）。

- [ ] **Step 4: 运行确认通过**

Run: `cd client && npx vitest run tests/v2-messaging.test.tsx`
Expected: PASS（含既有 `native foreground state prevents receipts...` 用例——foreground=false 仍阻断）

- [ ] **Step 5: 提交**

```bash
git add client/src/features/messaging/ConversationView.tsx client/tests/v2-messaging.test.tsx
git commit -m "fix: drop hasFocus from read gate and recheck on interaction"
```

## Task 11: 浏览器适配器真实 foreground 事件

**Files:**
- Modify: `client/src/runtime/browserAdapter.ts`
- Test: `client/tests/v2-runtime.test.tsx`（追加）

- [ ] **Step 1: 写失败测试**

```tsx
test('browser adapter reports real window foreground state',async()=>{
 const {createBrowserAdapter}=await import('../src/runtime/browserAdapter');
 vi.stubGlobal('WebSocket',class{close(){}});
 const adapter=createBrowserAdapter();
 const states:boolean[]=[];
 const remove=await adapter.listen('window.foreground',payload=>states.push(payload));
 window.dispatchEvent(new Event('blur'));
 window.dispatchEvent(new Event('focus'));
 document.dispatchEvent(new Event('visibilitychange'));
 expect(states.length).toBeGreaterThan(1);
 expect(states[states.length-1]).toBe(true);
 remove();
 window.dispatchEvent(new Event('blur'));
 vi.unstubAllGlobals();
});
```

- [ ] **Step 2: 运行确认失败**

Run: `cd client && npx vitest run tests/v2-runtime.test.tsx -t 'browser adapter reports'`
Expected: FAIL（browserAdapter 不处理 window.foreground，states 为空）

- [ ] **Step 3: 实现**

`client/src/runtime/browserAdapter.ts` 的 `listen` 函数开头（`const emit = ...` 定义之后的 listen 实现内）加入对 `window.foreground` 的特判——在现有事件分发逻辑之前插入：

```ts
    if (name === 'window.foreground') {
      const report = () => callback(document.visibilityState === 'visible' && document.hasFocus());
      report();
      window.addEventListener('focus', report);
      window.addEventListener('blur', report);
      document.addEventListener('visibilitychange', report);
      return async () => {
        window.removeEventListener('focus', report);
        window.removeEventListener('blur', report);
        document.removeEventListener('visibilitychange', report);
      };
    }
```

（插入点以该文件 `listen` 的实际结构为准：找到分发事件的入口分支，把上面整块放在最前。）

- [ ] **Step 4: 运行确认通过**

Run: `cd client && npx vitest run tests/v2-runtime.test.tsx && npm test`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add client/src/runtime/browserAdapter.ts client/tests/v2-runtime.test.tsx
git commit -m "feat: browser adapter emits real window foreground events"
```

## Task 12: native 适配器 onResized 失败降级

**Files:**
- Modify: `client/src/runtime/DesktopProvider.tsx`
- Test: `client/tests/desktop-events.test.tsx`（追加）

- [ ] **Step 1: 写失败测试**

在 `client/tests/desktop-events.test.tsx` 追加（mock `@tauri-apps/api/window` 使 `onResized` 拒绝）：

```tsx
test('window.foreground survives a failing onResized subscription',async()=>{
 const calls:{focus:(e:{payload:boolean})=>void;resize:()=>void}={focus:()=>{},resize:()=>{}};
 vi.doMock('@tauri-apps/api/window',()=>({getCurrentWindow:()=>({
  isFocused:async()=>true,isMinimized:async()=>false,
  onFocusChanged:async(cb:(e:{payload:boolean})=>void)=>{calls.focus=cb;return()=>{}},
  onResized:async()=>{throw new Error('unsupported')},
 })}));
 const {DesktopProvider,useDesktop}=await import('../src/runtime/DesktopProvider');
 let foreground=true;
 const Probe=()=>{foreground=useDesktop().foreground;return null};
 const view=render(<DesktopProvider><Probe/></DesktopProvider>);
 await waitFor(()=>expect(foreground).toBe(true));
 view.unmount();vi.doUnmock('@tauri-apps/api/window');
});
```

（若该文件的既有用例通过 `vi.mock` 模块级 mock，保持其风格改写；关键断言：onResized 拒绝后 `foreground` 仍能变为 true 且不抛错。）

- [ ] **Step 2: 运行确认失败**

Run: `cd client && npx vitest run tests/desktop-events.test.tsx -t 'onResized'`
Expected: FAIL（当前实现 unfocus 并 throw，foreground 卡 false）

- [ ] **Step 3: 实现**

`client/src/runtime/DesktopProvider.tsx` 的 nativeDesktop 中把：

```ts
      let unresize:()=>void;try{unresize=await current.onResized(()=>void refresh())}catch(error){unfocus();throw error}
```

替换为：

```ts
      // onResized only accelerates refresh on minimize/restore; focus events
      // remain the primary signal, so a failing subscription degrades quietly.
      let unresize:()=>void=()=>{};
      try{unresize=await current.onResized(()=>void refresh())}catch{/* optional signal */}
```

- [ ] **Step 4: 运行确认通过**

Run: `cd client && npx vitest run tests/desktop-events.test.tsx && npm test`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add client/src/runtime/DesktopProvider.tsx client/tests/desktop-events.test.tsx
git commit -m "fix: foreground listener degrades when onResized is unavailable"
```

## Task 13: 端到端角标清零回归

**Files:**
- Test: `client/tests/v2-messaging.test.tsx`（追加）

- [ ] **Step 1: 写测试（此后即应通过——若不通过说明 Part B 前序任务有缺漏）**

```tsx
test('unread badge clears after visible messages are acknowledged',async()=>{
 let unread=1;
 const h=setup({messages:[message('m1')]});
 // 让会话列表第一次返回 unreadCount=1，read 成功后返回 0
 // setup 的 request mock 对 '/api/v1/conversations' 返回固定数组；实现时把该返回值改为
 // 通过闭包变量 unread 控制：unreadCount: unread —— 与文件内 setup 的 conversation 对象合成。
 await login();
 expect(await screen.findByText('1')).toBeInTheDocument();
 fireEvent.click(screen.getByRole('button',{name:'周予'}));
 await screen.findByText('消息 m1');
 unread=0;
 await act(async()=>h.listeners.get('server.event')?.({type:'conversation.changed',id:conversation.id}));
 await waitFor(()=>expect(screen.queryByText('1')).not.toBeInTheDocument());
});
```

（实现时需要小幅扩展该文件 setup：`conversation` 的 `unreadCount` 由闭包 `unread` 变量驱动，并在 read 成功回调里置 0——具体写法仿照该文件对 friends 状态的闭包处理。）

- [ ] **Step 2: 运行**

Run: `cd client && npx vitest run tests/v2-messaging.test.tsx -t 'unread badge'`
Expected: PASS

- [ ] **Step 3: 提交**

```bash
git add client/tests/v2-messaging.test.tsx
git commit -m "test: unread badge clears after acknowledged reads"
```

- [ ] **Step 4: Windows 桌面端手动验收（不阻塞提交，记录结果）**

```text
1. npm --prefix client run desktop:dev 启动桌面端并登录。
2. 制造另一账号发来的 3 条消息，确认侧栏角标=3。
3. 不点击页面任何位置（模拟焦点 desync），仅滚动读完会话 → 返回列表，角标应为 0。
4. 切到别的窗口再切回、最小化后恢复 → 角标数字准确为未读数。
5. 断网发消息（发送失败重试场景）→ 恢复网络 → 已读最终上报。
```

---

# Part C：图标统一（Bug #1）

## Task 14: brand/mark.svg 唯一源 + Tauri 图标重生成 + 托盘

**Files:**
- Create: `brand/mark.svg`
- Modify: `client/src-tauri/icons/*`（重生成，二进制）
- Modify: `client/src-tauri/src/tray.rs`
- Modify: `docs/development.md`

- [ ] **Step 1: 创建唯一源**

`brand/mark.svg`（官网线条立方体，纸白圆角方底 + 绿描边；圆角绘制在源图内，Windows/macOS 呈现一致；描边 2.4 保证小尺寸可辨）：

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
  <rect width="512" height="512" rx="112" fill="#f7f9f8"/>
  <g fill="none" stroke="#17644e" stroke-width="26" stroke-linecap="round" stroke-linejoin="round">
    <path d="M128 160 256 88l128 72v208l-128 72-128-72Z"/>
    <path d="m128 160 128 80 128-80M256 240v200M128 240l128 80 128-80"/>
  </g>
  <circle cx="256" cy="470" r="18" fill="#17644e"/>
</svg>
```

（几何与 `server/internal/http/landing.html` 第 373 行 `<symbol id="mark" viewBox="0 0 32 36">` 一致，按 16 倍放大；底部圆点对齐 landing 的 cy=33/36 比例。）

- [ ] **Step 2: 重新生成 Tauri 图标**

Run:
```bash
cd client
npx tauri icon ../brand/mark.svg -o src-tauri/icons
git status --short src-tauri/icons
```
Expected: 生成 `icon.png`、`icon.ico`、`icon.icns` 及各尺寸 PNG。保留 `tauri.conf.json` 现有 `bundle.icon` 数组（icon.png/icon.ico/icon.icns 三项路径不变）；其余新生成 PNG 一并提交（不引用也不碍事，留给商店打包）。

注意：若 `tauri icon` 报 SVG 不支持（CLI 版本差异），改用任意本地工具把 SVG 渲染为 ≥1024px PNG（如 `npx sharp-cli` 或 Chrome 截图）后重跑 `npx tauri icon <png> -o src-tauri/icons`。**禁止手改二进制。**

- [ ] **Step 3: 重写托盘图标**

`client/src-tauri/src/tray.rs` 把程序化像素块（`let mut pixels = vec![0u8; 32 * 32 * 4];` 直到 `.icon(tauri::image::Image::new_owned(pixels, 32, 32))` 的整段）替换为使用打包图标：

```rust
    let mut builder = TrayIconBuilder::with_id("linknestx")
        .tooltip("LinkNestX · 后台传输服务")
        .menu(&menu)
        .show_menu_on_left_click(false);
    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    }
    builder
        .on_menu_event(|app, event| match event.id.as_ref() {
            "open" => show(app),
            "quit" => quit(app),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if matches!(
                event,
                TrayIconEvent::Click {
                    button: MouseButton::Left,
                    button_state: MouseButtonState::Up,
                    ..
                }
            ) {
                show(tray.app_handle())
            }
        })
        .build(app)?;
    Ok(())
```

（`default_window_icon()` 返回打包窗口图标，托盘自动与 exe 一致；保留函数内其余 open/quit 逻辑不动。）

- [ ] **Step 4: 构建与测试验证**

Run:
```bash
cargo check --manifest-path client/src-tauri/Cargo.toml
cargo test --manifest-path client/src-tauri/Cargo.toml
```
Expected: 编译通过、测试 PASS

- [ ] **Step 5: 文档**

`docs/development.md` 在构建/资源相关小节追加：

```markdown
### 应用图标再生成

图标唯一源是 `brand/mark.svg`（官网线条立方体）。改源后运行：

    cd client && npx tauri icon ../brand/mark.svg -o src-tauri/icons

提交生成的全部图标文件；托盘图标随 `default_window_icon()` 自动一致，无需单独维护。
```

- [ ] **Step 6: 提交**

```bash
git add brand/mark.svg client/src-tauri/icons client/src-tauri/src/tray.rs docs/development.md
git commit -m "feat: unify app icons on the website line-cube mark"
```

## Task 15: 服务端 favicon 路由与页面链接

**Files:**
- Create: `server/internal/http/favicon.svg`
- Create: `server/internal/http/favicon.ico`（从 Task 14 产物复制）
- Create: `server/internal/http/favicon.go`
- Modify: `server/internal/http/health.go`（两处路由注册）
- Modify: `server/internal/http/landing.html`、下载页 HTML（`releases` 包内页头）
- Test: `server/internal/http/health_test.go`（追加）

- [ ] **Step 1: 写失败测试**

在 `server/internal/http/health_test.go` 追加（该文件已有 httptest 用法，按其风格）：

```go
func TestFaviconRoutes(t *testing.T) {
	handler := NewHandler(testConfig(t)) // 以文件内既有的配置构造写法为准
	for path, contentType := range map[string]string{
		"/favicon.svg": "image/svg+xml",
		"/favicon.ico": "image/x-icon",
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s status = %d", path, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != contentType {
			t.Fatalf("%s content-type = %q", path, got)
		}
		if w.Body.Len() == 0 {
			t.Fatalf("%s empty body", path)
		}
	}
}
```

（`testConfig(t)` 为占位写法——以 health_test.go 文件内既有构造 config 的语句逐字替换，例如 `config.Default()` 或该文件实际使用的构造。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./server/internal/http/ -run TestFaviconRoutes -v`
Expected: FAIL（404）

- [ ] **Step 3: 实现资源与路由**

(a) `server/internal/http/favicon.svg`（与 brand 同源，无底色版用于浏览器页签）：

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 36">
  <g fill="none" stroke="#17644e" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round">
    <path d="M5 8 16 2l11 6v13l-11 7L5 21Z"/>
    <path d="m5 8 11 7 11-7M16 15v13M5 15l11 7 11-7"/>
  </g>
  <circle cx="16" cy="33" r="2" fill="#17644e"/>
</svg>
```

(b) 复制图标：`cp client/src-tauri/icons/icon.ico server/internal/http/favicon.ico`

(c) `server/internal/http/favicon.go`：

```go
package http

import (
	"embed"
	"net/http"
)

//go:embed favicon.svg favicon.ico
var faviconFiles embed.FS

// Favicon assets ship with the binary so every deployment serves the brand mark.
func faviconHandler() http.Handler {
	svg, _ := faviconFiles.ReadFile("favicon.svg")
	ico, _ := faviconFiles.ReadFile("favicon.ico")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if r.URL.Path == "/favicon.ico" {
			w.Header().Set("Content-Type", "image/x-icon")
			w.Write(ico)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(svg)
	})
}
```

(d) `health.go` 的 `NewHandler` 与 `NewRouterWithAuth` 中，在 `mux.Handle("GET /{$}", landingHandler())` 之后各加一行：

```go
	mux.Handle("GET /favicon.svg", faviconHandler())
	mux.Handle("GET /favicon.ico", faviconHandler())
```

（`NewRouterWithAuth` 用的是无方法前缀的 `mux.Handle("/healthz", ...)` 风格——那里用 `mux.Handle("/favicon.svg", faviconHandler())` 不带方法前缀，与其余行一致。）

(e) `landing.html` 的 `<head>`（`<meta charset>` 之后）加：

```html
    <link rel="icon" href="/favicon.svg" type="image/svg+xml">
    <link rel="alternate icon" href="/favicon.ico">
```

下载页：找到 releases 包内的下载 HTML（`server/internal/http/releases` 目录或其引用的模板，含第 178 行 mark 副本的那个文件），在其 `<head>` 加同样两行。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./server/internal/http/ -run TestFavicon -v && go test ./server/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add server/internal/http/favicon.go server/internal/http/favicon.svg server/internal/http/favicon.ico server/internal/http/health.go server/internal/http/landing.html
git commit -m "feat: serve brand favicons from the server"
```

（releases 下载页若属其他目录，一并 add。）

## Task 16: 网页版 favicon + 应用内品牌标记

**Files:**
- Create: `client/public/favicon.svg`
- Modify: `client/index.html`
- Modify: `client/src/components/Icon.tsx`
- Modify: `client/src/styles/desktop.css`

- [ ] **Step 1: favicon**

`cp server/internal/http/favicon.svg client/public/favicon.svg`

`client/index.html` 的 `<head>` 内加：

```html
    <link rel="icon" href="./favicon.svg" type="image/svg+xml">
```

- [ ] **Step 2: Icon.tsx 替换 nest**

`client/src/components/Icon.tsx`：

(a) 导入行去掉 `Stack`，即把 `import { ArrowsDownUp, BookOpen, CaretDown, ChatsCircle, GearSix, HouseSimple, Info, LockKey, FolderSimple, ShieldCheck, SignOut, Stack, Users, WifiHigh, WifiSlash } from "@phosphor-icons/react";` 改为不含 `Stack` 的同列表。

(b) `const icons={nest:Stack,...}` 中的 `nest:Stack` 删除。

(c) `Icon` 函数体在 `const Glyph=icons[name];` 之前插入对 nest 的特判：

```tsx
export function Icon({name,size=20,title,...props}:IconProps){const titleId=useId();if(name==='nest'){const GlyphNest=NestMark;return <GlyphNest size={size} aria-hidden={title?undefined:true} aria-label={title} role={title?'img':undefined} focusable="false" {...props}/>}const Glyph=icons[name];return <Glyph size={size} weight="regular" aria-hidden={title?undefined:true} aria-label={title} role={title?'img':undefined} focusable="false" {...props}/>}
```

(d) 文件末尾追加品牌标记组件（与 landing.html #mark 几何逐字一致）：

```tsx
function NestMark({size=20,...props}:SVGProps<SVGSVGElement>&{size?:number}){
  return <svg width={size} height={size*36/32} viewBox="0 0 32 36" fill="none" {...props}>
    <path d="M5 8 16 2l11 6v13l-11 7L5 21Z" stroke="currentColor" strokeWidth="2"/>
    <path d="m5 8 11 7 11-7M16 15v13M5 15l11 7 11-7" stroke="currentColor" strokeWidth="2"/>
    <circle cx="16" cy="33" r="2" fill="currentColor"/>
  </svg>;
}
```

- [ ] **Step 3: desktop.css 配色**

把 `.ln-brand-mark{display:inline-flex;align-items:center;justify-content:center;width:40px;height:40px;border-radius:13px;color:white;background:#4059e8}` 替换为：

```css
.ln-brand-mark{display:inline-flex;align-items:center;justify-content:center;width:40px;height:40px;border-radius:13px;color:#17644e;background:#e3eee7}
```

把 `.ln-sidebar-brand .ln-brand-mark{background:transparent;color:#d9dfff;width:44px;height:44px}` 替换为：

```css
.ln-sidebar-brand .ln-brand-mark{background:transparent;color:#b4f3d5;width:44px;height:44px}
```

（取值来源：landing.html 第 10 行调色板 `--green:#17644e;--mint:#b4f3d5` 与 `.platform-icon{background:#e3eee7}`。）

- [ ] **Step 4: 验证**

Run: `cd client && npm test && npm run check && npm run build`
Expected: PASS（若视觉相关用例断言了旧色值/旧图标名，按新值更新断言——这是预期内的测试更新）

- [ ] **Step 5: 提交**

```bash
git add client/public/favicon.svg client/index.html client/src/components/Icon.tsx client/src/styles/desktop.css
git commit -m "feat: web favicon and in-app brand mark use the line-cube"
```

## Task 17: Win7 兼容包图标

**Files:**
- Modify: `scripts/package-legacy-windows.ps1`
- Modify: `client/legacy/installer/legacy.nsi`
- Modify: `client/legacy/shell/main.cjs`

- [ ] **Step 1: 打包脚本 rcedit**

`scripts/package-legacy-windows.ps1`：

(a) 现有拷贝行（约第 47 行，把 `client/src-tauri/icons/icon.png` 拷到 appRoot）之后增加拷贝 ico：

```powershell
  Copy-Item "$repoRoot/client/src-tauri/icons/icon.ico" (Join-Path $stage 'resources/app/icon.ico') -Force
```

（变量名 `$repoRoot/$stage` 以脚本内现有变量为准逐字对齐。）

(b) 在重命名 exe（`electron.exe` → `LinkNestX-Legacy.exe`）之后、NSIS 打包之前插入：

```powershell
  npx --yes rcedit-x64 "$exePath" --set-icon (Join-Path $stage 'resources/app/icon.ico')
```

（`$exePath` 同样以脚本内实际变量为准；rcedit 的包名用 `rcedit-x64`，它无需本地依赖且同时提供 32 位场景的 `--arch` 参数——若脚本运行环境已有别的调用方式，保持仓库风格。）

- [ ] **Step 2: NSIS 图标**

`client/legacy/installer/legacy.nsi`：

(a) 头部 `!define MUI_ABORTWARNING` 之前加：

```nsis
!define MUI_ICON "${APP_STAGE}\resources\app\icon.ico"
!define MUI_UNICON "${APP_STAGE}\resources\app\icon.ico"
```

（`${APP_STAGE}` 为脚本传入的 define——以文件顶部既有 `!define APP_*` 的传入方式为准。）

(b) 两处 `CreateShortcut` 各追加图标参数：

```nsis
    CreateShortcut "$SMPROGRAMS\LinkNestX\LinkNestX (${APP_ARCH}).lnk" "$INSTDIR\LinkNestX-Legacy.exe" "" "$INSTDIR\resources\app\icon.ico"
    CreateShortcut "$DESKTOP\LinkNestX (${APP_ARCH}).lnk" "$INSTDIR\LinkNestX-Legacy.exe" "" "$INSTDIR\resources\app\icon.ico"
```

- [ ] **Step 3: Electron 窗口图标**

`client/legacy/shell/main.cjs` 第 139 行 `new BrowserWindow({...})` 的选项对象中加：

```js
      icon: path.join(appRoot, "icon.png"),
```

（`appRoot`/`path` 已在该文件作用域内——第 151 行托盘已用 `appRoot/icon.png`，逐字对齐其写法。）

- [ ] **Step 4: 验证**

Run（构建 Win7 包需要 Windows 与 NSIS——本任务只验证语法与引用存在）:

```bash
node --test scripts/test-smoke-script.mjs
ls client/src-tauri/icons/icon.ico
```

Expected: smoke 测试 PASS；ico 存在（Task 14 已生成）。在具备 NSIS 的环境完整跑一次 `powershell -File scripts/package-legacy-windows.ps1` 留作发布前人工步骤。

- [ ] **Step 5: 提交**

```bash
git add scripts/package-legacy-windows.ps1 client/legacy/installer/legacy.nsi client/legacy/shell/main.cjs
git commit -m "feat: legacy Windows package installs branded icons"
```

## Task 18: 收尾全量验证

- [ ] **Step 1: 全量测试**

```bash
go test ./server/...
cd client && npm test && npm run check
cargo test --manifest-path client/src-tauri/Cargo.toml
```
Expected: 全部 PASS

- [ ] **Step 2: 视觉快照重拍（人工）**

```bash
cd client/tests/visual && node capture-v3.mjs   # 按该目录 README/脚本注释确认调用方式
```
新截图替换 `docs/assets` 下旧图（若有）。此步不阻塞合并。

- [ ] **Step 3: 人工桌面验收**

按 Task 13 Step 4 的清单执行；图标部分另验：任务栏/exe/托盘/安装包快捷方式/浏览器页签/官网页签 六处图标一致（线条立方体）。

- [ ] **Step 4: 提交（如有快照/文档变更）**

```bash
git add -A
git commit -m "chore: refresh visual snapshots for branded icons"
```

---

## 自查记录（写计划时已核对）

1. **Spec 覆盖**：第 1 节图标 8 项改动清单 → Task 14–17 全覆盖（含 favicon 路由、React 品牌、Win7 三处）；第 2 节群昵称（迁移/三个名单解析/新接口/事件/客户端面板/成员行/openapi/文档）→ Task 1–7；第 3 节角标（门控、浏览器 foreground、重查、onResized 降级、attempts 归零、30s 慢重试、毒丸二分、测试 a–f）→ Task 8–13（测试 a=Task10-1、b=Task10-2、c/d=Task9、e=Task12、f=Task13）。
2. **占位符**：测试辅助（`login`/`observe`/`testConfig`/PowerShell 变量名）标注了"以文件内既有写法逐字对齐"——这些是仓库内已存在的具体事实，不是待定设计；其余代码均为完整可执行内容。
3. **类型一致性**：`Member.Nickname *string` ↔ 客户端 `nickname?: string|null`；事件名 `nickname.changed` 全链路（ws.go → MessagingWorkspace/ConversationView/GroupsView/ProfilePanel）一致；`ApiError.status` ↔ tracker `isBadRequest` 读取 `(error as {status?:unknown}).status===400` ↔ Rust `TransferError{status}` 字段一致；`SetNicknameObserver(func(string,string))` ↔ `hub.NicknameChanged(group,user)` 签名一致。
4. **有意偏离 spec 的两点**（已论证）：(a) spec 提议 project() 预取整群映射——实现按"页面路径预取、单消息路径单查"双轨（projectWith 的 nil/非 nil map 语义），与代码库现有每消息多查询的量级一致且更简单；(b) 毒丸剔除依赖 HTTP 状态码——借道既有 `TransferError.status` 序列化通道（rest.ts 透传），不改 Rust。

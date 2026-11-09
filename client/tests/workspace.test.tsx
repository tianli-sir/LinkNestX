import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { harness, msg, user } from "./workspace-fixtures";
describe("real desktop workflows", () => {
  it("shows authenticated groups and safely renders messages from forward pages", async () => {
    const h = harness({ request: async (_, path) => path.includes("/messages?") ? path.includes("cursor=cursor-1") ? { messages: [msg("2", 2, "第二条")], nextCursor: "cursor-2", hasMore: false } : { messages: [msg("1", 1, '<img src=x onerror="alert(1)">')], nextCursor: "cursor-1", hasMore: true } : undefined });
    await h.login();
    await screen.findByText("第二条");
    expect(screen.getByText('<img src=x onerror="alert(1)">')).toBeInTheDocument();
    expect(document.querySelector(".ln-message img")).toBeNull();
    expect(h.request.mock.calls.filter(c => c[1].includes("/messages?")).length).toBe(2);
  });
  it("retries the original message ID and merges duplicated live events", async () => {
    let failures = 1;
    const h = harness({ request: async (method, path, body) => {
        if (method === "POST" && path.endsWith("/messages")) {
          if (failures-- > 0)
            throw new Error("网络断开");
          return { ...msg("posted", 1, body.body), clientMessageId: body.clientMessageId };
        }
      } });
    await h.login();
    fireEvent.change(screen.getByLabelText("消息内容"), { target: { value: "准备发布" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByText("网络断开");
    fireEvent.click(screen.getByRole("button", { name: "重试发送" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "重试发送" })).not.toBeInTheDocument());
    const posts = h.request.mock.calls.filter(c => c[0] === "POST");
    expect(posts).toHaveLength(2);
    expect(posts[0][2].clientMessageId).toBe(posts[1][2].clientMessageId);
    await h.emit("server.event", { type: "message.created", groupId: "g1", message: { ...msg("posted", 1, "准备发布"), clientMessageId: posts[0][2].clientMessageId } });
    expect(screen.getAllByText("准备发布")).toHaveLength(1);
  });
  it("catches up on session.ready and ignores stale group responses", async () => {
    let resolveOld!: (value: any) => void;
    let oldPending = true;
    const h = harness({ request: async (_, path) => {
        if (path.includes("g1/messages?") && oldPending) {
          oldPending = false;
          return new Promise(r => resolveOld = r);
        }
        if (path.includes("g2/messages?"))
          return { messages: [msg("new", 2, "设计内容", "g2")], nextCursor: "new-cursor", hasMore: false };
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "设计讨论" }));
    await screen.findByText("设计内容");
    await act(async () => resolveOld({ messages: [msg("old", 1, "过时响应")], nextCursor: "old", hasMore: false }));
    expect(screen.queryByText("过时响应")).not.toBeInTheDocument();
    await h.emit("server.event", { type: "session.ready" });
    await waitFor(() => expect(h.request.mock.calls.some(c => c[1].includes("cursor=new-cursor"))).toBe(true));
  });
  it("delegates version download to native and hides pool deletion from employees", async () => {
    const h = harness({ request: async (_, path) => {
        if (path.includes("scope=pool"))
          return [{ id: "f1", name: "产品手册.pdf", visibility: "pool", currentVersionId: "v2", status: "active", createdAt: "2026-11-09" }];
        if (path.endsWith("/f1/versions"))
          return [{ versionId: "v1", fileId: "f1", size: 20, createdAt: "2026-11-08" }, { versionId: "v2", fileId: "f1", size: 30, createdAt: "2026-11-09" }];
      }, command: async (name) => name === "pick_download_destination" ? "/chosen/manual.pdf" : name === "start_download" ? "task1" : undefined });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "公共文件池" }));
    await screen.findByText("产品手册.pdf");
    expect(screen.queryByRole("button", { name: /删除/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "版本" }));
    await screen.findByLabelText("下载版本");
    fireEvent.change(screen.getByLabelText("下载版本"), { target: { value: "v1" } });
    fireEvent.click(screen.getByRole("button", { name: "下载所选版本" }));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("start_download", { versionId: "v1", destination: "/chosen/manual.pdf" }));
    expect(h.invoke).toHaveBeenCalledWith("pick_download_destination", { name: "产品手册.pdf" });
  });
  it("applies transfer events, pauses native task, and removes listeners on exit", async () => {
    const transfer = { id: "t1", name: "报告.zip", direction: "download", versionId: "v1", state: "transferring", bytesCompleted: 50, totalBytes: 100, activeSources: ["server", "peer-a"] };
    const h = harness({ command: async (name) => name === "list_transfers" ? [transfer] : name === "pause_transfer" ? null : undefined });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "传输任务" }));
    await screen.findByText("报告.zip");
    expect(screen.getByRole("progressbar")).toHaveAttribute("value", "50");
    fireEvent.click(screen.getByRole("button", { name: "暂停" }));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("pause_transfer", { taskId: "t1" }));
    await h.emit("transfer.updated", { ...transfer, state: "paused" });
    await screen.findByRole("button", { name: "继续" });
    h.view.unmount();
    expect([...h.listeners.values()].every(s => s.size === 0)).toBe(true);
  });
  it("creates users through the admin API and shows server errors", async () => {
    const h = harness({ admin: true, request: async (method, path) => {
        if (method === "POST" && path === "/api/v1/users")
          throw new Error("用户名已存在");
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "管理中心" }));
    fireEvent.change(await screen.findByLabelText("新用户名"), { target: { value: "colleague" } });
    fireEvent.change(screen.getByLabelText("初始密码"), { target: { value: "new-password" } });
    fireEvent.click(screen.getByRole("button", { name: "创建账号" }));
    await screen.findByText("用户名已存在");
    expect(h.request).toHaveBeenCalledWith("POST", "/api/v1/users", expect.objectContaining({ username: "colleague", password: "new-password" }));
  });
  it("requires a password change before loading the workspace", async () => {
    const h = harness({ mustChangePassword: true, request: async (method, path) => method === "POST" && path === "/api/v1/auth/password" ? { ...user, mustChangePassword: false } : undefined });
    await h.login();
    expect(h.request.mock.calls.some(c => c[1] === "/api/v1/groups")).toBe(false);
    fireEvent.change(screen.getByLabelText("当前密码"), { target: { value: "old" } });
    fireEvent.change(screen.getByLabelText("新密码"), { target: { value: "new-password" } });
    fireEvent.change(screen.getByLabelText("确认新密码"), { target: { value: "new-password" } });
    fireEvent.click(screen.getByRole("button", { name: "保存新密码" }));
    await screen.findByRole("button", { name: "研发协作" });
  });
  it("keeps delivered messages delivered when HTTP fails after a socket acknowledgement", async () => {
    let rejectPost!: (error: Error) => void;
    const h = harness({ request: async (method, path) => {
        if (method === "POST" && path.endsWith("/messages"))
          return new Promise((_, reject) => rejectPost = reject);
      } });
    await h.login();
    fireEvent.change(screen.getByLabelText("消息内容"), { target: { value: "已送达" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(h.request.mock.calls.some(c => c[0] === "POST")).toBe(true));
    const posted = h.request.mock.calls.find(c => c[0] === "POST")![2];
    await h.emit("server.event", { type: "message.created", groupId: "g1", message: { ...msg("delivered", 1, "已送达"), senderId: "u1", clientMessageId: posted.clientMessageId } });
    await act(async () => rejectPost(new Error("请求响应丢失")));
    expect(screen.queryByRole("button", { name: "重试发送" })).not.toBeInTheDocument();
    expect(screen.getAllByText("已送达")).toHaveLength(1);
  });
  it("queues a selected upload and refreshes files on completion", async () => {
    let published = false;
    const h = harness({ request: async (_, path) => {
        if (path.includes("/files?groupId="))
          return published ? [{ id: "f1", name: "会议记录.txt", size: 20, createdBy: "u1", visibility: "group", currentVersionId: "v1", status: "active", createdAt: "2026-11-09" }] : [];
      }, command: async (name) => name === "pick_upload_file" ? "/chosen/minutes.txt" : name === "start_upload" ? "upload-task" : undefined });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "群文件" }));
    fireEvent.click(await screen.findByRole("button", { name: "上传文件" }));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("start_upload", { groupId: "g1", source: "/chosen/minutes.txt", fileId: null }));
    published = true;
    await h.emit("transfer.updated", { id: "upload-task", direction: "upload", name: "会议记录.txt", state: "completed", totalBytes: 20, bytesCompleted: 20, activeSources: [] });
    await screen.findByText("会议记录.txt");
    expect(screen.getByRole("button", { name: "删除" })).toBeInTheDocument();
  });
  it("allows only administrators to promote pool files and requires delete confirmation", async () => {
    let removed = false;
    let pooled = false;
    const file = { id: "f1", name: "政策.pdf", size: 40, createdBy: "other", visibility: "group", currentVersionId: "v1", status: "active", createdAt: "2026-11-09" };
    const h = harness({ admin: true, request: async (method, path) => {
        if (method === "POST" && path.endsWith("/promote")) {
          pooled = true;
          return null;
        }
        if (method === "DELETE" && path.endsWith("/f1")) {
          removed = true;
          return null;
        }
        if (path.includes("/files?"))
          return removed ? [] : [{ ...file, visibility: pooled ? "pool" : "group" }];
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "群文件" }));
    fireEvent.click(await screen.findByRole("button", { name: "加入公共池" }));
    await screen.findByText("长期保留");
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(h.request.mock.calls.some(c => c[0] === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "确认删除" }));
    await screen.findByText("文件已删除。");
    await waitFor(() => expect(screen.queryByText("政策.pdf")).not.toBeInTheDocument());
  });
  it("lets group owners select a real employee and add membership", async () => {
    const h = harness({ request: async (method, path) => {
        if (path.endsWith("/members"))
          return method === "POST" ? null : [{ userId: "u1", role: "owner", joinedAt: "2026-11-09" }];
        if (path === "/api/v1/users")
          return [user, { id: "u2", username: "小陈", email: "chen@example.com" }];
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "成员" }));
    fireEvent.change(await screen.findByLabelText("选择同事"), { target: { value: "u2" } });
    fireEvent.click(screen.getByRole("button", { name: "保存成员" }));
    await waitFor(() => expect(h.request).toHaveBeenCalledWith("POST", "/api/v1/groups/g1/members", { userId: "u2", role: "member" }));
  });
  it("shows settings failure without claiming a save", async () => {
    const policy = { enabled: true, cacheQuotaBytes: 10000000000, maxConcurrent: 3, bandwidthLimitBps: 0, autostart: true };
    const h = harness({ command: async (name) => {
        if (name === "get_sharing_policy")
          return policy;
        if (name === "set_sharing_policy")
          throw new Error("无法启用系统启动项");
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "小林 的账户选项" }));
    fireEvent.click(screen.getByRole("button", { name: "设置" }));
    fireEvent.click(await screen.findByLabelText("允许本机共享缓存分片"));
    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
    await screen.findByText("无法启用系统启动项");
    expect(screen.queryByText("传输与共享设置已保存。")).not.toBeInTheDocument();
    expect(h.invoke).toHaveBeenCalledWith("set_sharing_policy", { policy: { ...policy, enabled: false } });
  });
  it("creates accounts and groups then reads filtered audit data", async () => {
    const h = harness({ admin: true, request: async (method, path) => {
        if (method === "POST" && path === "/api/v1/users")
          return { ...user, id: "new", username: "新同事" };
        if (method === "POST" && path === "/api/v1/groups")
          return { id: "new-group", name: "新项目" };
        if (path.startsWith("/api/v1/admin/audit?"))
          return { events: [{ id: "event-1", action: "user.create", actorId: "u1", targetId: "new", createdAt: "2026-11-09" }], total: 1 };
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "管理中心" }));
    fireEvent.change(await screen.findByLabelText("新用户名"), { target: { value: "新同事" } });
    fireEvent.change(screen.getByLabelText("初始密码"), { target: { value: "new-password" } });
    fireEvent.click(screen.getByRole("button", { name: "创建账号" }));
    await screen.findByText("账号已创建。请通过可信方式告知同事初始密码。");
    expect(screen.getByLabelText("初始密码")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "创建群组" }));
    fireEvent.change(screen.getByLabelText("群组名称"), { target: { value: "新项目" } });
    fireEvent.change(screen.getByLabelText("指定群主"), { target: { value: "u1" } });
    fireEvent.click(screen.getAllByRole("button", { name: "创建群组" })[1]);
    await screen.findByText("群组已创建，群主可在群组成员中添加同事。");
    expect(h.request).toHaveBeenCalledWith("POST", "/api/v1/groups", { name: "新项目", ownerId: "u1" });
    fireEvent.click(screen.getByRole("button", { name: "审计记录" }));
    await screen.findByText("user.create");
    fireEvent.change(screen.getByLabelText("按操作筛选"), { target: { value: "user.create" } });
    fireEvent.click(screen.getByRole("button", { name: "查询" }));
    await waitFor(() => expect(h.request.mock.calls.some(c => c[1].includes("action=user.create&limit=50&offset=0"))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "查看本页 JSON" }));
    expect((screen.getByLabelText("当前页审计数据（JSON）") as HTMLTextAreaElement).value).toContain('"action": "user.create"');
  });
});

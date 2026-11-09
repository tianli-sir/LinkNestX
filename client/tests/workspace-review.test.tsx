import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { harness, msg } from "./workspace-fixtures";
const file = { id: "f1", groupId: "g1", name: "设计规范.pdf", size: 100, createdBy: "u2", visibility: "group", currentVersionId: "v1", status: "active", createdAt: "2026-11-09" };
describe("desktop review regressions", () => {
  it("reconciles a failed send confirmed only through REST reconnect catchup", async () => {
    let accepted: any;
    const h = harness({ request: async (method, path, body) => {
        if (method === "POST" && path.endsWith("/messages")) {
          accepted = { ...msg("accepted", 1, body.body), senderId: "u1", clientMessageId: body.clientMessageId };
          throw new Error("响应中断");
        }
        if (path.includes("/messages?") && accepted)
          return { messages: [accepted], nextCursor: "accepted-cursor", hasMore: false };
      } });
    await h.login();
    fireEvent.change(screen.getByLabelText("消息内容"), { target: { value: "服务器已收下" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByRole("button", { name: "重试发送" });
    await h.emit("server.event", { type: "session.ready" });
    await waitFor(() => expect(screen.queryByRole("button", { name: "重试发送" })).not.toBeInTheDocument());
    expect(screen.getAllByText("服务器已收下")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("消息内容"), { target: { value: "下一条" } });
    expect(screen.getByRole("button", { name: "发送" })).toBeEnabled();
  });
  it("keeps a REST-confirmed send delivered after its late HTTP failure and accepts another send", async () => {
    let rejectFirst!: (error: Error) => void;
    let accepted: any;
    let sends = 0;
    const h = harness({ request: async (method, path, body) => {
        if (method === "POST" && path.endsWith("/messages")) {
          sends++;
          const delivered = { ...msg(`accepted-${sends}`, sends, body.body), senderId: "u1", clientMessageId: body.clientMessageId };
          if (sends === 1) {
            accepted = delivered;
            return new Promise((_, reject) => rejectFirst = reject);
          }
          return delivered;
        }
        if (path.includes("/messages?") && accepted)
          return { messages: [accepted], nextCursor: "accepted-cursor", hasMore: false };
      } });
    await h.login();
    fireEvent.change(screen.getByLabelText("消息内容"), { target: { value: "先确认的消息" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(sends).toBe(1));
    await h.emit("server.event", { type: "session.ready" });
    await waitFor(() => expect(screen.getAllByText("先确认的消息")).toHaveLength(1));
    fireEvent.change(screen.getByLabelText("消息内容"), { target: { value: "随后发送" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(sends).toBe(2));
    await screen.findByText("随后发送");
    await act(async () => rejectFirst(new Error("迟到的响应失败")));
    expect(screen.queryByRole("button", { name: "重试发送" })).not.toBeInTheDocument();
    expect(screen.queryByText("迟到的响应失败")).not.toBeInTheDocument();
  });
  it("does not allow original uploader or group owner to upload pool versions", async () => {
    const h = harness({ request: async (_, path) => {
        if (path.endsWith("/members"))
          return [{ userId: "u1", role: "owner", joinedAt: "2026-11-09" }];
        if (path.includes("/files?"))
          return [{ ...file, createdBy: "u1", visibility: "pool" }];
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "群文件" }));
    await screen.findByText(file.name);
    expect(screen.queryByRole("button", { name: "上传新版本" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "删除" })).not.toBeInTheDocument();
  });
  it("refreshes current owner permissions on reconnect and clears them after a failed membership read", async () => {
    let role = "member";
    let fail = false;
    const h = harness({ request: async (_, path) => {
        if (path.endsWith("/members")) {
          if (fail)
            throw new Error("成员信息读取失败");
          return [{ userId: "u1", role, joinedAt: "2026-11-09" }];
        }
        if (path.includes("/files?"))
          return [file];
      } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "群文件" }));
    await screen.findByText(file.name);
    expect(screen.queryByRole("button", { name: "删除" })).not.toBeInTheDocument();
    role = "owner";
    await h.emit("server.event", { type: "session.ready" });
    await screen.findByRole("button", { name: "删除" });
    role = "member";
    await h.emit("server.event", { type: "session.ready" });
    await waitFor(() => expect(screen.queryByRole("button", { name: "删除" })).not.toBeInTheDocument());
    role = "owner";
    fireEvent.click(within(screen.getByRole("complementary", { name: "我的群组" })).getByRole("button", { name: "刷新" }));
    await screen.findByRole("button", { name: "删除" });
    fail = true;
    await h.emit("server.event", { type: "session.ready" });
    await waitFor(() => expect(screen.queryByRole("button", { name: "删除" })).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "成员" }));
    await screen.findByText("成员信息读取失败");
    expect(screen.queryByRole("button", { name: "保存成员" })).not.toBeInTheDocument();
  });
  it("rereads membership when returning to member management", async () => {
    let role = "member";
    const h = harness({ request: async (_, path) => path.endsWith("/members") ? [{ userId: "u1", role, joinedAt: "2026-11-09" }] : undefined });
    await h.login();
    role = "owner";
    fireEvent.click(screen.getByRole("button", { name: "成员" }));
    await screen.findByRole("button", { name: "保存成员" });
    fireEvent.click(screen.getByRole("button", { name: "聊天" }));
    role = "member";
    fireEvent.click(screen.getByRole("button", { name: "成员" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "保存成员" })).not.toBeInTheDocument());
  });
  it("refreshes an open version panel after uploads, list refresh, and promotion", async () => {
    let current = "v1";
    let pooled = false;
    const h = harness({ admin: true, request: async (method, path) => {
        if (method === "POST" && path.endsWith("/promote")) {
          pooled = true;
          return null;
        }
        if (path.includes("/files?"))
          return [{ ...file, currentVersionId: current, visibility: pooled ? "pool" : "group", size: current === "v1" ? 100 : 200 }];
        if (path.endsWith("/f1/versions"))
          return ["v1", ...(current === "v1" ? [] : [current])].map(versionId => ({ versionId, fileId: "f1", size: versionId === "v1" ? 100 : 200, createdAt: "2026-11-09" }));
      }, command: async (name) => name === "pick_download_destination" ? "/chosen/file.pdf" : name === "start_download" ? "download1" : undefined });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "群文件" }));
    fireEvent.click(await screen.findByRole("button", { name: "版本" }));
    await screen.findByLabelText("下载版本");
    current = "v2";
    await h.emit("transfer.updated", { id: "upload1", direction: "upload", name: file.name, state: "completed", totalBytes: 200, bytesCompleted: 200, activeSources: [] });
    await waitFor(() => expect(within(screen.getByLabelText("下载版本")).getByRole("option", { name: /当前版本/ })).toHaveValue("v2"));
    fireEvent.change(screen.getByLabelText("下载版本"), { target: { value: "v2" } });
    fireEvent.click(screen.getByRole("button", { name: "下载所选版本" }));
    await waitFor(() => expect(h.invoke).toHaveBeenCalledWith("pick_download_destination", { name: file.name }));
    current = "v3";
    fireEvent.click(screen.getAllByRole("button", { name: "刷新" })[1]);
    await waitFor(() => expect(within(screen.getByLabelText("下载版本")).getByRole("option", { name: /当前版本/ })).toHaveValue("v3"));
    fireEvent.click(screen.getByRole("button", { name: "加入公共池" }));
    await waitFor(() => expect(within(screen.getByRole("region", { name: `${file.name}的版本` })).getByText(/长期保留/)).toBeInTheDocument());
  });
  it("positions long history at newest, preserves an older reading position, and follows an own send", async () => {
    let finishHistory!: (value: any) => void;
    const h = harness({ request: async (method, path, body) => {
        if (method === "GET" && path.includes("/messages?"))
          return new Promise(resolve => finishHistory = resolve);
        if (method === "POST" && path.endsWith("/messages"))
          return { ...msg("own", 33, body.body), senderId: "u1", clientMessageId: body.clientMessageId };
      } });
    await h.login();
    const scroll = screen.getByRole("region", { name: "聊天记录" });
    let height = 2000;
    Object.defineProperties(scroll, { scrollHeight: { get: () => height, configurable: true }, clientHeight: { value: 400, configurable: true } });
    await act(async () => finishHistory({ messages: Array.from({ length: 30 }, (_, i) => msg(String(i), i, `历史消息 ${i}`)), nextCursor: "after30", hasMore: false }));
    expect(scroll.scrollTop).toBe(1600);
    scroll.scrollTop = 600;
    fireEvent.scroll(scroll);
    height = 2100;
    await h.emit("server.event", { type: "message.created", groupId: "g1", message: msg("remote", 31, "新消息") });
    expect(scroll.scrollTop).toBe(600);
    fireEvent.click(screen.getByRole("button", { name: "有新消息，回到最新" }));
    expect(scroll.scrollTop).toBe(1700);
    scroll.scrollTop = 500;
    fireEvent.scroll(scroll);
    height = 2200;
    fireEvent.change(screen.getByLabelText("消息内容"), { target: { value: "我发送的新消息" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByText("我发送的新消息");
    expect(scroll.scrollTop).toBe(1800);
  });
});

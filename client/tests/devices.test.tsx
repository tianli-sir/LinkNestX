import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createNativeTransport, RestClient } from "../src/api/rest";
import { AuthProvider } from "../src/auth";
import { AdminView } from "../src/features/AdminView";
import { App } from "../src/main";
import { harness, user } from "./workspace-fixtures";

const policy = { enabled: true, cacheQuotaBytes: 10000000000, maxConcurrent: 3, bandwidthLimitBps: 0, autostart: true };
const currentDevice = { id: "d", userId: "u1", name: "办公电脑", createdAt: "2026-11-08T01:00:00Z", lastSeen: "2026-11-09T02:30:00Z", active: true, sessionCount: 2, current: true };
const otherDevice = { ...currentDevice, id: "other /device", name: "家中电脑", sessionCount: 1, current: false };
const sharingCommand = async (name: string) => name === "get_transfer_preferences" ? policy : undefined;

async function openSettings(h: ReturnType<typeof harness>) {
  await h.login();
  fireEvent.click(screen.getByRole("button", { name: "小林 的账户选项" }));
  fireEvent.click(screen.getByRole("button", { name: "设置" }));
}

describe("device management", () => {
  it("lists own devices with current status and readable dates, and disables inactive devices", async () => {
    const h = harness({ command: sharingCommand, request: async (method, path) => {
      if (method === "GET" && path === "/api/v1/me/devices")
        return [currentDevice, { ...otherDevice, name: "   ", active: false, sessionCount: 0 }];
    } });
    await openSettings(h);
    const table = await screen.findByRole("table", { name: "我的设备" });
    const current = within(table).getByRole("row", { name: /办公电脑/ });
    expect(current).toHaveTextContent("当前设备");
    expect(current).toHaveTextContent("2 个会话");
    expect(current).toHaveTextContent("2026/11/08");
    expect(current).toHaveTextContent("2026/11/09");
    const inactive = within(table).getByRole("row", { name: /未命名设备/ });
    expect(inactive).toHaveTextContent("无有效会话");
    expect(within(inactive).getByRole("button", { name: "撤销设备" })).toBeDisabled();
    expect(h.request).toHaveBeenCalledWith("GET", "/api/v1/me/devices", undefined);
    expect(h.request.mock.calls.some(([, path]) => path.startsWith("/api/v1/admin/"))).toBe(false);
  });

  it("requires confirmation, cancels without a request, and revokes only the selected own device", async () => {
    let revoked = false;
    const h = harness({ command: sharingCommand, request: async (method, path) => {
      if (method === "GET" && path === "/api/v1/me/devices")
        return [currentDevice, { ...otherDevice, active: !revoked, sessionCount: revoked ? 0 : 1 }];
      if (method === "POST" && path === "/api/v1/me/devices/other%20%2Fdevice/revoke") {
        revoked = true;
        return {};
      }
    } });
    await openSettings(h);
    const row = await screen.findByRole("row", { name: /家中电脑/ });
    fireEvent.click(within(row).getByRole("button", { name: "撤销设备" }));
    const confirmation = screen.getByRole("form", { name: "撤销设备确认" });
    expect(confirmation).toHaveTextContent("家中电脑");
    expect(confirmation).toHaveTextContent("重新登录");
    expect(h.request.mock.calls.filter(([method]) => method === "POST")).toHaveLength(0);
    fireEvent.click(within(confirmation).getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("form", { name: "撤销设备确认" })).not.toBeInTheDocument();
    expect(h.request.mock.calls.filter(([method]) => method === "POST")).toHaveLength(0);
    fireEvent.click(within(row).getByRole("button", { name: "撤销设备" }));
    fireEvent.click(screen.getByRole("button", { name: "确认撤销" }));
    await waitFor(() => expect(within(row).getByRole("button", { name: "撤销设备" })).toBeDisabled());
    expect(h.request.mock.calls.filter(([method]) => method === "POST")).toEqual([["POST", "/api/v1/me/devices/other%20%2Fdevice/revoke", {}]]);
    expect(h.request.mock.calls.filter(([method, path]) => method === "GET" && path === "/api/v1/me/devices")).toHaveLength(2);
    expect(screen.getByRole("heading", { name: "设置", level: 2 })).toBeInTheDocument();
  });

  it("shows loading and an empty state without inventing devices", async () => {
    let resolveDevices!: (value: unknown[]) => void;
    const h = harness({ command: sharingCommand, request: async (method, path) => {
      if (method === "GET" && path === "/api/v1/me/devices")
        return new Promise(resolve => { resolveDevices = resolve; });
    } });
    await openSettings(h);
    const section = screen.getByRole("region", { name: "我的设备" });
    expect(within(section).getByRole("status")).toHaveTextContent("正在加载");
    expect(within(section).getByRole("button", { name: "刷新设备" })).toBeDisabled();
    await act(async () => resolveDevices([]));
    expect(within(section).getByText("暂无设备记录。")).toBeInTheDocument();
    expect(within(section).queryByRole("table")).not.toBeInTheDocument();
  });

  it("preserves the device list on refresh failure and retries the read", async () => {
    let fail = false;
    const h = harness({ command: sharingCommand, request: async (method, path) => {
      if (method === "GET" && path === "/api/v1/me/devices") {
        if (fail) throw new Error("设备列表暂时无法读取");
        return [otherDevice];
      }
    } });
    await openSettings(h);
    await screen.findByText("家中电脑");
    fail = true;
    fireEvent.click(screen.getByRole("button", { name: "刷新设备" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("设备列表暂时无法读取");
    expect(screen.getByText("家中电脑")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(screen.getByText("家中电脑")).toBeInTheDocument();
    expect(h.request.mock.calls.filter(([, path]) => path === "/api/v1/me/devices")).toHaveLength(3);
  });

  it("keeps the selected device and confirmation after a failed revoke so it can be retried", async () => {
    let fail = true;
    let revoked = false;
    const h = harness({ command: sharingCommand, request: async (method, path) => {
      if (method === "GET" && path === "/api/v1/me/devices")
        return [{ ...otherDevice, active: !revoked, sessionCount: revoked ? 0 : 1 }];
      if (method === "POST" && path.endsWith("/revoke")) {
        if (fail) throw new Error("设备撤销失败，请重试");
        revoked = true;
        return {};
      }
    } });
    await openSettings(h);
    fireEvent.click(await screen.findByRole("button", { name: "撤销设备" }));
    fireEvent.click(screen.getByRole("button", { name: "确认撤销" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("设备撤销失败，请重试");
    expect(screen.getByRole("form", { name: "撤销设备确认" })).toHaveTextContent("家中电脑");
    expect(screen.getByRole("row", { name: /家中电脑/ })).toHaveTextContent("1 个会话");
    expect(screen.getByRole("button", { name: "确认撤销" })).toBeEnabled();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "确认撤销" }));
    await waitFor(() => expect(screen.queryByRole("form", { name: "撤销设备确认" })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "撤销设备" })).toBeDisabled();
    expect(h.request.mock.calls.filter(([method]) => method === "POST")).toHaveLength(2);
  });

  it("lists all devices for administrators with account names and uses the selected admin revoke endpoint", async () => {
    let revoked = false;
    const h = harness({ admin: true, request: async (method, path) => {
      if (path === "/api/v1/admin/users") return [user, { ...user, id: "u2", username: "小王" }];
      if (method === "GET" && path === "/api/v1/admin/devices")
        return [currentDevice, { ...otherDevice, userId: "u2", active: !revoked, sessionCount: revoked ? 0 : 1 }];
      if (method === "POST" && path === "/api/v1/admin/devices/other%20%2Fdevice/revoke") {
        revoked = true;
        return {};
      }
    } });
    await h.login();
    fireEvent.click(screen.getByRole("button", { name: "管理" }));
    fireEvent.click(screen.getByRole("button", { name: "设备管理" }));
    const row = await screen.findByRole("row", { name: /家中电脑/ });
    expect(row).toHaveTextContent("小王");
    fireEvent.click(within(row).getByRole("button", { name: "撤销设备" }));
    expect(screen.getByRole("form", { name: "撤销设备确认" })).toHaveTextContent("小王");
    fireEvent.click(screen.getByRole("button", { name: "确认撤销" }));
    await waitFor(() => expect(within(row).getByRole("button", { name: "撤销设备" })).toBeDisabled());
    expect(h.request.mock.calls.filter(([method]) => method === "POST")).toEqual([["POST", "/api/v1/admin/devices/other%20%2Fdevice/revoke", {}]]);
    expect(h.request.mock.calls.filter(([method, path]) => method === "GET" && path === "/api/v1/admin/devices")).toHaveLength(2);
    expect(h.request.mock.calls.some(([, path]) => path.startsWith("/api/v1/me/devices"))).toBe(false);
  });

  it("does not expose administrator device management to employees", async () => {
    const h = harness({ command: sharingCommand, request: async (_, path) => path === "/api/v1/me/devices" ? [] : undefined });
    await openSettings(h);
    await screen.findByText("暂无设备记录。");
    expect(screen.queryByRole("button", { name: "管理" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "设备管理" })).not.toBeInTheDocument();
    expect(h.request.mock.calls.some(([, path]) => path.startsWith("/api/v1/admin/"))).toBe(false);
  });

  it("does not request administrator data if the protected view is mounted without an admin session", () => {
    const request = vi.fn(async () => []);
    const transport = { request: async <T,>() => await request() as T, login: vi.fn(), logout: vi.fn() };
    render(<AuthProvider transport={transport}><AdminView client={new RestClient(transport)} /></AuthProvider>);
    expect(screen.getByRole("alert")).toHaveTextContent("需要管理员权限");
    expect(request).not.toHaveBeenCalled();
  });

  it.each([false, true])("logs out locally through the native command after current-device revocation, remote logout failure: %s", async (logoutFails) => {
    const calls: Array<{ command: string; args?: Record<string, unknown> }> = [];
    const nativeInvoke = async <T,>(command: string, args?: Record<string, unknown>): Promise<T> => {
      calls.push({ command, args });
      if (command === "login") return { id: "s", accessToken: "a", refreshToken: "r", expiresAt: "", deviceId: "d", user } as T;
      if (command === "logout") {
        if (logoutFails) throw new Error("会话已撤销");
        return undefined as T;
      }
      if (command === "get_transfer_preferences") return policy as T;
      if (command === "list_transfers") return [] as T;
      if (command === "api_request") {
        if (args?.path === "/api/v1/me/devices") return [currentDevice] as T;
        if (args?.path === "/api/v1/me/devices/d/revoke") return {} as T;
        if (args?.path === "/api/v1/groups") return [] as T;
      }
      throw new Error(`未提供响应 ${command} ${String(args?.path)}`);
    };
    render(<App transport={createNativeTransport(nativeInvoke)} desktop={{ invoke: nativeInvoke, listen: async () => () => undefined }} storage={{ getItem: () => null, setItem: () => undefined }} />);
    fireEvent.change(screen.getByLabelText("账号"), { target: { value: "小林" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "password" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    fireEvent.click(await screen.findByRole("button", { name: "小林 的账户选项" }));
    fireEvent.click(screen.getByRole("button", { name: "设置" }));
    fireEvent.click(await screen.findByRole("button", { name: "撤销设备" }));
    expect(screen.getByRole("form", { name: "撤销设备确认" })).toHaveTextContent("退出登录");
    expect(calls.some(call => call.command === "logout")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "确认撤销" }));
    await screen.findByTestId("login-route");
    await act(async () => undefined);
    expect(screen.queryByRole("button", { name: "小林 的账户选项" })).not.toBeInTheDocument();
    expect(screen.queryByText("会话已撤销")).not.toBeInTheDocument();
    expect(calls.filter(call => call.command === "logout")).toEqual([{ command: "logout", args: undefined }]);
    const revokeIndex = calls.findIndex(call => call.args?.path === "/api/v1/me/devices/d/revoke");
    expect(calls[revokeIndex]).toEqual({ command: "api_request", args: { method: "POST", path: "/api/v1/me/devices/d/revoke", body: {} } });
    expect(calls.findIndex(call => call.command === "logout")).toBeGreaterThan(revokeIndex);
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { AuthProvider, LoginScreen, useAuth, type Session } from "../src/auth";
import { AppShell } from "../src/components/AppShell";
import { createNativeTransport, normalizeServerUrl, RestClient, type NativeTransport } from "../src/api/rest";
import type { Invoke, InvokeArgs } from "../src/lib/tauri";

const session: Session = {
  id: "session-1", accessToken: "access-1", refreshToken: "refresh-1",
  expiresAt: "2030-01-01T00:00:00Z", deviceId: "device-1",
  user: { id: "user-1", username: "li", email: "li@example.test", roles: ["employee"], disabled: false, mustChangePassword: false },
};

function fillLogin(serverUrl = "https://files.example.test") {
  fireEvent.change(screen.getByLabelText("服务器地址"), { target: { value: serverUrl } });
  fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "li" } });
  fireEvent.change(screen.getByLabelText("密码"), { target: { value: "secret" } });
}

function MemoryStorage() {
  const data = new Map<string, string>();
  return { data, getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => { data.set(key, value); } };
}

function AuthConsumer() {
  const auth = useAuth();
  return <>
    <LoginScreen onLogin={auth.login} initialServerUrl={auth.serverUrl} />
    <output aria-label="当前用户">{auth.session?.user.username ?? "未登录"}</output>
    <button onClick={() => { void auth.logout().catch(() => undefined); }}>退出登录</button>
  </>;
}

describe("server address policy", () => {
  it.each([
    ["files.example.test:8443/", "https://files.example.test:8443"],
    [" https://files.example.test/ ", "https://files.example.test"],
    ["http://localhost:8080", "http://localhost:8080"],
    ["http://127.0.0.1:8080", "http://127.0.0.1:8080"],
    ["http://[::1]:8080", "http://[::1]:8080"],
  ])("normalizes %s", (input, expected) => { expect(normalizeServerUrl(input)).toBe(expected); });

  it.each(["http://192.168.1.9:8080", "http://localhost.evil.test", "https://me:secret@files.test", "file:///tmp/data", "https://files.test/path", "https://files.test?token=secret"])("rejects unsafe or ambiguous address %s", (input) => {
    expect(() => normalizeServerUrl(input)).toThrow();
  });
});

describe("native request boundary", () => {
  it("calls the specified native commands without putting tokens in generic requests", async () => {
    const calls: Array<{ command: string; args: InvokeArgs }> = [];
    const invoke = (async <T,>(command: string, args?: InvokeArgs) => {
      calls.push({ command, args });
      if (command === "login") return session as T;
      if (command === "api_request") return { id: "created-1" } as T;
      return undefined as T;
    }) satisfies Invoke;
    const transport = createNativeTransport(invoke);
    expect(await transport.login({ serverUrl: "https://files.test", username: "li", password: "secret", deviceId: "known-device" })).toEqual(session);
    const client = new RestClient(transport);
    expect(await client.request("POST", "/api/v1/users", { username: "new", password: "temporary", roles: ["employee"] })).toEqual({ id: "created-1" });
    await transport.logout();
    expect(calls).toEqual([
      { command: "login", args: { serverUrl: "https://files.test", username: "li", password: "secret", deviceId: "known-device" } },
      { command: "api_request", args: { method: "POST", path: "/api/v1/users", body: { username: "new", password: "temporary", roles: ["employee"] } } },
      { command: "logout", args: undefined },
    ]);
  });

  it("extracts the server error body and fails honestly outside desktop", async () => {
    const transport = createNativeTransport(async () => { throw { error: "账号或密码错误" }; });
    await expect(transport.login({ serverUrl: "https://files.test", username: "li", password: "secret" })).rejects.toThrow("账号或密码错误");
    await expect(createNativeTransport().login({ serverUrl: "https://files.test", username: "li", password: "secret" })).rejects.toThrow(/桌面/);
  });
});

describe("LoginScreen", () => {
  it("does not show an error summary when editing pristine fields", () => {
    render(<LoginScreen onLogin={async () => undefined} />);
    for (const label of ["服务器地址", "用户名", "密码"]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value: "a" } });
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    }
  });

  it("removes only the corrected field from the error summary, then dismisses an empty summary", () => {
    render(<LoginScreen onLogin={async () => undefined} />);
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(within(screen.getByRole("alert")).getAllByRole("link")).toHaveLength(3);

    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "li" } });

    const remaining = within(screen.getByRole("alert"));
    expect(remaining.queryByRole("link", { name: "请输入用户名" })).not.toBeInTheDocument();
    expect(remaining.getAllByRole("link")).toHaveLength(2);
    expect(remaining.getByRole("link", { name: "请输入服务器地址" })).toBeInTheDocument();
    expect(remaining.getByRole("link", { name: "请输入密码" })).toBeInTheDocument();
    expect(screen.getByLabelText("用户名")).toHaveAttribute("aria-invalid", "false");
    expect(screen.getByLabelText("密码")).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(screen.getByLabelText("服务器地址"), { target: { value: "https://files.example.test" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "secret" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("validates fields inline, links its focused summary, and never submits empty fields", async () => {
    const onLogin = vi.fn();
    render(<LoginScreen onLogin={onLogin} />);
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    const summary = await screen.findByRole("alert");
    expect(summary).toHaveFocus();
    expect(screen.getByLabelText("服务器地址")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("用户名")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("密码")).toHaveAttribute("aria-invalid", "true");
    fireEvent.click(screen.getByRole("link", { name: "请输入用户名" }));
    expect(screen.getByLabelText("用户名")).toHaveFocus();
    expect(onLogin).not.toHaveBeenCalled();
  });

  it("normalizes the server and username, preserves password whitespace, and displays API errors", async () => {
    const onLogin = vi.fn().mockRejectedValue(new Error("账号或密码错误"));
    render(<LoginScreen onLogin={onLogin} />);
    fillLogin("  files.example.test/  ");
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: " li " } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: " secret " } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(onLogin).toHaveBeenCalledWith({ serverUrl: "https://files.example.test", username: "li", password: " secret " }));
    expect(await screen.findByRole("alert")).toHaveTextContent("账号或密码错误");
    expect(screen.getByLabelText("密码")).toHaveValue(" secret ");
  });

  it("prevents duplicate login while pending and clears password after success", async () => {
    let finish!: () => void;
    const onLogin = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    render(<LoginScreen onLogin={onLogin} />);
    fillLogin();
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(screen.getByRole("button", { name: "正在登录…" })).toBeDisabled();
    expect(screen.getByLabelText("密码")).toBeDisabled();
    fireEvent.submit(screen.getByRole("form", { name: "员工登录" }));
    expect(onLogin).toHaveBeenCalledTimes(1);
    finish();
    await waitFor(() => expect(screen.getByLabelText("密码")).toHaveValue(""));
  });
});

describe("AuthProvider", () => {
  it("stores only server URL and device id, and clears the in-memory session after logout", async () => {
    const storage = MemoryStorage();
    const login = vi.fn().mockResolvedValue(session);
    const logout = vi.fn().mockResolvedValue(undefined);
    const transport: NativeTransport = { login, logout, request: async <T,>() => undefined as T };
    render(<AuthProvider storage={storage} transport={transport}><AuthConsumer /></AuthProvider>);
    fillLogin();
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(screen.getByLabelText("当前用户")).toHaveTextContent("li"));
    expect([...storage.data.entries()]).toEqual([["linknestx.serverUrl", "https://files.example.test"], ["linknestx.deviceId", "device-1"]]);
    fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
    await waitFor(() => expect(screen.getByLabelText("当前用户")).toHaveTextContent("未登录"));
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("reuses the saved device id and keeps a failed login unauthenticated", async () => {
    const storage = MemoryStorage();
    storage.setItem("linknestx.deviceId", "existing-device");
    const login = vi.fn().mockRejectedValue({ error: "账号已禁用" });
    const transport: NativeTransport = { login, logout: async () => undefined, request: async <T,>() => undefined as T };
    render(<AuthProvider storage={storage} transport={transport}><AuthConsumer /></AuthProvider>);
    fillLogin();
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("账号已禁用");
    expect(login).toHaveBeenCalledWith({ serverUrl: "https://files.example.test", username: "li", password: "secret", deviceId: "existing-device" });
    expect(screen.getByLabelText("当前用户")).toHaveTextContent("未登录");
    expect(storage.data.size).toBe(1);
  });
});

describe("AppShell", () => {
  it("navigates Chinese destinations, exposes real connection state, and hides management from employees", () => {
    function Shell({ roles = ["employee"] }: { roles?: string[] }) {
      const [selected, setSelected] = useState<"groups" | "pool" | "transfers" | "admin">("groups");
      return <AppShell user={{ username: "li", roles }} selected={selected} onNavigate={setSelected} connection={{ status: "connected", label: "已连接到公司服务器" }} activeTransfers={2}>
        <p>工作区内容</p>
      </AppShell>;
    }
    const { rerender } = render(<Shell />);
    expect(screen.getByRole("navigation", { name: "主导航" })).toHaveTextContent("群组");
    expect(screen.queryByRole("button", { name: "管理中心" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "公共文件池" }));
    expect(screen.getByRole("button", { name: "公共文件池" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("heading", { name: "公共文件池" })).toBeInTheDocument();
    expect(screen.getByText("已连接到公司服务器")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "2 项传输进行中" })).toBeInTheDocument();
    rerender(<Shell roles={["admin"]} />);
    expect(screen.getByRole("button", { name: "管理中心" })).toBeInTheDocument();
  });

  it("provides a keyboard-operable account disclosure with settings and logout callbacks", () => {
    const settings = vi.fn();
    const logout = vi.fn();
    render(<AppShell user={{ username: "li", roles: ["employee"] }} selected="groups" onNavigate={() => undefined} connection={{ status: "disconnected" }} onSettings={settings} onLogout={logout}><p>内容</p></AppShell>);
    const account = screen.getByRole("button", { name: "li 的账户选项" });
    fireEvent.click(account);
    fireEvent.click(screen.getByRole("button", { name: "设置" }));
    expect(settings).toHaveBeenCalledTimes(1);
    expect(account).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(account);
    fireEvent.keyDown(screen.getByRole("button", { name: "退出登录" }), { key: "Escape" });
    expect(account).toHaveFocus();
    fireEvent.click(account);
    fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
    expect(logout).toHaveBeenCalledTimes(1);
  });
});

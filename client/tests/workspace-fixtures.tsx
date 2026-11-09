import { act, fireEvent, render, screen } from "@testing-library/react";
import { App } from "../src/main";
import type { NativeTransport } from "../src/api/rest";
export const user = { id: "u1", username: "小林", email: "", roles: ["employee"], disabled: false, mustChangePassword: false };
export const msg = (id: string, sequence: number, body: string, groupId = "g1") => ({ id, sequence, body, groupId, senderId: "u2", clientMessageId: `client-${id}`, createdAt: "2026-11-09T01:00:00Z", cursor: `cursor-${sequence}` });
export function harness(options: {
  admin?: boolean;
  mustChangePassword?: boolean;
  request?: (method: string, path: string, body?: any) => Promise<any>;
  command?: (name: string, args?: any) => Promise<any>;
} = {}) {
  const listeners = new Map<string, Set<(value: any) => void>>();
  const request = vi.fn(async (method: string, path: string, body?: any) => {
    const custom = await options.request?.(method, path, body);
    if (custom !== undefined)
      return custom;
    if (path === "/api/v1/groups")
      return [{ id: "g1", name: "研发协作", createdBy: "u1", createdAt: "2026-11-09" }, { id: "g2", name: "设计讨论", createdBy: "u2", createdAt: "2026-11-09" }];
    if (path.endsWith("/members"))
      return [{ userId: "u1", role: "member", joinedAt: "2026-11-09" }];
    if (path.includes("/messages?"))
      return { messages: [], nextCursor: "initial", hasMore: false };
    if (path.includes("/files?"))
      return [];
    if (path === "/api/v1/admin/users" || path === "/api/v1/users")
      return [user];
    throw new Error(`未提供响应 ${method} ${path}`);
  });
  const transport: NativeTransport = { login: vi.fn(async () => ({ id: "s", accessToken: "a", refreshToken: "r", expiresAt: "", deviceId: "d", user: { ...user, roles: options.admin ? ["admin"] : user.roles, mustChangePassword: !!options.mustChangePassword } })), logout: vi.fn(async () => {
    }), request };
  const invoke = vi.fn(async (name: string, args?: any) => {
    const custom = await options.command?.(name, args);
    if (custom !== undefined)
      return custom;
    if (name === "list_transfers")
      return [];
    throw new Error(`命令不可用 ${name}`);
  });
  const listen = vi.fn(async (name: string, callback: (payload: any) => void) => {
    if (!listeners.has(name))
      listeners.set(name, new Set());
    listeners.get(name)!.add(callback);
    return () => {
      listeners.get(name)!.delete(callback);
    };
  });
  const view = render(<App transport={transport} desktop={{ invoke, listen }} storage={{ getItem: () => null, setItem: () => {
      } }}/>);
  const login = async () => {
    fireEvent.change(screen.getByLabelText("服务器地址"), { target: { value: "https://files.example.com" } });
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "小林" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "password" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByRole(options.mustChangePassword ? "heading" : "button", { name: options.mustChangePassword ? "设置新密码" : "研发协作" });
  };
  const emit = async (name: string, payload: any) => {
    await act(async () => {
      listeners.get(name)?.forEach(fn => fn(payload));
    });
  };
  return { view, login, emit, request, invoke, listeners };
}

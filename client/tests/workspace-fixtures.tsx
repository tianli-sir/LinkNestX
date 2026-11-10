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
    if (path === "/api/v1/conversations") return [{id:"g1",kind:"group",title:"研发协作",canSend:true,memberCount:2,unreadCount:0},{id:"g2",kind:"group",title:"设计讨论",canSend:true,memberCount:2,unreadCount:0}];
    if (path === "/api/v1/contacts") return {friends:[],incoming:[],outgoing:[]};
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
    if (method === "GET" && path === "/api/v1/me/devices")
      return [];
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
    if (name === "get_transfer_preferences") return {maxConcurrent:3,bandwidthLimitBps:0,autostart:false};
    if (["discard_attachment", "get_update_status"].includes(name)) return null;
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
  const login = async (management = false) => {
    fireEvent.change(screen.getByLabelText("账号"), { target: { value: "小林" } });
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "password" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    if(management){fireEvent.click(await screen.findByRole("button",{name:"群组与成员管理"}));return;}
    await screen.findByRole(options.mustChangePassword ? "heading" : "button", { name: options.mustChangePassword ? "设置新密码" : "研发协作" });
    if(!options.mustChangePassword) fireEvent.click(screen.getByRole("button",{name:"研发协作"}));
  };
  const emit = async (name: string, payload: any) => {
    await act(async () => {
      listeners.get(name)?.forEach(fn => fn(payload));
    });
  };
  return { view, login, emit, request, invoke, listeners };
}

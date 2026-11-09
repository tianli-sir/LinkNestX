import { invoke, isTauri, type Invoke } from "../lib/tauri";
import type { LoginInput, Session } from "../auth/types";

export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

/** Native code owns authorization headers, refresh and secure session storage. */
export interface ApiTransport {
  request<T>(method: HttpMethod, path: string, body?: unknown): Promise<T>;
}

export interface NativeTransport extends ApiTransport {
  login(input: LoginInput): Promise<Session>;
  logout(): Promise<void>;
}

export class ApiError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ApiError";
  }
}

/** Extract the server's public error; never stringify arbitrary response data. */
export function apiErrorMessage(error: unknown, fallback = "请求失败，请稍后重试"): string {
  if (error && typeof error === "object" && "error" in error && typeof error.error === "string") {
    return error.error;
  }
  if (error instanceof Error) return error.message || fallback;
  if (typeof error === "string") {
    try {
      const parsed: unknown = JSON.parse(error);
      if (parsed && typeof parsed === "object" && "error" in parsed && typeof parsed.error === "string") return parsed.error;
    } catch { /* Native commands may reject with plain text. */ }
    return error || fallback;
  }
  return fallback;
}

/** Use a server origin, preventing credentials, query strings and hidden path prefixes. */
export function normalizeServerUrl(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) throw new ApiError("请输入服务器地址");
  let url: URL;
  try {
    url = new URL(/^[a-z][a-z\d+.-]*:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`);
  } catch {
    throw new ApiError("请输入有效的服务器地址，例如 https://files.example.com");
  }
  const loopback = url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "[::1]";
  if (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) {
    throw new ApiError("服务器需要使用 HTTPS；HTTP 仅支持本机开发地址");
  }
  if (!url.hostname || url.username || url.password || url.search || url.hash || url.pathname !== "/") {
    throw new ApiError("请填写服务器地址，不包含账号、路径或查询参数");
  }
  return url.origin;
}

const desktopInvoke: Invoke = async <T>(command: string, args?: Record<string, unknown>) => {
  if (!isTauri()) throw new ApiError("请在 LinkNestX 桌面应用中登录；浏览器预览无法连接工作区");
  return invoke<T>(command, args);
};

export function createNativeTransport(invokeCommand: Invoke = desktopInvoke): NativeTransport {
  const call = async <T>(command: string, args?: Record<string, unknown>): Promise<T> => {
    try {
      return await invokeCommand<T>(command, args);
    } catch (error) {
      throw new ApiError(apiErrorMessage(error));
    }
  };
  return {
    request: <T>(method: HttpMethod, path: string, body?: unknown) => call<T>("api_request", { method, path, body: body ?? null }),
    login: (input) => call<Session>("login", { ...input, serverUrl: normalizeServerUrl(input.serverUrl) }),
    logout: () => call<void>("logout"),
  };
}

export class RestClient {
  constructor(private readonly transport: ApiTransport = createNativeTransport()) {}

  async request<T>(method: HttpMethod, path: string, body?: unknown): Promise<T> {
    if (!path.startsWith("/api/v1/") || path.includes("://") || path.includes("\\") || path.includes("#")) {
      throw new ApiError("请求路径无效");
    }
    try {
      return await this.transport.request<T>(method, path, body);
    } catch (error) {
      throw new ApiError(apiErrorMessage(error));
    }
  }
}

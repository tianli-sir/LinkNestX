import { invoke, isDesktop, type Invoke } from "../lib/tauri";
import type { LoginInput, Session } from "../auth/types";

export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

/** Native code owns authorization headers, refresh and secure session storage. */
export interface ApiTransport {
  request<T>(method: HttpMethod, path: string, body?: unknown): Promise<T>;
}

export interface NativeTransport extends ApiTransport {
  login(input: LoginInput): Promise<Session>;
  logout(): Promise<void>;
  restore?(): Promise<Session>;
  refresh?(): Promise<Session>;
}

export class ApiError extends Error {
  constructor(message: string, public readonly status?: number) {
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
  const octets = /^\d+\.\d+\.\d+\.\d+$/.test(url.hostname) ? url.hostname.split(".").map(Number) : [];
  const privateIpv4 = octets.length === 4 && octets.every((octet) => octet >= 0 && octet <= 255)
    && (octets[0] === 10 || (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31) || (octets[0] === 192 && octets[1] === 168));
  if (url.protocol !== "https:" && !(url.protocol === "http:" && (loopback || privateIpv4))) {
    throw new ApiError("公网地址需使用 HTTPS；局域网私有 IPv4 或本机地址可使用 HTTP");
  }
  if (!url.hostname || url.username || url.password || url.search || url.hash || url.pathname !== "/") {
    throw new ApiError("请填写服务器地址，不包含账号、路径或查询参数");
  }
  return url.origin;
}

const desktopInvoke: Invoke = async <T>(command: string, args?: Record<string, unknown>) => {
  if (!isDesktop()) throw new ApiError("请在 LinkNestX 桌面应用中登录；浏览器预览无法连接工作区");
  return invoke<T>(command, args);
};

export function createNativeTransport(invokeCommand: Invoke = desktopInvoke): NativeTransport {
  const call = async <T>(command: string, args?: Record<string, unknown>): Promise<T> => {
    try {
      return await invokeCommand<T>(command, args);
    } catch (error) {
      throw new ApiError(apiErrorMessage(error), (error as { status?: number })?.status ?? undefined);
    }
  };
  return {
    request: <T>(method: HttpMethod, path: string, body?: unknown) => call<T>("api_request", { method, path, body: body ?? null }),
    login: ({ username, password, deviceId }) => call<Session>("login", { username, password, ...(deviceId ? { deviceId } : {}) }),
    restore: () => call<Session>("session_restore"),
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
      throw new ApiError(apiErrorMessage(error), (error as { status?: number })?.status ?? undefined);
    }
  }
}

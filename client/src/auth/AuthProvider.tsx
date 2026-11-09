import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { apiErrorMessage, ApiError, createNativeTransport, normalizeServerUrl, RestClient, type NativeTransport } from "../api/rest";
import type { LoginInput, Session } from "./types";

export interface PreferenceStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export interface AuthContextValue {
  session: Session | null;
  serverUrl: string;
  client: RestClient;
  login(input: LoginInput): Promise<void>;
  logout(): Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);
const SERVER_KEY = "linknestx.serverUrl";
const DEVICE_KEY = "linknestx.deviceId";

function browserStorage(): PreferenceStorage | undefined {
  try { return typeof window !== "undefined" ? window.localStorage : undefined; }
  catch { return undefined; }
}
function readPreference(storage: PreferenceStorage | undefined, key: string): string {
  try { return storage?.getItem(key) ?? ""; }
  catch { return ""; }
}
function savePreference(storage: PreferenceStorage | undefined, key: string, value: string): void {
  try { storage?.setItem(key, value); }
  catch { /* A blocked preference store must not invalidate a successful login. */ }
}

export interface AuthProviderProps {
  children: ReactNode;
  transport?: NativeTransport;
  storage?: PreferenceStorage;
}

export function AuthProvider({ children, transport, storage }: AuthProviderProps) {
  const nativeTransport = useMemo(() => transport ?? createNativeTransport(), [transport]);
  const preferences = useMemo(() => storage ?? browserStorage(), [storage]);
  const client = useMemo(() => new RestClient(nativeTransport), [nativeTransport]);
  const [session, setSession] = useState<Session | null>(null);
  const [serverUrl, setServerUrl] = useState(() => readPreference(preferences, SERVER_KEY));
  const operation = useRef(0);

  const login = useCallback(async (input: LoginInput): Promise<void> => {
    const generation = ++operation.current;
    const normalized = normalizeServerUrl(input.serverUrl);
    const deviceId = input.deviceId || readPreference(preferences, DEVICE_KEY) || undefined;
    try {
      const result = await nativeTransport.login({ ...input, serverUrl: normalized, ...(deviceId ? { deviceId } : {}) });
      if (generation !== operation.current) return;
      setSession(result);
      setServerUrl(normalized);
      savePreference(preferences, SERVER_KEY, normalized);
      savePreference(preferences, DEVICE_KEY, result.deviceId);
    } catch (error) {
      throw new ApiError(apiErrorMessage(error, "登录失败，请重试"));
    }
  }, [nativeTransport, preferences]);

  const logout = useCallback(async () => {
    ++operation.current;
    // Remove the visible session immediately, including when remote revocation fails.
    setSession(null);
    try { await nativeTransport.logout(); }
    catch (error) { throw new ApiError(apiErrorMessage(error, "退出登录失败，请重试")); }
  }, [nativeTransport]);

  const value = useMemo(() => ({ session, serverUrl, client, login, logout }), [session, serverUrl, client, login, logout]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (!context) throw new Error("useAuth must be used inside AuthProvider");
  return context;
}

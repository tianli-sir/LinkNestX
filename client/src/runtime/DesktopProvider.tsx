import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { listen } from "@tauri-apps/api/event";
import { invoke, isTauri, type Invoke } from "../lib/tauri";
import type { ConnectionState } from "../components/AppShell";
import { apiErrorMessage } from "../api/rest";
export interface DesktopAdapter {
  invoke: Invoke;
  listen(event: string, callback: (payload: any) => void): Promise<() => void>;
}
const nativeDesktop: DesktopAdapter = { invoke, listen: async (event, callback) => {
    if (!isTauri())
      throw new Error("实时连接仅在桌面应用中可用");
    return listen(event, e => callback(e.payload));
  } };
interface DesktopContextValue {
  desktop: DesktopAdapter;
  connection: ConnectionState;
  subscribe: (event: string, callback: (payload: any) => void) => () => void;
}
const Context = createContext<DesktopContextValue | null>(null);
export function DesktopProvider({ children, adapter = nativeDesktop }: {
  children: ReactNode;
  adapter?: DesktopAdapter;
}) {
  const [connection, setConnection] = useState<ConnectionState>({ status: "connecting" });
  const subscribers = useRef(new Map<string, Set<(payload: any) => void>>());
  const subscribe = useCallback((event: string, callback: (payload: any) => void) => {
    let set = subscribers.current.get(event);
    if (!set) {
      set = new Set();
      subscribers.current.set(event, set);
    }
    set.add(callback);
    return () => {
      set!.delete(callback);
    };
  }, []);
  useEffect(() => {
    let stopped = false;
    const removers: Array<() => void> = [];
    for (const name of ["server.event", "connection.changed", "transfer.updated"]) {
      adapter.listen(name, payload => {
        if (stopped)
          return;
        if (name === "connection.changed" && ["connected", "connecting", "disconnected", "error"].includes(payload?.status))
          setConnection({ status: payload.status });
        if (name === "server.event" && payload?.type === "session.ready")
          setConnection({ status: "connected" });
        subscribers.current.get(name)?.forEach(callback => callback(payload));
      }).then(remove => {
        if (stopped)
          remove();
        else
          removers.push(remove);
      }).catch(error => {
        if (!stopped)
          setConnection({ status: "error", label: apiErrorMessage(error) });
      });
    }
    return () => {
      stopped = true;
      removers.forEach(remove => remove());
    };
  }, [adapter]);
  const value = useMemo(() => ({ desktop: adapter, connection, subscribe }), [adapter, connection, subscribe]);
  return <Context.Provider value={value}>{children}</Context.Provider>;
}
export function useDesktop() {
  const value = useContext(Context);
  if (!value)
    throw new Error("DesktopProvider missing");
  return value;
}
export function useDesktopEvent(event: string, callback: (payload: any) => void) {
  const { subscribe } = useDesktop();
  const current = useRef(callback);
  current.current = callback;
  useEffect(() => subscribe(event, payload => current.current(payload)), [event, subscribe]);
}

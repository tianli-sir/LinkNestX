import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { listen } from "@tauri-apps/api/event";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { invoke, isTauri, type Invoke } from "../lib/tauri";
import type { ConnectionState } from "../components/AppShell";
import { browserCapabilities, nativeCapabilities } from "./capabilities";
import { createBrowserAdapter } from "./browserAdapter";
import { apiErrorMessage } from "../api/rest";
export interface DesktopAdapter {
  kind?: "browser" | "native";
  invoke: Invoke;
  listen(event: string, callback: (payload: any) => void): Promise<() => void>;
}
const nativeDesktop: DesktopAdapter = { invoke, listen: async (event, callback) => {
    if (!isTauri())
      throw new Error("实时连接仅在桌面应用中可用");
    if(event === "window.foreground") {
      const current=getCurrentWindow();let stopped=false;let revision=0;
      callback(false);
      const refresh=async()=>{const run=++revision;try{const [focused,minimized]=await Promise.all([current.isFocused(),current.isMinimized()]);if(!stopped&&run===revision)callback(focused&&!minimized)}catch{if(!stopped&&run===revision)callback(false)}};
      const unfocus=await current.onFocusChanged(event=>{if(!event.payload){revision++;callback(false)}else void refresh()});
      let unresize:()=>void;try{unresize=await current.onResized(()=>void refresh())}catch(error){unfocus();throw error}
      void refresh();return()=>{stopped=true;revision++;unfocus();unresize()};
    }
    // Logical event names stay dotted; Tauri's event channels do not allow dots.
    return listen(event.replaceAll(".", ":"), e => callback(e.payload));
  } };
export interface BackgroundStatus {sharingEnabled:boolean;activeUploads:number;error?:string|null;lastCompletion?:string|null}
interface DesktopContextValue {
  foreground: boolean;
  capabilities: typeof nativeCapabilities;
  background:BackgroundStatus;
  desktop: DesktopAdapter;
  connection: ConnectionState;
  subscribe: (event: string, callback: (payload: any) => void) => () => void;
}
const Context = createContext<DesktopContextValue | null>(null);
export function DesktopProvider({ children, adapter: suppliedAdapter }: {
  children: ReactNode;
  adapter?: DesktopAdapter;
}) {
  const adapter = useMemo(() => suppliedAdapter ?? (isTauri() ? nativeDesktop : createBrowserAdapter()), [suppliedAdapter]);
  const [foreground,setForeground]=useState(!isTauri());
  const [connection, setConnection] = useState<ConnectionState>({ status: "connecting" });
  const [background,setBackground]=useState<BackgroundStatus>({sharingEnabled:false,activeUploads:0});
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
    let backgroundEvents=0;
    for (const name of ["server.event", "connection.changed", "transfer.updated","service.error","sharing.changed","sharing.revoked","sharing.activity","peer.sent","service.status", "attachments.prepared", "update.status", "window.foreground"]) {
      adapter.listen(name, payload => {
        if (stopped)
          return;
        if (name === "window.foreground") setForeground(payload===true);
        if (name === "connection.changed" && ["connected", "connecting", "disconnected", "error"].includes(payload?.status))
          setConnection({ status: payload.status });
        if (name === "server.event" && payload?.type === "session.ready")
          setConnection({ status: "connected" });
        if(["service.status","service.error","sharing.changed","sharing.activity"].includes(name))backgroundEvents++;
        if(name==="service.status"&&payload) setBackground(payload);
        if(name==="service.error")setBackground(old=>({...old,error:apiErrorMessage(payload)}));
        if(name==="sharing.changed")setBackground(old=>({...old,sharingEnabled:!!payload.enabled,...(!payload.enabled?{activeUploads:0}:{})}));
        if(name==="sharing.activity")setBackground(old=>({...old,activeUploads:Math.min(32,Math.max(0,payload.activeUploads??0))}));
        subscribers.current.get(name)?.forEach(callback => callback(payload));
      }).then(remove => {
        if (stopped)
          remove();
        else
          removers.push(remove);
      }).catch(error => {
        if(name === "window.foreground"){if(!stopped)setForeground(false);return;}
        if (!stopped)
          setConnection({ status: "error", label: apiErrorMessage(error) });
      });
    }
    adapter.invoke<BackgroundStatus>("get_background_status").then(status=>{if(!stopped&&status&&backgroundEvents===0)setBackground(status)}).catch(()=>{});
    return () => {
      stopped = true;
      removers.forEach(remove => remove());
    };
  }, [adapter]);
  const value = useMemo(() => ({ desktop: adapter, connection, subscribe,background, foreground, capabilities: adapter.kind === "browser" ? browserCapabilities : nativeCapabilities }), [adapter, connection, subscribe,background,foreground]);
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

/** The small native boundary used by the web shell and feature modules. */
export type InvokeArgs = Record<string, unknown> | undefined;
export type Invoke = <T>(command: string, args?: InvokeArgs) => Promise<T>;

declare global {
  interface Window {
    __LINKNESTX_DESKTOP__?: {
      kind: "legacy";
      invoke: Invoke;
      listen(event: string, callback: (payload: unknown) => void): Promise<() => void>;
    };
    __TAURI__?: {
      core?: {
        invoke?: Invoke;
      };
    };
  }
}

/**
 * Calls a Tauri command when running in the desktop shell. Browser previews
 * fail clearly so feature code never reaches into Tauri globals directly.
 */
export const invoke: Invoke = async <T>(command: string, args?: InvokeArgs) => {
  const tauriInvoke = window.__LINKNESTX_DESKTOP__?.invoke ?? window.__TAURI__?.core?.invoke;
  if (!tauriInvoke) {
    throw new Error(`Tauri command unavailable: ${command}`);
  }
  return tauriInvoke<T>(command, args);
};

export const isTauri = (): boolean => Boolean(window.__TAURI__?.core?.invoke);
export const isDesktop = (): boolean => Boolean(window.__LINKNESTX_DESKTOP__?.invoke) || isTauri();

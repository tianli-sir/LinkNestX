import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import type { NativeTransport } from "./api/rest";
import { AuthProvider, type PreferenceStorage } from "./auth";
import { WorkspaceRouter } from "./features/WorkspaceRouter";
import { DesktopProvider, type DesktopAdapter } from "./runtime/DesktopProvider";
import "./styles.css";
import "./styles/features.css";
const DEFAULT_VERSION = "0.1.0";
declare global {
  interface Window {
    __APP_VERSION__: string;
  }
}
export interface AppWindow extends Window {
}
window.__APP_VERSION__ = window.__APP_VERSION__ || import.meta.env.VITE_APP_VERSION || DEFAULT_VERSION;
export interface AppProps {
  transport?: NativeTransport;
  desktop?: DesktopAdapter;
  storage?: PreferenceStorage;
}
export function App({ transport, desktop, storage }: AppProps = {}) {
  return <DesktopProvider adapter={desktop}><AuthProvider transport={transport} storage={storage}><WorkspaceRouter /></AuthProvider></DesktopProvider>;
}
const root = document.getElementById("root");
if (root)
  createRoot(root).render(<StrictMode><App /></StrictMode>);

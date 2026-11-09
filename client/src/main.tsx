import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

const DEFAULT_VERSION = "0.1.0";

declare global {
  interface Window {
    __APP_VERSION__: string;
  }
}

export interface AppWindow extends Window {}

const appWindow = window as AppWindow;
appWindow.__APP_VERSION__ =
  appWindow.__APP_VERSION__ || import.meta.env.VITE_APP_VERSION || DEFAULT_VERSION;

export function App() {
  return (
    <main aria-labelledby="app-title" className="app-shell">
      <header>
        <p className="eyebrow">LinkNestX</p>
        <h1 id="app-title">Sign in to LinkNestX</h1>
      </header>
      <section aria-labelledby="login-title" data-testid="login-route" className="login-placeholder">
        <h2 id="login-title">Login</h2>
        <p>The secure employee login screen will appear here.</p>
      </section>
      <footer data-testid="app-version">Version {appWindow.__APP_VERSION__}</footer>
    </main>
  );
}

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}

import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { ThemeControl } from "../runtime/ThemeProvider";
import { Icon, type IconName } from "./Icon";
import { AccountNumber } from "./AccountNumber";
import "../styles/desktop.css";

export type NavigationKey = "groups" | "documents" | "contacts" | "pool" | "transfers" | "admin";
export interface ShellUser { username: string; roles: string[]; accountNumber?: string }
export interface ConnectionState {
  status: "connected" | "connecting" | "disconnected" | "error";
  label?: string;
}
export interface AppShellProps {
  children: ReactNode;
  user: ShellUser;
  selected: NavigationKey;
  onNavigate(destination: NavigationKey): void;
  connection: ConnectionState;
  activeTransfers?: number;
  transferLabel?: string;
  title?: string;
  headerActions?: ReactNode;
  canManage?: boolean;
  onSettings?(): void;
  onLogout?(): void;
}
export const workspaceModules: Array<{ key: NavigationKey; label: string; icon: IconName }> = [
  { key: "groups", label: "消息", icon: "chat" },
  { key: "documents", label: "文档", icon: "documents" },
  { key: "pool", label: "文件", icon: "folder" },
  { key: "contacts", label: "联系人", icon: "groups" },
  { key: "transfers", label: "传输", icon: "transfers" },
  { key: "admin", label: "管理", icon: "admin" },
];
const connectionLabels: Record<ConnectionState["status"], string> = {
  connected: "已连接", connecting: "正在连接", disconnected: "未连接", error: "连接异常",
};

export function AppShell({ children, user, selected, onNavigate, connection, activeTransfers = 0, transferLabel, title, headerActions, canManage: managementAllowed = true, onSettings, onLogout }: AppShellProps) {
  const id = useId();
  const [accountOpen, setAccountOpen] = useState(false);
  const accountButton = useRef<HTMLButtonElement>(null);
  const accountArea = useRef<HTMLDivElement>(null);
  const workspace = useRef<HTMLElement>(null);
  const previousPage = useRef(selected);
  const canManage = user.roles.includes("admin") && managementAllowed;
  const availableNavigation = workspaceModules.filter((item) => item.key !== "admin" || canManage);
  const currentTitle = title ?? availableNavigation.find((item) => item.key === selected)?.label ?? "工作区";
  const transfers = Math.max(0, Math.floor(activeTransfers));

  useEffect(() => {
    if (previousPage.current !== selected) workspace.current?.focus();
    previousPage.current = selected;
  }, [selected]);
  useEffect(() => {
    if (!accountOpen) return;
    const closeOutside = (event: PointerEvent) => {
      if (!accountArea.current?.contains(event.target as Node)) setAccountOpen(false);
    };
    document.addEventListener("pointerdown", closeOutside);
    return () => document.removeEventListener("pointerdown", closeOutside);
  }, [accountOpen]);

  function accountAction(action: (() => void) | undefined) {
    setAccountOpen(false);
    accountButton.current?.focus();
    action?.();
  }

  return <div className={`ln-desktop ${selected==="groups"||selected==="contacts"?"is-messaging":""}`}>
    <a href={`#${id}-workspace`} className="ln-skip-link" onClick={(event) => { event.preventDefault(); workspace.current?.focus(); }}>跳至工作区</a>
    <aside className="ln-sidebar" aria-label="工作区侧栏">
      <div className="ln-sidebar-brand" aria-label="LinkNestX"><span className="ln-brand-mark"><Icon name="nest" size={25} /></span></div>
      <nav aria-label="主导航" className="ln-navigation">
        {availableNavigation.map((item) => <button type="button" key={item.key} className="ln-nav-button" aria-label={item.label} aria-current={selected === item.key ? "page" : undefined} onClick={() => onNavigate(item.key)}><Icon name={item.icon} /><span>{item.label}</span>{item.key==="transfers"&&transfers>0&&<b className="im-nav-badge">{transfers}</b>}</button>)}
      </nav>
      <div className="ln-account" ref={accountArea} onKeyDown={(event) => { if (event.key === "Escape" && accountOpen) { event.stopPropagation(); setAccountOpen(false); accountButton.current?.focus(); } }}>
        <button type="button" className="ln-account-button" aria-label={`${user.username} 的账户选项`} aria-expanded={accountOpen} aria-controls={`${id}-account-options`} onClick={() => setAccountOpen((open) => !open)} ref={accountButton}>
          <span className="ln-avatar" aria-hidden="true">{Array.from(user.username)[0]?.toUpperCase() ?? "员"}</span><span className="ln-account-name"><strong>{user.username}</strong><span>{canManage ? "管理员" : "员工"}</span></span><Icon name="chevron" size={16} />
        </button>
        {accountOpen && <div id={`${id}-account-options`} className="ln-account-options" aria-label="账户选项">
          <p>{user.username}<span>{canManage ? "管理员" : "员工"}</span></p>
          <AccountNumber number={user.accountNumber} compact/>
          {onSettings && <button type="button" onClick={() => accountAction(onSettings)}><Icon name="settings" size={18} />设置</button>}
          {onLogout && <button type="button" className="ln-account-logout" onClick={() => accountAction(onLogout)}><Icon name="logout" size={18} />退出登录</button>}
          {!onSettings && !onLogout && <span className="ln-account-note">账户操作由工作区提供</span>}
        </div>}
      </div>
    </aside>
    <div className="ln-workspace">
      <header className="ln-workspace-header"><div className="ln-workspace-brand"><strong>LinkNest<span>X</span></strong><span className="ln-header-divider"/><h1>{currentTitle}</h1><span className="ln-workspace-label">一起，把工作变轻。</span></div><div className="ln-header-actions">{headerActions}<ThemeControl/>{onSettings&&<button className="ln-header-settings" aria-label="工作区设置" onClick={onSettings}><Icon name="settings" size={20}/></button>}</div></header>
      <main className="ln-workspace-content" id={`${id}-workspace`} ref={workspace} tabIndex={-1} aria-label={currentTitle}>{children}</main>
      <footer className="ln-statusbar"><span className={`ln-connection ln-connection-${connection.status}`} role="status"><Icon name={connection.status === "connected" ? "connected" : "disconnected"} size={16} />{connection.label ?? connectionLabels[connection.status]}</span><button type="button" className="ln-transfer-status" onClick={() => onNavigate("transfers")}><Icon name="transfers" size={15} />{transferLabel ?? (transfers > 0 ? `${transfers} 项传输进行中` : "暂无传输任务")}</button></footer>
    </div>
  </div>;
}

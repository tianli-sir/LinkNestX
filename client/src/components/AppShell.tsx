import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Icon, type IconName } from "./Icon";
import "../styles/desktop.css";

export type NavigationKey = "groups" | "pool" | "transfers" | "admin";
export interface ShellUser { username: string; roles: string[] }
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
  onSettings?(): void;
  onLogout?(): void;
}
const navigation: Array<{ key: NavigationKey; label: string; icon: IconName }> = [
  { key: "groups", label: "群组", icon: "groups" },
  { key: "pool", label: "公共文件池", icon: "folder" },
  { key: "transfers", label: "传输任务", icon: "transfers" },
  { key: "admin", label: "管理中心", icon: "admin" },
];
const connectionLabels: Record<ConnectionState["status"], string> = {
  connected: "已连接", connecting: "正在连接", disconnected: "未连接", error: "连接异常",
};

export function AppShell({ children, user, selected, onNavigate, connection, activeTransfers = 0, transferLabel, title, headerActions, onSettings, onLogout }: AppShellProps) {
  const id = useId();
  const [accountOpen, setAccountOpen] = useState(false);
  const accountButton = useRef<HTMLButtonElement>(null);
  const accountArea = useRef<HTMLDivElement>(null);
  const workspace = useRef<HTMLElement>(null);
  const previousPage = useRef(selected);
  const canManage = user.roles.includes("admin");
  const availableNavigation = navigation.filter((item) => item.key !== "admin" || canManage);
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

  return <div className="ln-desktop">
    <a href={`#${id}-workspace`} className="ln-skip-link" onClick={(event) => { event.preventDefault(); workspace.current?.focus(); }}>跳至工作区</a>
    <aside className="ln-sidebar" aria-label="工作区侧栏">
      <div className="ln-sidebar-brand"><span className="ln-brand-mark"><Icon name="nest" size={23} /></span><div><strong>LinkNestX</strong><span>企业协作</span></div></div>
      <nav aria-label="主导航" className="ln-navigation">
        {availableNavigation.map((item) => <button type="button" key={item.key} className="ln-nav-button" aria-current={selected === item.key ? "page" : undefined} onClick={() => onNavigate(item.key)}><Icon name={item.icon} /><span>{item.label}</span></button>)}
      </nav>
      <div className="ln-account" ref={accountArea} onKeyDown={(event) => { if (event.key === "Escape" && accountOpen) { event.stopPropagation(); setAccountOpen(false); accountButton.current?.focus(); } }}>
        <button type="button" className="ln-account-button" aria-label={`${user.username} 的账户选项`} aria-expanded={accountOpen} aria-controls={`${id}-account-options`} onClick={() => setAccountOpen((open) => !open)} ref={accountButton}>
          <span className="ln-avatar" aria-hidden="true">{Array.from(user.username)[0]?.toUpperCase() ?? "员"}</span><span className="ln-account-name"><strong>{user.username}</strong><span>{canManage ? "管理员" : "员工"}</span></span><Icon name="chevron" size={16} />
        </button>
        {accountOpen && <div id={`${id}-account-options`} className="ln-account-options" aria-label="账户选项">
          <p>{user.username}<span>{canManage ? "管理员" : "员工"}</span></p>
          {onSettings && <button type="button" onClick={() => accountAction(onSettings)}><Icon name="settings" size={18} />设置</button>}
          {onLogout && <button type="button" className="ln-account-logout" onClick={() => accountAction(onLogout)}><Icon name="logout" size={18} />退出登录</button>}
          {!onSettings && !onLogout && <span className="ln-account-note">账户操作由工作区提供</span>}
        </div>}
      </div>
    </aside>
    <div className="ln-workspace">
      <header className="ln-workspace-header"><h1>{currentTitle}</h1>{headerActions && <div className="ln-header-actions">{headerActions}</div>}</header>
      <main className="ln-workspace-content" id={`${id}-workspace`} ref={workspace} tabIndex={-1} aria-label={currentTitle}>{children}</main>
      <footer className="ln-statusbar"><span className={`ln-connection ln-connection-${connection.status}`} role="status"><Icon name={connection.status === "connected" ? "connected" : "disconnected"} size={16} />{connection.label ?? connectionLabels[connection.status]}</span><button type="button" className="ln-transfer-status" onClick={() => onNavigate("transfers")}><Icon name="transfers" size={15} />{transferLabel ?? (transfers > 0 ? `${transfers} 项传输进行中` : "暂无传输任务")}</button></footer>
    </div>
  </div>;
}

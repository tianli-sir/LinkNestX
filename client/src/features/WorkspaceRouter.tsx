import { useState, type FormEvent } from "react";
import { apiErrorMessage } from "../api/rest";
import { LoginScreen, useAuth } from "../auth";
import { AppShell, type NavigationKey } from "../components/AppShell";
import { useDesktop } from "../runtime/DesktopProvider";
import { AdminView } from "./AdminView";
import { FilesView } from "./FilesView";
import { GroupsView } from "./GroupsView";
import { SettingsView } from "./SettingsView";
import { TransfersView, activeTransfer, useTransfers } from "./TransfersView";
import { ErrorNotice, Field } from "./shared";
export function WorkspaceRouter() {
  const { session, serverUrl, login, logout } = useAuth();
  const [error, setError] = useState("");
  const leave = async () => {
    try {
      await logout();
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
  };
  if (!session)
    return <><ErrorNotice error={error}/><div data-testid="login-route"><LoginScreen onLogin={async (input) => {
      await login(input);
      setError("");
    }} initialServerUrl={serverUrl} appVersion={window.__APP_VERSION__}/></div></>;
  if (session.user.mustChangePassword)
    return <PasswordChange onLogout={() => void leave()}/>;
  return <Workspace key={session.id} onLogout={() => void leave()}/>;
}
function Workspace({ onLogout }: {
  onLogout: () => void;
}) {
  const { session, client } = useAuth();
  const { connection } = useDesktop();
  const [page, setPage] = useState<NavigationKey>("groups");
  const [settings, setSettings] = useState(false);
  const queue = useTransfers();
  const user = session!.user;
  return <AppShell user={user} selected={page} onNavigate={destination => {
    setPage(destination);
    setSettings(false);
  }} connection={connection} activeTransfers={queue.tasks.filter(activeTransfer).length} transferLabel={queue.loading ? "正在读取传输任务" : queue.error ? "传输任务读取失败" : queue.tasks.length > 0 && !queue.tasks.some(activeTransfer) ? "暂无进行中的传输" : undefined} title={settings ? "设置" : undefined} onSettings={() => setSettings(true)} onLogout={onLogout}>
  {settings ? <SettingsView onClose={() => setSettings(false)}/> : page === "groups" ? <GroupsView client={client} user={user}/> : page === "pool" ? <FilesView client={client} user={user}/> : page === "transfers" ? <TransfersView queue={queue}/> : user.roles.includes("admin") ? <AdminView client={client}/> : null}
 </AppShell>;
}
function PasswordChange({ onLogout }: {
  onLogout: () => void;
}) {
  const { changePassword } = useAuth();
  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy)
      return;
    setError("");
    if (password !== confirm) {
      setError("两次输入的新密码不一致");
      return;
    }
    setBusy(true);
    try {
      await changePassword(current, password);
      setCurrent("");
      setPassword("");
      setConfirm("");
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  return <main className="ln-login"><section className="ln-login-panel"><h1>设置新密码</h1><p className="ln-muted">此账号需要修改密码后才能进入工作区。</p><form onSubmit={submit}><ErrorNotice error={error}/><Field label="当前密码" id="current-password"><input id="current-password" type="password" required autoComplete="current-password" value={current} onChange={event => setCurrent(event.target.value)} disabled={busy}/></Field><Field label="新密码" id="new-password"><input id="new-password" type="password" required minLength={8} autoComplete="new-password" value={password} onChange={event => setPassword(event.target.value)} disabled={busy}/></Field><Field label="确认新密码" id="confirm-password"><input id="confirm-password" type="password" required minLength={8} autoComplete="new-password" value={confirm} onChange={event => setConfirm(event.target.value)} disabled={busy}/></Field><div className="ln-actions"><button className="ln-button ln-button-primary" disabled={busy}>保存新密码</button><button className="ln-button ln-button-secondary" type="button" disabled={busy} onClick={onLogout}>退出登录</button></div></form></section></main>;
}

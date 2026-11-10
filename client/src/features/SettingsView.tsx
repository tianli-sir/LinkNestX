import { useState, type FormEvent } from "react";
import { apiErrorMessage } from "../api/rest";
import { useAuth } from "../auth";
import { useDesktop } from "../runtime/DesktopProvider";
import { ThemeControl } from "../runtime/ThemeProvider";
import { UpdatePanel } from "./UpdatePanel";
import { DevicesView } from "./DevicesView";
import { ErrorNotice, Field, Loading, useResource } from "./shared";
export interface TransferPreferences {
  maxConcurrent: number;
  bandwidthLimitBps: number;
  autostart: boolean;
}
export function SettingsView({ onClose }: {
  onClose: () => void;
}) {
  const { session, client } = useAuth();
  const { desktop, capabilities } = useDesktop();
  const resource = useResource("transfer-preferences", () => capabilities.nativeTransfers ? desktop.invoke<TransferPreferences>("get_transfer_preferences") : Promise.resolve(null));
  return <section className="ln-feature ln-settings"><div className="ln-toolbar"><h2>设置</h2><button className="ln-button ln-button-secondary" onClick={onClose}>返回工作区</button></div><dl className="ln-account-details"><dt>当前账号</dt><dd>{session?.user.displayName ?? session?.user.username}</dd></dl><section className="ln-settings-appearance"><h3>外观</h3><p className="ln-muted">选择舒适的阅读方式，跟随系统会自动切换浅色与深色。</p><ThemeControl/></section><DevicesView client={client}/><UpdatePanel/><ErrorNotice error={resource.error} retry={resource.refresh}/>{resource.loading && <Loading />}{resource.data && <TransferPreferencesForm key={JSON.stringify(resource.data)} initial={resource.data}/>}</section>;
}
function TransferPreferencesForm({ initial }: {
  initial: TransferPreferences;
}) {
  const { desktop } = useDesktop();
  const [policy, setPolicy] = useState(initial);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy)
      return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await desktop.invoke("set_transfer_preferences", { preferences: policy });
      setNotice("传输设置已保存。");
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  return <form onSubmit={submit} className="ln-settings-form"><h3>传输偏好</h3><p className="ln-muted">选择适合当前网络的任务数量与传输速度。</p><ErrorNotice error={error}/>{notice && <p role="status" className="ln-notice">{notice}</p>}<label className="ln-checkbox"><input type="checkbox" checked={policy.autostart} onChange={event => setPolicy({ ...policy, autostart: event.target.checked })}/>登录系统时启动应用</label><Field label="同时传输任务数" id="max-concurrent"><input id="max-concurrent" type="number" min="1" max="16" required value={policy.maxConcurrent} onChange={event => setPolicy({ ...policy, maxConcurrent: Number(event.target.value) })}/></Field><Field label="传输限速（MB/s，0 为不限速）" id="bandwidth"><input id="bandwidth" type="number" min="0" step="0.1" required value={policy.bandwidthLimitBps / 1e6} onChange={event => setPolicy({ ...policy, bandwidthLimitBps: Number(event.target.value) * 1e6 })}/></Field><button className="ln-button ln-button-primary" disabled={busy}>{busy ? "保存中…" : "保存设置"}</button></form>;
}

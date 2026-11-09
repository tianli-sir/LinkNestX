import { useState, type FormEvent } from "react";
import { apiErrorMessage } from "../api/rest";
import { useAuth } from "../auth";
import { useDesktop } from "../runtime/DesktopProvider";
import { ErrorNotice, Field, Loading, useResource } from "./shared";
export interface SharingPolicy {
  enabled: boolean;
  cacheQuotaBytes: number;
  maxConcurrent: number;
  bandwidthLimitBps: number;
  autostart: boolean;
}
export function SettingsView({ onClose }: {
  onClose: () => void;
}) {
  const { serverUrl, session } = useAuth();
  const { desktop } = useDesktop();
  const resource = useResource("sharing-policy", () => desktop.invoke<SharingPolicy>("get_sharing_policy"));
  return <section className="ln-feature ln-settings"><div className="ln-toolbar"><h2>设置</h2><button className="ln-button ln-button-secondary" onClick={onClose}>返回工作区</button></div><dl className="ln-account-details"><dt>服务器</dt><dd>{serverUrl}</dd><dt>当前账号</dt><dd>{session?.user.username}</dd></dl><ErrorNotice error={resource.error} retry={resource.refresh}/>{resource.loading && <Loading />}{resource.data && <SharingForm key={JSON.stringify(resource.data)} initial={resource.data}/>}</section>;
}
function SharingForm({ initial }: {
  initial: SharingPolicy;
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
      await desktop.invoke("set_sharing_policy", { policy });
      setNotice("传输与共享设置已保存。");
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  return <form onSubmit={submit} className="ln-settings-form"><h3>传输与共享</h3><p className="ln-muted">共享经过校验的缓存分片，可帮助同事更快下载。缓存清理不会删除已保存的文件。</p><ErrorNotice error={error}/>{notice && <p role="status" className="ln-notice">{notice}</p>}<label className="ln-checkbox"><input type="checkbox" checked={policy.enabled} onChange={event => setPolicy({ ...policy, enabled: event.target.checked })}/>允许本机共享缓存分片</label><label className="ln-checkbox"><input type="checkbox" checked={policy.autostart} onChange={event => setPolicy({ ...policy, autostart: event.target.checked })}/>登录系统时启动应用</label><Field label="缓存上限（GB）" id="cache-quota"><input id="cache-quota" type="number" min="0" max="100" step="0.1" required value={policy.cacheQuotaBytes / 1e9} onChange={event => setPolicy({ ...policy, cacheQuotaBytes: Number(event.target.value) * 1e9 })}/></Field><Field label="同时传输任务数" id="max-concurrent"><input id="max-concurrent" type="number" min="1" max="16" required value={policy.maxConcurrent} onChange={event => setPolicy({ ...policy, maxConcurrent: Number(event.target.value) })}/></Field><Field label="传输限速（MB/s，0 为不限速）" id="bandwidth"><input id="bandwidth" type="number" min="0" step="0.1" required value={policy.bandwidthLimitBps / 1e6} onChange={event => setPolicy({ ...policy, bandwidthLimitBps: Number(event.target.value) * 1e6 })}/></Field><button className="ln-button ln-button-primary" disabled={busy}>{busy ? "保存中…" : "保存设置"}</button></form>;
}

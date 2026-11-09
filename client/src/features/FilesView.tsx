import { useState } from "react";
import { apiErrorMessage, type RestClient } from "../api/rest";
import type { User } from "../auth";
import { EmptyState } from "../components/EmptyState";
import { useDesktop, useDesktopEvent } from "../runtime/DesktopProvider";
import type { Transfer } from "./TransfersView";
import { ErrorNotice, Loading, dateLabel, sizeLabel, segment, useResource } from "./shared";
export interface FileRecord {
  id: string;
  groupId: string;
  name: string;
  visibility: "group" | "pool";
  currentVersionId: string;
  createdBy?: string;
  size?: number;
  createdAt: string;
  expiresAt?: string | null;
  status: string;
}
export interface FileVersion {
  id?: string;
  versionId?: string;
  fileId: string;
  name?: string;
  size: number;
  createdAt?: string;
  expiresAt?: string | null;
  status?: string;
}
const versionId = (version: FileVersion) => version.versionId ?? version.id ?? "";
const unavailable = (item: {
  status?: string;
  expiresAt?: string | null;
}) => ["expired", "deleted", "unavailable"].includes(item.status ?? "") || !!(item.expiresAt && new Date(item.expiresAt).valueOf() <= Date.now());
export function FilesView({ client, user, groupId, canManageGroup = false, onRefreshPermissions }: {
  client: RestClient;
  user: User;
  groupId?: string;
  canManageGroup?: boolean;
  onRefreshPermissions?: () => void;
}) {
  const { desktop } = useDesktop();
  const files = useResource(`files-${groupId ?? "pool"}`, () => client.request<FileRecord[]>("GET", `/api/v1/files?${groupId ? `groupId=${segment(groupId)}` : "scope=pool"}`));
  const [query, setQuery] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = files.data?.find(file => file.id === selectedId);
  const [confirm, setConfirm] = useState<FileRecord | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const admin = user.roles.includes("admin");
  useDesktopEvent("transfer.updated", (task: Transfer) => {
    if (task.direction === "upload" && task.state === "completed")
      files.refresh();
  });
  useDesktopEvent("server.event", event => {
    if (event.type === "session.ready")
      files.refresh();
  });
  const canModify = (file: FileRecord) => !files.loading && !files.error && (admin || (file.visibility !== "pool" && (canManageGroup || file.createdBy === user.id)));
  const refreshFiles = () => {
    files.refresh();
    onRefreshPermissions?.();
  };
  const visible = files.data?.filter(file => file.name.toLocaleLowerCase().includes(query.toLocaleLowerCase())) ?? [];
  async function run(action: () => Promise<void>) {
    if (busy)
      return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  const upload = (fileId?: string) => run(async () => {
    if (!groupId)
      return;
    const source = await desktop.invoke<string | null>("pick_upload_file");
    if (!source)
      return;
    await desktop.invoke("start_upload", { groupId, source, fileId: fileId ?? null });
    setNotice("上传已加入传输队列，完成后将显示在文件列表中。");
  });
  const download = (file: FileRecord, id = file.currentVersionId) => run(async () => {
    if (!id)
      throw new Error("文件没有可下载的版本");
    if (unavailable(file))
      throw new Error("文件已过期或不可用");
    const destination = await desktop.invoke<string | null>("pick_download_destination", { name: file.name });
    if (!destination)
      return;
    await desktop.invoke("start_download", { versionId: id, destination });
    setNotice("下载已加入传输队列。");
  });
  const promote = (file: FileRecord) => run(async () => {
    await client.request("POST", `/api/v1/files/${segment(file.id)}/promote`, {});
    files.refresh();
    setNotice("文件已加入公共文件池，长期保留。");
  });
  const remove = () => run(async () => {
    if (!confirm || !canModify(files.data?.find(file => file.id === confirm.id) ?? confirm))
      return;
    await client.request("DELETE", `/api/v1/files/${segment(confirm.id)}`);
    setSelectedId(old => old === confirm.id ? null : old);
    setConfirm(null);
    files.refresh();
    setNotice("文件已删除。");
  });
  return <section className="ln-feature"><div className="ln-toolbar"><div><h2>{groupId ? "群文件" : "公共文件池"}</h2><p className="ln-muted">{groupId ? "群文件保留 30 天；管理员可将文件加入公共文件池。" : "全体员工均可访问，文件长期保留。"}</p></div><div className="ln-actions"><button className="ln-button ln-button-secondary" onClick={refreshFiles}>刷新</button>{groupId && <button className="ln-button ln-button-primary" disabled={busy} onClick={() => void upload()}>上传文件</button>}</div></div>
 <label className="ln-search">搜索文件<input type="search" placeholder="按文件名搜索" value={query} onChange={event => setQuery(event.target.value)}/></label><ErrorNotice error={files.error} retry={refreshFiles}/><ErrorNotice error={error}/>{notice && <p className="ln-notice" role="status">{notice}</p>}{files.loading && <Loading />}
 {!files.loading && !files.error && !visible.length && <EmptyState title={query ? "没有匹配的文件" : groupId ? "暂无群文件" : "公共文件池为空"} description={query ? "尝试使用其他文件名搜索。" : groupId ? "上传文件，与本群成员共享。" : "管理员将文件加入公共池后，会显示在这里。"}/>}
 {visible.length > 0 && <div className="ln-table-scroll"><table className="ln-table"><thead><tr><th>文件名</th><th>大小</th><th>保留期限</th><th>操作</th></tr></thead><tbody>{visible.map(file => <tr key={file.id}><td><strong>{file.name}</strong><small>{dateLabel(file.createdAt)}{unavailable(file) ? " · 已过期或不可用" : ""}</small></td><td>{sizeLabel(file.size)}</td><td>{file.visibility === "pool" ? "长期保留" : <>30 天保留<small>到期 {dateLabel(file.expiresAt)}</small></>}</td><td><div className="ln-actions"><button className="ln-text-button" disabled={busy || unavailable(file) || !file.currentVersionId} onClick={() => void download(file)}>下载</button><button className="ln-text-button" onClick={() => setSelectedId(file.id)}>版本</button>{groupId && canModify(file) && !unavailable(file) && <button className="ln-text-button" disabled={busy} onClick={() => void upload(file.id)}>上传新版本</button>}{admin && file.visibility !== "pool" && !unavailable(file) && <button className="ln-text-button" disabled={busy} onClick={() => void promote(file)}>加入公共池</button>}{canModify(file) && <button className="ln-text-button ln-danger" disabled={busy} onClick={() => setConfirm(file)}>删除</button>}</div></td></tr>)}</tbody></table></div>}
 {confirm && canModify(files.data?.find(file => file.id === confirm.id) ?? confirm) && <div className="ln-confirm" role="group" aria-label="确认删除文件"><h3>删除「{confirm.name}」？</h3><p>删除后将立即停止提供此文件的下载。</p><div className="ln-actions"><button className="ln-button ln-button-danger" disabled={busy} onClick={() => void remove()}>确认删除</button><button className="ln-button ln-button-secondary" disabled={busy} onClick={() => setConfirm(null)}>取消</button></div></div>}
 {selected && <VersionPanel key={selected.id} client={client} file={selected} metadataVersion={files.version} busy={busy || files.loading || !!files.error} onClose={() => setSelectedId(null)} onDownload={id => void download(selected, id)}/>}
</section>;
}
function VersionPanel({ client, file, metadataVersion, busy, onClose, onDownload }: {
  client: RestClient;
  file: FileRecord;
  metadataVersion: number;
  busy: boolean;
  onClose: () => void;
  onDownload: (id: string) => void;
}) {
  const versions = useResource(`versions-${file.id}-${metadataVersion}`, () => client.request<FileVersion[]>("GET", `/api/v1/files/${segment(file.id)}/versions`));
  const [selected, setSelected] = useState(file.currentVersionId);
  const current = versions.data?.find(version => versionId(version) === selected) ?? versions.data?.[0];
  return <section className="ln-version-panel" aria-label={`${file.name}的版本`}><div className="ln-row-heading"><h3>{file.name} · 版本</h3><button className="ln-text-button" onClick={onClose}>关闭版本</button></div><ErrorNotice error={versions.error} retry={versions.refresh}/>{versions.loading && <Loading />}{versions.data?.length ? <><label htmlFor="download-version">下载版本</label><select id="download-version" value={current ? versionId(current) : ""} onChange={event => setSelected(event.target.value)}>{versions.data.map(version => <option key={versionId(version)} value={versionId(version)}>{dateLabel(version.createdAt)} · {sizeLabel(version.size)} · {versionId(version) === file.currentVersionId ? "当前版本" : versionId(version)}{unavailable(version) ? "（已过期）" : ""}</option>)}</select>{current && <p className="ln-muted">{sizeLabel(current.size)} · {unavailable(current) ? "此版本已过期或不可用" : file.visibility === "pool" ? "长期保留" : `到期 ${dateLabel(current.expiresAt)}`}</p>}<button className="ln-button ln-button-primary" disabled={busy || versions.loading || !!versions.error || !current || unavailable(current) || !versionId(current)} onClick={() => current && onDownload(versionId(current))}>下载所选版本</button></> : !versions.loading && !versions.error && <p>没有可用版本，文件可能已经过期或被删除。</p>}</section>;
}

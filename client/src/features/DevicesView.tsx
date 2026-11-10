import { useEffect, useRef, useState, type FormEvent } from "react";
import { apiErrorMessage, type RestClient } from "../api/rest";
import { useAuth, type User } from "../auth";
import { dateLabel, ErrorNotice, Loading, segment, useResource } from "./shared";

export interface Device {
  id: string;
  userId?: string;
  name?: string;
  createdAt: string;
  lastSeen: string;
  active: boolean;
  sessionCount: number;
  current: boolean;
}

const deviceName = (device: Device) => device.name?.trim() || "未命名设备";

export function DevicesView({ client, scope = "me", users = [] }: {
  client: RestClient;
  scope?: "me" | "admin";
  users?: User[];
}) {
  const { logout, session } = useAuth();
  const title = scope === "admin" ? "全部设备" : "我的设备";
  const path = `/api/v1/${scope}/devices`;
  const devices = useResource(`${path}-${session?.user.id}`, () => client.request<Device[]>("GET", path));
  const [selected, setSelected] = useState<Device | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const confirmationHeading = useRef<HTMLHeadingElement>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const accountName = (device: Device) => users.find(user => user.id === device.userId)?.username || device.userId || "未知账号";

  useEffect(() => {
    if (selected) confirmationHeading.current?.focus();
  }, [selected]);

  async function revoke(event: FormEvent) {
    event.preventDefault();
    if (busy || !selected?.active) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await client.request("POST", `${path}/${segment(selected.id)}/revoke`, {});
    } catch (error) {
      setError(apiErrorMessage(error));
      setBusy(false);
      return;
    }
    setSelected(null);
    if (selected.current) {
      try {
        await logout();
      } catch {
        // The revoked remote session can reject logout. AuthProvider already
        // clears the visible session and delegates local cleanup to native.
      }
      return;
    }
    setNotice(`已撤销「${deviceName(selected)}」的现有会话。该设备仍可主动重新登录。`);
    setBusy(false);
    devices.refresh();
  }

  return <section className="ln-devices" aria-label={title}>
    <div className="ln-toolbar"><h3>{title}</h3><button type="button" className="ln-button ln-button-secondary" disabled={busy || devices.loading} onClick={() => {
      setSelected(null);
      setError("");
      setNotice("");
      devices.refresh();
    }}>刷新设备</button></div>
    <p className="ln-muted">撤销设备会结束该设备的现有登录会话；该设备之后仍可主动重新登录。</p>
    <ErrorNotice error={devices.error} retry={devices.refresh} />
    {devices.loading && <Loading />}
    {notice && <p className="ln-notice" role="status">{notice}</p>}
    {!devices.loading && !devices.error && devices.data?.length === 0 && <p className="ln-muted">暂无设备记录。</p>}
    {!!devices.data?.length && <div className="ln-table-scroll"><table className="ln-table" aria-label={title}>
      <thead><tr><th scope="col">设备</th>{scope === "admin" && <th scope="col">所属账号</th>}<th scope="col">登录状态</th><th scope="col">首次登录</th><th scope="col">最近活动</th><th scope="col">操作</th></tr></thead>
      <tbody>{devices.data.map(device => <tr key={device.id}>
        <td><strong>{deviceName(device)}</strong><small>{device.id}</small>{device.current && <small>当前设备</small>}</td>
        {scope === "admin" && <td>{accountName(device)}</td>}
        <td>{device.active ? "已登录" : "无有效会话"}<small>{device.sessionCount} 个会话</small></td>
        <td>{dateLabel(device.createdAt)}</td><td>{dateLabel(device.lastSeen)}</td>
        <td><button type="button" className="ln-text-button ln-danger" disabled={busy || devices.loading || !!devices.error || !device.active} onClick={event => {
          trigger.current = event.currentTarget;
          setSelected(device);
          setError("");
          setNotice("");
        }}>撤销设备</button></td>
      </tr>)}</tbody>
    </table></div>}
    {selected && <form className="ln-confirm" aria-label="撤销设备确认" onSubmit={revoke}>
      <h3 ref={confirmationHeading} tabIndex={-1}>撤销设备 · {deviceName(selected)}</h3>
      {scope === "admin" && <p>所属账号：{accountName(selected)}</p>}
      <p>此设备的现有登录会话将失效。该设备之后仍可主动重新登录。</p>
      {selected.current && <p>这是当前设备。确认后将退出登录，并停止本机传输。</p>}
      <ErrorNotice error={error} />
      <div className="ln-actions"><button className="ln-button ln-button-danger" disabled={busy}>{busy ? "撤销中…" : "确认撤销"}</button><button type="button" className="ln-button ln-button-secondary" disabled={busy} onClick={() => {
        setSelected(null);
        setError("");
        trigger.current?.focus();
      }}>取消</button></div>
    </form>}
  </section>;
}

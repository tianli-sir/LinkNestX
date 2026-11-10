import { useEffect, useRef, useState, type FormEvent } from "react";
import { apiErrorMessage, type RestClient } from "../api/rest";
import { useDesktop } from "../runtime/DesktopProvider";
import { ErrorNotice, Loading, dateLabel, useResource } from "./shared";
interface AuditEvent {
  id: string;
  action: string;
  actorId?: string;
  targetId?: string;
  createdAt: string;
  [key: string]: unknown;
}
interface AuditPage {
  events: AuditEvent[];
  total: number;
}
type ExportResult = { status: "cancelled" } | { status: "saved"; path: string; bytes: number };
interface ExportOperation { id?: string; cancelled: boolean }
export function AuditView({ client }: { client: RestClient }) {
  const { desktop } = useDesktop();
  const [input, setInput] = useState("");
  const [action, setAction] = useState("");
  const [offset, setOffset] = useState(0);
  const [format, setFormat] = useState<"json" | "csv" | null>(null);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [exportMessage, setExportMessage] = useState("");
  const active = useRef<ExportOperation | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      const operation = active.current;
      if (operation) {
        operation.cancelled = true;
        if (operation.id) void desktop.invoke("cancel_audit_export", { exportId: operation.id }).catch(() => undefined);
      }
    };
  }, [desktop]);
  const limit = 50;
  const page = useResource(`audit-${action}-${offset}`, () => client.request<AuditPage>("GET", `/api/v1/admin/audit?action=${encodeURIComponent(action)}&limit=${limit}&offset=${offset}`));
  const events = page.data?.events ?? [];
  const csvCell = (value: unknown) => {
    const text = String(value ?? "");
    const safe = /^[\s]*[=+\-@]/u.test(text) || /^[\t\r\n]/u.test(text) ? `'${text}` : text;
    return `"${safe.replace(/"/g, '""')}"`;
  };
  const text = format === "csv" ? ["id,action,actorId,targetId,createdAt", ...events.map(event => [event.id, event.action, event.actorId, event.targetId, event.createdAt].map(csvCell).join(","))].join("\r\n") : JSON.stringify(events, null, 2);
  function filter(event: FormEvent) {
    event.preventDefault();
    setOffset(0);
    setAction(input.trim());
    setFormat(null);
  }
  async function exportAll(exportFormat: "csv" | "json") {
    if (active.current) return;
    const operation: ExportOperation = { cancelled: false };
    active.current = operation;
    setExporting(true);
    setCancelling(false);
    setExportMessage("");
    setError("");
    try {
      operation.id = await desktop.invoke<string>("reserve_audit_export");
      if (operation.cancelled || !mounted.current) {
        await desktop.invoke("cancel_audit_export", { exportId: operation.id });
        if (mounted.current) setExportMessage("已取消导出。");
        return;
      }
      const result = await desktop.invoke<ExportResult>("export_audit", { exportId: operation.id, format: exportFormat, action });
      if (mounted.current) setExportMessage(result.status === "saved" ? `已保存：${result.path}` : "已取消导出。");
    } catch (cause) {
      if (mounted.current) setError(apiErrorMessage(cause));
    } finally {
      active.current = null;
      if (mounted.current) {
        setExporting(false);
        setCancelling(false);
      }
    }
  }
  async function cancelExport() {
    const operation = active.current;
    if (!operation) return;
    operation.cancelled = true;
    setCancelling(true);
    // While reservation is in flight, exportAll retains this intent and cancels its late reply.
    if (!operation.id) return;
    try {
      await desktop.invoke("cancel_audit_export", { exportId: operation.id });
    } catch (cause) {
      if (mounted.current) {
        setError(apiErrorMessage(cause));
        setCancelling(false);
      }
    }
  }
  async function copy() {
    setError("");
    setCopied(false);
    try {
      if (!navigator.clipboard) throw new Error("无法自动复制，请选中文本后复制");
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch (cause) {
      setError(apiErrorMessage(cause));
    }
  }
  return <>
    <form onSubmit={filter} className="ln-toolbar">
      <label className="ln-search">按操作筛选<input value={input} maxLength={256} disabled={exporting} onChange={event => setInput(event.target.value)} placeholder="例如 user.create" /></label>
      <button className="ln-button ln-button-secondary" disabled={exporting}>查询</button>
    </form>
    <ErrorNotice error={page.error} retry={page.refresh} /><ErrorNotice error={error} />
    <section aria-label="完整审计导出">
      <p className="ln-muted">导出当前已查询条件下的全部记录，不受分页限制。请选择新的文件名。</p>
      <div className="ln-actions">
        <button className="ln-button ln-button-secondary" disabled={exporting} onClick={() => void exportAll("csv")}>导出全部 CSV</button>
        <button className="ln-button ln-button-secondary" disabled={exporting} onClick={() => void exportAll("json")}>导出全部 JSON</button>
        {exporting && <button className="ln-button ln-button-secondary" disabled={cancelling} onClick={() => void cancelExport()}>{cancelling ? "正在取消…" : "取消导出"}</button>}
      </div>
      {exporting && <p role="status">正在选择位置或导出全部记录…</p>}
      {exportMessage && <p role="status">{exportMessage}</p>}
    </section>
    {page.loading && <Loading />}
    <div className="ln-table-scroll"><table className="ln-table"><thead><tr><th>时间</th><th>操作</th><th>操作人</th><th>对象</th></tr></thead><tbody>{events.map(event => <tr key={event.id}><td>{dateLabel(event.createdAt)}</td><td>{event.action}</td><td>{event.actorId ?? "—"}</td><td>{event.targetId ?? "—"}</td></tr>)}</tbody></table></div>
    {!page.loading && !page.error && !events.length && <p className="ln-muted">暂无符合条件的审计记录。</p>}
    <div className="ln-toolbar"><span className="ln-muted">共 {page.data?.total ?? 0} 条记录</span><div className="ln-actions">
      <button className="ln-button ln-button-secondary" disabled={!offset || page.loading} onClick={() => { setOffset(Math.max(0, offset - limit)); setFormat(null); }}>上一页</button>
      <button className="ln-button ln-button-secondary" disabled={page.loading || offset + limit >= (page.data?.total ?? 0)} onClick={() => { setOffset(offset + limit); setFormat(null); }}>下一页</button>
    </div></div>
    <div className="ln-actions">
      <button className="ln-button ln-button-secondary" disabled={!events.length} onClick={() => { setFormat("json"); setCopied(false); }}>查看本页 JSON</button>
      <button className="ln-button ln-button-secondary" disabled={!events.length} onClick={() => { setFormat("csv"); setCopied(false); }}>查看本页 CSV</button>
    </div>
    {format && <div className="ln-export"><label htmlFor="audit-export">当前页审计数据（{format.toUpperCase()}）</label><textarea id="audit-export" readOnly value={text} rows={10} /><button className="ln-button ln-button-secondary" onClick={() => void copy()}>复制数据</button>{copied && <span role="status">已复制</span>}</div>}
  </>;
}

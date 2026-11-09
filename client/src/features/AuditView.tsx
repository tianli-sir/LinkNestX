import { useState, type FormEvent } from "react";
import { apiErrorMessage, type RestClient } from "../api/rest";
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
export function AuditView({ client }: {
  client: RestClient;
}) {
  const [input, setInput] = useState("");
  const [action, setAction] = useState("");
  const [offset, setOffset] = useState(0);
  const [format, setFormat] = useState<"json" | "csv" | null>(null);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const limit = 50;
  const page = useResource(`audit-${action}-${offset}`, () => client.request<AuditPage>("GET", `/api/v1/admin/audit?action=${encodeURIComponent(action)}&limit=${limit}&offset=${offset}`));
  const events = page.data?.events ?? [];
  const csvCell = (value: unknown) => `"${String(value ?? "").replace(/"/g, '""')}"`;
  const text = format === "csv" ? ["id,action,actorId,targetId,createdAt", ...events.map(event => [event.id, event.action, event.actorId, event.targetId, event.createdAt].map(csvCell).join(","))].join("\r\n") : JSON.stringify(events, null, 2);
  function filter(event: FormEvent) {
    event.preventDefault();
    setOffset(0);
    setAction(input.trim());
    setFormat(null);
  }
  async function copy() {
    setError("");
    setCopied(false);
    try {
      if (!navigator.clipboard)
        throw new Error("无法自动复制，请选中文本后复制");
      await navigator.clipboard.writeText(text);
      setCopied(true);
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
  }
  return <><form onSubmit={filter} className="ln-toolbar"><label className="ln-search">按操作筛选<input value={input} onChange={event => setInput(event.target.value)} placeholder="例如 user.create"/></label><button className="ln-button ln-button-secondary">查询</button></form><ErrorNotice error={page.error} retry={page.refresh}/><ErrorNotice error={error}/>{page.loading && <Loading />}<div className="ln-table-scroll"><table className="ln-table"><thead><tr><th>时间</th><th>操作</th><th>操作人</th><th>对象</th></tr></thead><tbody>{events.map(event => <tr key={event.id}><td>{dateLabel(event.createdAt)}</td><td>{event.action}</td><td>{event.actorId ?? "—"}</td><td>{event.targetId ?? "—"}</td></tr>)}</tbody></table></div>{!page.loading && !page.error && !events.length && <p className="ln-muted">暂无符合条件的审计记录。</p>}<div className="ln-toolbar"><span className="ln-muted">共 {page.data?.total ?? 0} 条记录</span><div className="ln-actions"><button className="ln-button ln-button-secondary" disabled={!offset || page.loading} onClick={() => {
    setOffset(Math.max(0, offset - limit));
    setFormat(null);
  }}>上一页</button><button className="ln-button ln-button-secondary" disabled={page.loading || offset + limit >= (page.data?.total ?? 0)} onClick={() => {
    setOffset(offset + limit);
    setFormat(null);
  }}>下一页</button></div></div><div className="ln-actions"><button className="ln-button ln-button-secondary" disabled={!events.length} onClick={() => {
    setFormat("json");
    setCopied(false);
  }}>查看本页 JSON</button><button className="ln-button ln-button-secondary" disabled={!events.length} onClick={() => {
    setFormat("csv");
    setCopied(false);
  }}>查看本页 CSV</button></div>{format && <div className="ln-export"><label htmlFor="audit-export">当前页审计数据（{format.toUpperCase()}）</label><textarea id="audit-export" readOnly value={text} rows={10}/><button className="ln-button ln-button-secondary" onClick={() => void copy()}>复制数据</button>{copied && <span role="status">已复制</span>}</div>}</>;
}

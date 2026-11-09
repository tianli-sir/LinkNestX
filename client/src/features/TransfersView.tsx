import { useCallback, useEffect, useRef, useState } from "react";
import { apiErrorMessage } from "../api/rest";
import { EmptyState } from "../components/EmptyState";
import { useDesktop, useDesktopEvent } from "../runtime/DesktopProvider";
import { ErrorNotice, Loading, sizeLabel } from "./shared";
export type TransferState = "queued" | "hashing" | "transferring" | "paused" | "verifying" | "completed" | "failed";
export interface Transfer {
  id: string;
  direction: "upload" | "download";
  versionId: string;
  name: string;
  state: TransferState;
  bytesCompleted: number;
  totalBytes: number;
  activeSources: string[];
  error?: string;
}
const labels: Record<TransferState, string> = { queued: "等待中", hashing: "正在计算校验", transferring: "传输中", paused: "已暂停", verifying: "正在校验", completed: "已完成", failed: "失败" };
export const activeTransfer = (task: Transfer) => ["queued", "hashing", "transferring", "verifying"].includes(task.state);
export function useTransfers() {
  const { desktop } = useDesktop();
  const [tasks, setTasks] = useState<Transfer[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const revision = useRef(0);
  const updates = useRef(new Map<string, Transfer>());
  useDesktopEvent("transfer.updated", (task: Transfer) => {
    updates.current.set(task.id, task);
    setTasks(previous => {
      const merged = new Map(previous.map(t => [t.id, t]));
      merged.set(task.id, task);
      return [...merged.values()];
    });
  });
  const refresh = useCallback(async () => {
    const run = ++revision.current;
    updates.current.clear();
    setLoading(true);
    setError("");
    try {
      const list = await desktop.invoke<Transfer[]>("list_transfers");
      if (run !== revision.current)
        return;
      const merged = new Map(list.map(t => [t.id, t]));
      updates.current.forEach((task, id) => merged.set(id, task));
      setTasks([...merged.values()]);
    }
    catch (error) {
      if (run === revision.current)
        setError(apiErrorMessage(error));
    }
    finally {
      if (run === revision.current)
        setLoading(false);
    }
  }, [desktop]);
  useEffect(() => {
    void refresh();
    return () => {
      revision.current++;
    };
  }, [refresh]);
  return { tasks, error, loading, refresh };
}
export function TransfersView({ queue }: {
  queue: ReturnType<typeof useTransfers>;
}) {
  const { desktop } = useDesktop();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  async function action(task: Transfer) {
    setBusy(task.id);
    setError("");
    try {
      await desktop.invoke(task.state === "paused" || task.state === "failed" ? "resume_transfer" : "pause_transfer", { taskId: task.id });
      await queue.refresh();
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy("");
    }
  }
  return <section className="ln-feature"><div className="ln-toolbar"><p className="ln-muted">窗口关闭后，已启动的任务仍可在后台运行。</p><button className="ln-button ln-button-secondary" onClick={() => void queue.refresh()}>刷新</button></div><ErrorNotice error={queue.error} retry={() => void queue.refresh()}/><ErrorNotice error={error}/>{queue.loading && <Loading />}
  {!queue.loading && !queue.error && queue.tasks.length === 0 && <EmptyState icon="transfers" title="暂无传输任务" description="在群文件或公共文件池中选择文件，即可开始下载。"/>}
  <ul className="ln-transfer-list">{queue.tasks.map(task => <li key={task.id}><div className="ln-row-heading"><div><strong>{task.name || "正在读取文件名"}</strong><p className="ln-muted">{task.direction === "upload" ? "上传" : "下载"} · {labels[task.state]} · {task.activeSources.length} 个来源</p></div>{task.state !== "completed" && <button className="ln-button ln-button-secondary" disabled={busy === task.id} onClick={() => void action(task)}>{task.state === "paused" ? "继续" : task.state === "failed" ? "重试" : "暂停"}</button>}</div><progress aria-label={`${task.name}传输进度`} max={task.totalBytes || 1} value={Math.min(task.bytesCompleted, task.totalBytes)}/><p className="ln-muted">{sizeLabel(task.bytesCompleted)} / {sizeLabel(task.totalBytes)}</p><ErrorNotice error={task.error ?? ""}/></li>)}</ul>
 </section>;
}

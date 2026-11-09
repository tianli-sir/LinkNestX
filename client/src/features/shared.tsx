import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { apiErrorMessage } from "../api/rest";
export function useResource<T>(key: string, load: () => Promise<T>) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const [version, setVersion] = useState(0);
  const [settledRequest, setSettledRequest] = useState("");
  const requestKey = JSON.stringify([key, revision]);
  const loader = useRef(load);
  loader.current = load;
  const refresh = useCallback(() => setRevision(value => value + 1), []);
  useEffect(() => {
    let canceled = false;
    setLoading(true);
    setError("");
    loader.current().then(result => {
      if (!canceled) {
        setData(result);
        setVersion(value => value + 1);
      }
    }).catch(error => {
      if (!canceled)
        setError(apiErrorMessage(error));
    }).finally(() => {
      if (!canceled) {
        setLoading(false);
        setSettledRequest(requestKey);
      }
    });
    return () => {
      canceled = true;
    };
  }, [key, revision]);
  return { data, error, loading: loading || settledRequest !== requestKey, refresh, version };
}
export function ErrorNotice({ error, retry }: {
  error: string;
  retry?: () => void;
}) {
  return error ? <div role="alert" className="ln-notice ln-notice-error"><span>{error}</span>{retry && <button type="button" className="ln-button ln-button-secondary" onClick={retry}>重试</button>}</div> : null;
}
export function Loading() {
  return <p className="ln-muted" role="status">正在加载…</p>;
}
export function Field({ label, id, children }: {
  label: string;
  id: string;
  children: ReactNode;
}) {
  return <div className="ln-field"><label htmlFor={id}>{label}</label>{children}</div>;
}
export function sizeLabel(size?: number) {
  if (size === undefined)
    return "大小待读取";
  if (size < 1000)
    return `${size} B`;
  const units = ["KB", "MB", "GB"];
  let n = size / 1000;
  let i = 0;
  while (n >= 1000 && i < 2) {
    n /= 1000;
    i++;
  }
  return `${n.toFixed(n >= 10 ? 0 : 1)} ${units[i]}`;
}
export function dateLabel(date?: string | null) {
  if (!date)
    return "—";
  const parsed = new Date(date);
  return Number.isNaN(parsed.valueOf()) ? "—" : parsed.toLocaleString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}
export const segment = (value: string) => encodeURIComponent(value);

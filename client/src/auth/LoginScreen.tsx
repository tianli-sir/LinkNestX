import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { apiErrorMessage, normalizeServerUrl } from "../api/rest";
import { Icon } from "../components/Icon";
import type { LoginInput } from "./types";
import "../styles/desktop.css";

export interface LoginScreenProps {
  onLogin(input: LoginInput): Promise<void>;
  initialServerUrl?: string;
  appVersion?: string;
}

type Field = "serverUrl" | "username" | "password";
type Errors = Partial<Record<Field, string>>;
const fieldLabels: Record<Field, string> = { serverUrl: "服务器地址", username: "用户名", password: "密码" };

export function LoginScreen({ onLogin, initialServerUrl = "", appVersion }: LoginScreenProps) {
  const id = useId();
  const [values, setValues] = useState({ serverUrl: initialServerUrl, username: "", password: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [apiError, setApiError] = useState("");
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const summary = useRef<HTMLDivElement>(null);
  const [failureCount, setFailureCount] = useState(0);
  useEffect(() => { if (failureCount) summary.current?.focus(); }, [failureCount]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending.current) return;
    const nextErrors: Errors = {};
    let serverUrl = "";
    try { serverUrl = normalizeServerUrl(values.serverUrl); }
    catch (error) { nextErrors.serverUrl = apiErrorMessage(error); }
    if (!values.username.trim()) nextErrors.username = "请输入用户名";
    if (!values.password) nextErrors.password = "请输入密码";
    setErrors(nextErrors);
    setApiError("");
    if (Object.keys(nextErrors).length) { setFailureCount((value) => value + 1); return; }
    pending.current = true;
    setBusy(true);
    try {
      await onLogin({ serverUrl, username: values.username.trim(), password: values.password });
      setValues((previous) => ({ ...previous, serverUrl, password: "" }));
    } catch (error) {
      setApiError(apiErrorMessage(error, "登录失败，请检查服务器地址和账号后重试"));
      setFailureCount((value) => value + 1);
    } finally { pending.current = false; setBusy(false); }
  }

  return <main className="ln-login" aria-labelledby={`${id}-title`}>
    <div className="ln-login-brand"><span className="ln-brand-mark"><Icon name="nest" size={24} /></span><strong>LinkNestX</strong><span>企业协作</span></div>
    <section className="ln-login-panel">
      <header className="ln-login-heading"><h1 id={`${id}-title`}>登录工作区</h1><p>使用管理员提供的服务器地址和员工账号。</p></header>
      <form aria-label="员工登录" onSubmit={(event) => { void submit(event); }} noValidate aria-busy={busy}>
        {(Object.keys(errors).length > 0 || apiError) && <div className="ln-error-summary" role="alert" tabIndex={-1} ref={summary}>
          {apiError ? <p>{apiError}</p> : <><p>请检查以下信息</p><ul>{Object.entries(errors).map(([field, message]) => <li key={field}><a href={`#${id}-${field}`} onClick={(event) => { event.preventDefault(); document.getElementById(`${id}-${field}`)?.focus(); }}>{message}</a></li>)}</ul></>}
        </div>}
        {(["serverUrl", "username", "password"] as const).map((field) => <div className="ln-field" key={field}>
          <label htmlFor={`${id}-${field}`}>{fieldLabels[field]}</label>
          <input id={`${id}-${field}`} name={field} type={field === "password" ? "password" : "text"} value={values[field]} disabled={busy}
            autoComplete={field === "password" ? "current-password" : field === "username" ? "username" : "url"}
            autoCapitalize="none" spellCheck={false} aria-invalid={Boolean(errors[field])}
            aria-describedby={errors[field] ? `${id}-${field}-error` : field === "serverUrl" ? `${id}-server-hint` : undefined}
            placeholder={field === "serverUrl" ? "https://files.example.com" : undefined}
            onChange={(event) => {
              const value = event.target.value;
              setValues((previous) => ({ ...previous, [field]: value }));
              setErrors((previous) => {
                const remainingErrors = { ...previous };
                delete remainingErrors[field];
                return remainingErrors;
              });
            }} />
          {errors[field] ? <p className="ln-field-error" id={`${id}-${field}-error`}>{errors[field]}</p> : field === "serverUrl" ? <p className="ln-field-hint" id={`${id}-server-hint`}>默认使用 HTTPS，地址由管理员提供。</p> : null}
        </div>)}
        <button className="ln-button ln-button-primary ln-login-submit" type="submit" disabled={busy}>{busy ? "正在登录…" : "登录"}</button>
      </form>
      <p className="ln-login-help"><Icon name="info" size={16} />还没有账号？请联系企业管理员。</p>
    </section>
    <footer className="ln-login-footer"><span><Icon name="lock" size={14} />在桌面应用中连接企业工作区</span>{appVersion && <span className="ln-version" data-testid="app-version">版本 {appVersion}</span>}</footer>
  </main>;
}

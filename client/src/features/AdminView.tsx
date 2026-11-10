import { useState, type FormEvent } from "react";
import { apiErrorMessage, type RestClient } from "../api/rest";
import { useAuth, type User } from "../auth";
import { GroupsView } from "./GroupsView";
import { AuditView } from "./AuditView";
import { DevicesView } from "./DevicesView";
import { ErrorNotice, Field, Loading, segment, useResource } from "./shared";
export function AdminView({ client }: {
  client: RestClient;
}) {
  const { session } = useAuth();
  if (!session?.user.roles.includes("admin")) return <ErrorNotice error="需要管理员权限"/>;
  return <AdminContent client={client}/>;
}
function AdminContent({ client }: {
  client: RestClient;
}) {
  const [tab, setTab] = useState("users");
  const {session}=useAuth();
  const [groupsRevision,setGroupsRevision]=useState(0);
  const users = useResource("admin-users", () => client.request<User[]>("GET", "/api/v1/admin/users"));
  return <section className="ln-feature"><div className="ln-tabs ln-admin-tabs" aria-label="管理内容">{[["users", "账号管理"], ["devices", "设备管理"], ["groups", "群组管理"], ["audit", "审计记录"]].map(([key, label]) => <button key={key} aria-pressed={tab === key} onClick={() => setTab(key)}>{label}</button>)}</div>{tab !== "audit" && <><ErrorNotice error={users.error} retry={users.refresh}/>{users.loading && <Loading />}</>}{tab === "users" && <UsersPanel client={client} users={users.data ?? []} refresh={users.refresh}/>} {tab === "devices" && <DevicesView client={client} scope="admin" users={users.data ?? []}/>} {tab === "groups" && <><GroupForm client={client} users={users.data ?? []} onCreated={()=>setGroupsRevision(v=>v+1)}/><GroupsView key={groupsRevision} client={client} user={session!.user}/></>} {tab === "audit" && <AuditView client={client}/>}</section>;
}
function UsersPanel({ client, users, refresh }: {
  client: RestClient;
  users: User[];
  refresh: () => void;
}) {
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [admin, setAdmin] = useState(false);
  const [resetPassword, setResetPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [action, setAction] = useState<{
    user: User;
    kind: "disable" | "password" | "revoke-sessions";
  } | null>(null);
  async function run(work: () => Promise<void>) {
    if (busy)
      return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await work();
      refresh();
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  function create(event: FormEvent) {
    event.preventDefault();
    void run(async () => {
      await client.request("POST", "/api/v1/users", { username: username.trim(), email: email.trim(), password, roles: admin ? ["admin"] : ["user"] });
      setUsername("");
      setEmail("");
      setPassword("");
      setAdmin(false);
      setNotice("账号已创建。请通过可信方式告知同事初始密码。");
    });
  }
  function execute(event: FormEvent) {
    event.preventDefault();
    if (!action)
      return;
    void run(async () => {
      await client.request("POST", `/api/v1/admin/users/${segment(action.user.id)}/${action.kind}`, action.kind === "disable" ? { disabled: !action.user.disabled } : action.kind === "password" ? { password: resetPassword } : {});
      setAction(null);
      setResetPassword("");
      setNotice("账号操作已完成。");
    });
  }
  return <><ErrorNotice error={error}/>{notice && <p className="ln-notice" role="status">{notice}</p>}<div className="ln-table-scroll"><table className="ln-table"><thead><tr><th>账号</th><th>角色 / 状态</th><th>操作</th></tr></thead><tbody>{users.map(user => <tr key={user.id}><td><strong>{user.username}</strong><small>{user.email || user.id}</small></td><td>{user.roles.includes("admin") ? "管理员" : "员工"} · {user.disabled ? "已停用" : "正常"}</td><td><div className="ln-actions">{([["disable", user.disabled ? "启用" : "停用"], ["password", "重置密码"], ["revoke-sessions", "强制下线"]] as const).map(([kind, label]) => <button className="ln-text-button" key={kind} disabled={busy} onClick={() => {
    setAction({ user, kind });
    setResetPassword("");
    setError("");
  }}>{label}</button>)}</div></td></tr>)}</tbody></table></div>
  {action && <form className="ln-confirm" onSubmit={execute}><h3>{action.kind === "password" ? "重置密码" : action.kind === "disable" ? action.user.disabled ? "启用账号" : "停用账号" : "强制下线"} · {action.user.username}</h3><p>{action.kind === "disable" && action.user.disabled ? "启用后，该员工可以重新登录。" : "现有会话将失效，员工需要重新登录。"}</p>{action.kind === "password" && <Field label="重置为新密码" id="reset-password"><input id="reset-password" type="password" autoComplete="new-password" required minLength={8} value={resetPassword} onChange={event => setResetPassword(event.target.value)}/></Field>}<div className="ln-actions"><button className="ln-button ln-button-danger" disabled={busy}>确认操作</button><button type="button" className="ln-button ln-button-secondary" disabled={busy} onClick={() => {
    setAction(null);
    setResetPassword("");
  }}>取消</button></div></form>}
  <form className="ln-inline-form" onSubmit={create}><h3>创建员工账号</h3><Field label="新用户名" id="new-username"><input id="new-username" required value={username} autoComplete="off" onChange={event => setUsername(event.target.value)}/></Field><Field label="邮箱（可选）" id="new-email"><input id="new-email" type="email" value={email} onChange={event => setEmail(event.target.value)}/></Field><Field label="初始密码" id="initial-password"><input id="initial-password" type="password" required minLength={8} autoComplete="new-password" value={password} onChange={event => setPassword(event.target.value)}/></Field><label className="ln-checkbox"><input type="checkbox" checked={admin} onChange={event => setAdmin(event.target.checked)}/>授予管理员权限</label><button className="ln-button ln-button-primary" disabled={busy || !username.trim() || !password}>创建账号</button></form>
 </>;
}
function GroupForm({ client, users, onCreated }: {
  onCreated:()=>void;
  client: RestClient;
  users: User[];
}) {
  const [name, setName] = useState("");
  const [owner, setOwner] = useState("");
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
      await client.request("POST", "/api/v1/groups", { name: name.trim(), ...(owner ? { ownerId: owner } : {}) });
      setName("");
      setNotice("群组已创建，可在下方管理成员和文件。");
      onCreated();
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  return <form className="ln-inline-form" onSubmit={submit}><h3>创建群组</h3><ErrorNotice error={error}/>{notice && <p className="ln-notice" role="status">{notice}</p>}<Field label="群组名称" id="group-name"><input id="group-name" required maxLength={120} value={name} onChange={event => setName(event.target.value)}/></Field><Field label="指定群主" id="group-owner"><select id="group-owner" value={owner} onChange={event => setOwner(event.target.value)}><option value="">由我担任群主</option>{users.filter(user => !user.disabled).map(user => <option value={user.id} key={user.id}>{user.username}</option>)}</select></Field><button className="ln-button ln-button-primary" disabled={busy || !name.trim()}>创建群组</button></form>;
}

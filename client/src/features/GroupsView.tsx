import { useEffect, useRef, useState, type FormEvent } from "react";
import { apiErrorMessage, type RestClient } from "../api/rest";
import type { User } from "../auth";
import { EmptyState } from "../components/EmptyState";
import { useDesktop, useDesktopEvent } from "../runtime/DesktopProvider";
import { ChatView } from "./ChatView";
import { FilesView } from "./FilesView";
import { Avatar, displayName, ProfilePanel } from "./messaging/Profile";
import { ErrorNotice, Loading, Field, segment, useResource } from "./shared";
export interface Group {
  id: string;
  isMember?: boolean;
  name: string;
  createdBy: string;
  createdAt: string;
}
export interface DirectoryUser {
  id: string;
  username: string;
  email: string;
}
export interface Member {
  userId: string;
  role: "owner" | "member";
  joinedAt: string;
  nickname?: string | null;
  displayName: string;
  accountNumber?: string;
  hasAvatar?: boolean;
  disabled?: boolean;
}
export function GroupsView({ client, user, initialGroupId = "", initialTab }: {
  client: RestClient;
  user: User;
  initialGroupId?: string;
  initialTab?: string;
}) {
  const groups = useResource("groups", () => client.request<Group[]>("GET", "/api/v1/groups"));
  const [selected, setSelected] = useState(initialGroupId);
  const [membershipRevision, setMembershipRevision] = useState(0);
  const refreshGroups = () => {
    groups.refresh();
    setMembershipRevision(value => value + 1);
  };
  useDesktopEvent("server.event", event => {
    if (["session.ready","membership.changed"].includes(event.type))
      refreshGroups();
  });
  const current = groups.data?.find(group => group.id === selected) ?? groups.data?.[0];
  return <div className="ln-groups-layout"><aside className="ln-group-list" aria-label={user.roles.includes("admin") ? "全部群组" : "我的群组"}><div className="ln-row-heading"><h2>{user.roles.includes("admin") ? "全部群组" : "我的群组"}</h2><button className="ln-text-button" onClick={refreshGroups}>刷新</button></div><ErrorNotice error={groups.error} retry={refreshGroups}/>{groups.loading && <Loading />}{groups.data?.map(group => <button className="ln-group-button" aria-pressed={current?.id === group.id} onClick={() => setSelected(group.id)} key={group.id}>{group.name}</button>)}</aside><div className="ln-group-content">{current ? <GroupWorkspace key={`${current.id}-${current.isMember}`} group={current} client={client} user={user} membershipRevision={membershipRevision} initialTab={initialTab}/> : !groups.loading && !groups.error && <EmptyState icon="groups" title="尚未加入群组" description="请联系管理员或群主，将你添加到部门或项目群。"/>}</div></div>;
}
function GroupWorkspace({ group, client, user, membershipRevision, initialTab }: {
  group: Group;
  membershipRevision: number;
  initialTab?: string;
  client: RestClient;
  user: User;
}) {
  const member=group.isMember!==false;
  const [tab, setTab] = useState(member ? initialTab ?? "chat" : "files");
  const {capabilities}=useDesktop();
  const directory = useResource(`user-directory-${capabilities.adminWrite}`, () => capabilities.adminWrite ? client.request<DirectoryUser[]>("GET", "/api/v1/users") : Promise.resolve([]));
  const members = useResource(`members-${group.id}-${membershipRevision}`, () => client.request<Member[]>("GET", `/api/v1/groups/${segment(group.id)}/members`));
  const canManage = capabilities.adminWrite && (user.roles.includes("admin") || (!members.loading && !members.error && !!members.data?.some(member => member.userId === user.id && member.role === "owner")));
  return <><div className="ln-group-heading"><div><h2>{group.name}</h2><p className="ln-muted">{!member ? "管理模式 · 尚未加入聊天" : members.data ? `${members.data.length} 位成员` : "群组工作区"}</p></div><div className="ln-tabs" aria-label="群组内容">{[["chat", "聊天"], ["files", "群文件"], ["members", "成员"]].filter(([key])=>key!=="chat"||member).map(([key, label]) => <button key={key} aria-pressed={tab === key} onClick={() => {
    if (key !== "chat")
      members.refresh();
    setTab(key);
  }}>{label}</button>)}</div></div>
 {member && <div className="ln-chat-tab" hidden={tab !== "chat"}><ChatView client={client} groupId={group.id} user={user} directory={directory.data ?? []} active={tab === "chat"} onFiles={() => { members.refresh(); setTab("files"); }}/></div>}
 {tab === "files" && <FilesView client={client} user={user} groupId={group.id} canManageGroup={canManage} onRefreshPermissions={members.refresh}/>}
 {tab === "members" && <MembersView client={client} user={user} groupId={group.id} members={members} canManage={canManage} directory={directory}/>}
</>;
}
function MembersView({ client, user, groupId, members, canManage, directory }: {
  client: RestClient;
  user: User;
  groupId: string;
  members: ReturnType<typeof useResource<Member[]>>;
  canManage: boolean;
  directory: ReturnType<typeof useResource<DirectoryUser[]>>;
}) {
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState("member");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<Member | null>(null);
  const [presence, setPresence] = useState<Record<string, boolean>>({});
  useEffect(() => {
    if (members.loading || members.error || !members.data?.some(member => member.userId === confirm?.userId)) setConfirm(null);
  }, [members.loading, members.error, members.data, confirm?.userId]);
  useDesktopEvent("server.event", event => {
    if (event.type === "presence.changed" && event.groupId === groupId)
      setPresence(old => ({ ...old, [event.userId]: event.online }));
  });
  async function add(event: FormEvent) {
    event.preventDefault();
    if (busy || !canManage)
      return;
    setBusy(true);
    setError("");
    try {
      await client.request("POST", `/api/v1/groups/${segment(groupId)}/members`, { userId: userId.trim(), role });
      setUserId("");
      members.refresh();
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  async function remove() {
    if (!confirm || busy || !canManage)
      return;
    setBusy(true);
    setError("");
    try {
      await client.request("DELETE", `/api/v1/groups/${segment(groupId)}/members/${segment(confirm.userId)}`);
      setConfirm(null);
      members.refresh();
    }
    catch (error) {
      setError(apiErrorMessage(error));
    }
    finally {
      setBusy(false);
    }
  }
  return <section className="ln-feature"><ErrorNotice error={members.error} retry={members.refresh}/><ErrorNotice error={error}/>{members.loading && <Loading />}<ul className="ln-data-list">{!members.loading && !members.error && members.data?.map(member => <MemberProfileRow key={member.userId} member={member} group={{ id: groupId }} me={user} client={client} online={presence[member.userId]} canManage={canManage} onRemove={() => setConfirm(member)}/>)}</ul>
 {confirm && canManage && <div className="ln-confirm" role="group" aria-label="确认移除成员"><p>确定移除该成员？该成员将失去本群访问权限。</p><div className="ln-actions"><button className="ln-button ln-button-danger" disabled={busy} onClick={() => void remove()}>确认移除</button><button className="ln-button ln-button-secondary" onClick={() => setConfirm(null)}>取消</button></div></div>}
 {canManage && <form className="ln-inline-form" onSubmit={add}><h3>添加或更新成员</h3><ErrorNotice error={directory.error} retry={directory.refresh}/>{directory.data ? <Field label="选择同事" id="member-id"><select id="member-id" value={userId} onChange={event => setUserId(event.target.value)} required><option value="">请选择同事</option>{directory.data.map(person => <option key={person.id} value={person.id}>{person.username}{person.email ? ` · ${person.email}` : ""}</option>)}</select></Field> : <Field label="成员账号 ID" id="member-id"><input id="member-id" value={userId} onChange={event => setUserId(event.target.value)} required/></Field>}<Field label="群组角色" id="member-role"><select id="member-role" value={role} onChange={event => setRole(event.target.value)}><option value="member">成员</option><option value="owner">群主</option></select></Field><button className="ln-button ln-button-primary" disabled={busy || !userId.trim()}>保存成员</button></form>}
</section>;
}

function MemberProfileRow({ member, group, me, client, online, canManage, onRemove }: {
  member: Member;
  group: { id: string };
  me: User;
  client: RestClient;
  online?: boolean;
  canManage: boolean;
  onRemove: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const disabled = !!member.disabled;
  const person: User = {
    id: member.userId,
    username: member.displayName,
    displayName: member.displayName,
    email: "",
    roles: ["member"],
    disabled,
    mustChangePassword: false,
    accountNumber: member.accountNumber,
    avatarUrl: member.hasAvatar ? `/api/v1/profiles/${member.userId}/avatar` : undefined,
  };
  async function direct() {
    try {
      const conversation = await client.request<{ id: string }>("POST", "/api/v1/conversations/direct", { userId: member.userId });
      setOpen(false);
      window.dispatchEvent(new CustomEvent("linknestx:open-conversation", { detail: { id: conversation.id } }));
    } catch (e) {
      setError(apiErrorMessage(e));
    }
  }
  return <li><div className="ln-member-identity">{disabled ? <Avatar name={member.displayName}/> : <Avatar profile={person} onClick={() => setOpen(true)}/>}<div><strong>{member.displayName}{disabled ? "（已停用）" : ""}</strong>{!disabled && <p className="ln-muted">{member.accountNumber && /^\d+$/.test(member.accountNumber) ? `账号 ${member.accountNumber}` : "账号信息暂不可用"}</p>}<p className="ln-muted">{member.role === "owner" ? "群主" : "成员"}{online !== undefined ? ` · ${online ? "在线" : "离线"}` : ""}</p></div></div>{canManage && <button className="ln-text-button ln-danger" onClick={onRemove}>移除</button>}<ErrorNotice error={error}/>{!disabled && open && <ProfilePanel profile={person} me={me} client={client} group={{ id: group.id }} nickname={member.nickname ?? null} onClose={() => setOpen(false)} onDirect={direct} onChanged={() => {}}/>}</li>;
}

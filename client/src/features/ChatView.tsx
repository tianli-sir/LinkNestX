import { useCallback, useEffect, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import { apiErrorMessage, type RestClient } from "../api/rest";
import type { DirectoryUser } from "./GroupsView";
import type { User } from "../auth";
import { useDesktop, useDesktopEvent } from "../runtime/DesktopProvider";
import { ErrorNotice, Loading, dateLabel, segment } from "./shared";
export interface Message {
  id: string;
  sequence: number;
  groupId: string;
  senderId: string;
  clientMessageId: string;
  body: string | null;
  createdAt: string;
  expiredAt?: string;
  cursor: string;
  fileId?: string;
  fileVersionId?: string;
}
interface MessagePage {
  messages: Message[];
  nextCursor: string;
  hasMore: boolean;
}
interface PendingMessage {
  clientMessageId: string;
  body: string;
  status: "sending" | "failed";
  error?: string;
}
function mergeMessages(previous: Message[], incoming: Message[]) {
  const map = new Map(previous.map(message => [message.id, message]));
  incoming.forEach(message => map.set(message.id, message));
  return [...map.values()].sort((a, b) => a.sequence - b.sequence);
}
export function ChatView({ client, groupId, user, directory, onFiles, active = true }: {
  client: RestClient;
  groupId: string;
  user: User;
  directory: DirectoryUser[];
  onFiles: () => void;
  active?: boolean;
}) {
  const { connection } = useDesktop();
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState<PendingMessage | null>(null);
  const scrollArea = useRef<HTMLDivElement>(null);
  const followLatest = useRef(true);
  const previousLatest = useRef("");
  const [hasUnread, setHasUnread] = useState(false);
  const scrollToLatest = useCallback(() => {
    const area = scrollArea.current;
    if (area)
      area.scrollTop = Math.max(0, area.scrollHeight - area.clientHeight);
    followLatest.current = true;
    setHasUnread(false);
  }, []);
  useLayoutEffect(() => {
    const latest = messages.at(-1)?.id ?? "";
    if (active && followLatest.current)
      scrollToLatest();
    else if (latest && latest !== previousLatest.current)
      setHasUnread(true);
    previousLatest.current = latest;
  }, [messages, pending, loading, active, connection.status, scrollToLatest]);
  useEffect(() => {
    const resize = () => {
      if (active && followLatest.current)
        scrollToLatest();
    };
    window.addEventListener("resize", resize);
    return () => window.removeEventListener("resize", resize);
  }, [active, scrollToLatest]);
  const cursor = useRef("");
  const alive = useRef(false);
  const inFlight = useRef(false);
  const repeat = useRef(false);
  const sending = useRef<string | null>(null);
  const generation = useRef(0);
  const receive = useCallback((incoming: Message[]) => {
    const relevant = incoming.filter(message => message.groupId === groupId);
    setMessages(old => mergeMessages(old, relevant));
    const delivered = (id: string) => relevant.some(message => message.senderId === user.id && message.clientMessageId === id);
    if (sending.current && delivered(sending.current))
      sending.current = null;
    setPending(current => current && delivered(current.clientMessageId) ? null : current);
  }, [groupId, user.id]);
  const sync = useCallback(async () => {
    if (inFlight.current) {
      repeat.current = true;
      return;
    }
    inFlight.current = true;
    const run = generation.current;
    setError("");
    try {
      do {
        repeat.current = false;
        let more = true;
        while (more && alive.current && run === generation.current) {
          const after = cursor.current;
          const page = await client.request<MessagePage>("GET", `/api/v1/groups/${segment(groupId)}/messages?cursor=${encodeURIComponent(after)}&limit=200`);
          if (!alive.current || run !== generation.current)
            return;
          receive(page.messages);
          if (page.hasMore && (!page.nextCursor || page.nextCursor === after))
            throw new Error("消息同步游标无效，请重试");
          cursor.current = page.nextCursor;
          more = page.hasMore;
        }
      } while (repeat.current && alive.current && run === generation.current);
    }
    catch (error) {
      if (alive.current && run === generation.current)
        setError(apiErrorMessage(error));
    }
    finally {
      if (run === generation.current) {
        inFlight.current = false;
        if (alive.current)
          setLoading(false);
      }
    }
  }, [client, groupId, receive]);
  useDesktopEvent("server.event", event => {
    if (event.type === "session.ready") {
      void sync();
      return;
    }
    if ((event.type === "message.created" || event.type === "message.ack") && event.groupId === groupId && event.message?.groupId === groupId) {
      const message = event.message as Message;
      receive([message]);
    }
  });
  useEffect(() => {
    alive.current = true;
    generation.current++;
    inFlight.current = false;
    void sync();
    return () => {
      alive.current = false;
      generation.current++;
    };
  }, [sync]);
  async function post(message: PendingMessage) {
    if (sending.current)
      return;
    followLatest.current = true;
    sending.current = message.clientMessageId;
    setPending({ ...message, status: "sending", error: undefined });
    try {
      const result = await client.request<Message>("POST", `/api/v1/groups/${segment(groupId)}/messages`, { clientMessageId: message.clientMessageId, body: message.body });
      if (alive.current) {
        setMessages(old => mergeMessages(old, [result]));
        setPending(current => current?.clientMessageId === message.clientMessageId ? null : current);
      }
    }
    catch (error) {
      if (alive.current)
        setPending(current => current?.clientMessageId === message.clientMessageId ? { ...message, status: "failed", error: apiErrorMessage(error) } : current);
    }
    finally {
      if (sending.current === message.clientMessageId)
        sending.current = null;
    }
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    if (!draft.trim() || pending || sending.current)
      return;
    const message: PendingMessage = { clientMessageId: crypto.randomUUID(), body: draft, status: "sending" };
    setDraft("");
    void post(message);
  }
  return <section className="ln-chat" aria-label="群聊天">
 {connection.status !== "connected" && <p className="ln-notice">实时连接尚未就绪。消息发送会返回实际结果，连接恢复后自动补齐记录。</p>}
 <ErrorNotice error={error} retry={() => void sync()}/>{loading && <Loading />}
 <div className="ln-message-scroll" role="region" aria-label="聊天记录" tabIndex={0} ref={scrollArea} onScroll={event => {
      const area = event.currentTarget;
      followLatest.current = area.scrollHeight - area.clientHeight - area.scrollTop < 48;
      if (followLatest.current)
        setHasUnread(false);
    }}>
 <ol className="ln-messages" aria-label="消息记录" aria-live="polite" aria-relevant="additions text">{messages.map(message => <li className={`ln-message ${message.senderId === user.id ? "ln-message-self" : ""}`} key={message.id}><div className="ln-message-meta"><strong>{message.senderId === user.id ? "我" : directory.find(person => person.id === message.senderId)?.username ?? message.senderId}</strong><time dateTime={message.createdAt}>{dateLabel(message.createdAt)}</time></div><div className="ln-message-body">{message.expiredAt ? "此消息已超过保留期限" : message.body}{message.fileId && <button className="ln-text-button" onClick={onFiles}>查看群文件</button>}</div></li>)}{pending && <li className="ln-message ln-message-self"><div className="ln-message-meta">我 · {pending.status === "sending" ? "发送中" : "发送失败"}</div><div className="ln-message-body">{pending.body}</div>{pending.error && <ErrorNotice error={pending.error}/>} {pending.status === "failed" && <div className="ln-actions"><button className="ln-button ln-button-secondary" onClick={() => void post(pending)}>重试发送</button><button className="ln-text-button" onClick={() => {
          setDraft(pending.body);
          setPending(null);
        }}>撤回到输入框</button></div>}</li>}</ol>
 {!loading && !messages.length && !pending && !error && <p className="ln-muted ln-chat-empty">暂无消息，发送第一条消息开始协作。</p>}
 </div>
 {hasUnread && <button className="ln-new-messages ln-button ln-button-secondary" onClick={scrollToLatest}>有新消息，回到最新</button>}
 <form className="ln-composer" onSubmit={submit}><label htmlFor={`message-${groupId}`}>消息内容</label><textarea id={`message-${groupId}`} value={draft} maxLength={16000} rows={3} placeholder="输入消息…" onChange={event => setDraft(event.target.value)}/><div className="ln-row-heading"><span className="ln-muted">消息保留两年</span><button className="ln-button ln-button-primary" disabled={!draft.trim() || !!pending}>发送</button></div></form>
</section>;
}

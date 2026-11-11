import { Bell, Check, X, ArrowClockwise } from '@phosphor-icons/react';
import * as Dialog from '@radix-ui/react-dialog';
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { useAuth } from '../auth';
import { apiErrorMessage } from '../api/rest';
import { useDesktop, useDesktopEvent } from './DesktopProvider';
import type { Contacts, Conversation } from '../features/messaging/types';
import '../styles/notifications.css';
interface Notice {id:string;kind:'success'|'error'|'info';title:string;message?:string;createdAt:number;read:boolean;conversationId?:string;destination?:'contacts'}
interface Notices {items:Notice[];toasts:Notice[];markAll:()=>void;clear:()=>void;dismiss:(id:string)=>void;activate:(notice:Notice)=>void;open:boolean;setOpen:(value:boolean)=>void;}
const Context=createContext<Notices|null>(null);
const eventName='linknestx:notice';
export function notify(title:string,kind:Notice['kind']='info',message?:string){window.dispatchEvent(new CustomEvent(eventName,{detail:{kind,title,message}}));}
export function NotificationProvider({children}:{children:ReactNode}) {
 const {session,client,updateUser}=useAuth();const {connection,foreground}=useDesktop();
 const currentForeground=useRef(foreground);currentForeground.current=foreground;
 const [items,setItems]=useState<Notice[]>([]);const [toasts,setToasts]=useState<Notice[]>([]);const [open,setOpen]=useState(false);
 const seen=useRef(new Set<string>());const active=useRef<string|null>(null);const counter=useRef(0);const timers=useRef(new Map<string,ReturnType<typeof setTimeout>>());const live=useRef(true);
 const dismiss=useCallback((id:string)=>{setToasts(old=>old.filter(n=>n.id!==id));clearTimeout(timers.current.get(id));timers.current.delete(id)},[]);
 const add=useCallback((input:Partial<Notice>&Pick<Notice,'title'>,key?:string)=>{
  if(!live.current||!input.title)return;
  if(key){if(seen.current.has(key))return;seen.current.add(key);if(seen.current.size>500)seen.current.delete(seen.current.values().next().value!);}
  const notice:Notice={id:'notice-'+(++counter.current),kind:'info',createdAt:Date.now(),read:false,...input,title:input.title.slice(0,160),message:input.message?.slice(0,400)};
  setItems(old=>[notice,...old].slice(0,100));setToasts(old=>[notice,...old].slice(0,3));
  timers.current.set(notice.id,setTimeout(()=>dismiss(notice.id),notice.kind==='error'?12000:6000));
 },[dismiss]);
 useEffect(()=>{live.current=true;const operation=(e:Event)=>{const value=(e as CustomEvent).detail;if(typeof value?.title==='string'&&['info','success','error'].includes(value.kind))add({title:value.title,kind:value.kind,message:typeof value.message==='string'?value.message:undefined})};const selected=(e:Event)=>{const id=(e as CustomEvent).detail?.id;active.current=typeof id==='string'?id:null};window.addEventListener(eventName,operation);window.addEventListener('linknestx:active-conversation',selected);return()=>{live.current=false;window.removeEventListener(eventName,operation);window.removeEventListener('linknestx:active-conversation',selected);timers.current.forEach(clearTimeout);timers.current.clear()}},[add]);
 const previous=useRef(connection.status);const disconnected=useRef(false);
 useEffect(()=>{if(connection.status===previous.current)return;previous.current=connection.status;if(connection.status==='disconnected'||connection.status==='error'){if(!disconnected.current)add({title:'连接已断开，正在自动重试',message:'消息会在连接恢复后同步。你也可以立即重连。',kind:'error'});disconnected.current=true}else if(connection.status==='connected'&&disconnected.current){add({title:'连接已恢复',message:'已重新连接工作区，正在同步最新消息。',kind:'success'});disconnected.current=false}},[connection.status,add]);
 const profileGeneration=useRef(0); const refreshProfile=()=>{const run=++profileGeneration.current;void client.request<import('../auth/types').User>('GET','/api/v1/me').then(user=>{if(live.current&&run===profileGeneration.current&&user?.id===session?.user.id)updateUser(user)}).catch(()=>{})}; const contactGeneration=useRef(0);const outgoing=useRef(new Map<string,string>());const pendingFriends=useRef(new Map<string,string>());
 const syncContacts=useCallback(async()=>{const version=++contactGeneration.current;try{const contacts=await client.request<Contacts>('GET','/api/v1/contacts');if(!live.current||version!==contactGeneration.current||!contacts)return;for(const friend of contacts.friends??[]){if(pendingFriends.current.has(friend.id)){add({title:'好友申请已通过',message:(friend.displayName||friend.username)+' 已成为你的好友。',destination:'contacts'},'accepted:'+pendingFriends.current.get(friend.id));pendingFriends.current.delete(friend.id)}}const currentPending=new Set((contacts.outgoing??[]).filter(r=>r.status==='pending').map(r=>r.to.id));for(const id of pendingFriends.current.keys()){if(!currentPending.has(id))pendingFriends.current.delete(id)}for(const request of contacts.outgoing??[]){if(request.status==='pending')pendingFriends.current.set(request.to.id,request.id)}for(const request of contacts.incoming??[]){if(request.status==='pending')add({title:'收到好友申请',message:(request.from.displayName||request.from.username)+' 希望添加你为好友。',destination:'contacts'},'friend:'+request.id)}for(const request of contacts.outgoing??[]){if(outgoing.current.get(request.id)==='pending'&&request.status==='accepted')add({title:'好友申请已通过',message:(request.to.displayName||request.to.username)+' 已成为你的好友。',destination:'contacts'},'accepted:'+request.id);outgoing.current.set(request.id,request.status)}}catch{/* Contacts page retains detailed API errors and retry. */}},[client,add]);
 useEffect(()=>{void syncContacts();return()=>{contactGeneration.current++}},[syncContacts]);
 const messageHydration=useRef(0);
 const hydrateUnread=useCallback(async()=>{
  const run=++messageHydration.current;
  try{
   const conversations=await client.request<Conversation[]>('GET','/api/v1/conversations');
   if(!live.current||run!==messageHydration.current||!Array.isArray(conversations))return;
   for(const conversation of conversations){
    // The server excludes our own messages from unreadCount, even when our reply is last.
    if(conversation.unreadCount>0&&conversation.lastMessage?.id&&!(currentForeground.current&&active.current===conversation.id)){
     add({title:'有未读消息',message:conversation.title+' 有 '+conversation.unreadCount+' 条未读消息。',conversationId:conversation.id},'message:'+conversation.lastMessage.id);
    }
   }
  }catch{/* A later session-ready event retries missed-message recovery. */}
 },[client,add]);
 useEffect(()=>{void hydrateUnread();return()=>{messageHydration.current++}},[hydrateUnread]);
 useDesktopEvent('server.event',event=>{
  if(event?.type==='contacts.changed'){void syncContacts();return;}
  if(event?.type==='session.ready'){void syncContacts();void hydrateUnread();refreshProfile();return;}
  if(event?.type==='message.created'&&event.message){const m=event.message;const id=event.conversationId||m.conversationId||m.groupId||event.groupId;if(m.senderId===session?.user.id||(!m.id))return;if(foreground&&active.current===id)return;add({title:m.kind==='system'?'群组动态':'收到新消息',message:m.kind==='system'?m.body:'工作区有新的消息，点击查看。',conversationId:id},'message:'+m.id)}
  if(event?.type==='membership.changed')add({title:'群组成员或权限已更新',message:'会话列表与成员信息已刷新。'},event.eventId?'membership:'+event.eventId:undefined);
  if(event?.type==='profile.changed'&&event.userId===session?.user.id)refreshProfile();
 });
 useDesktopEvent('transfer.updated',task=>{if(task?.id&&['completed','failed'].includes(task.state))add({title:task.state==='completed'?'文件传输已完成':'文件传输失败',kind:task.state==='completed'?'success':'error',message:task.state==='completed'?'可以在传输中心查看已完成任务。':'请在传输中心查看原因并重试。'},'transfer:'+task.id+':'+task.state)});
 useDesktopEvent('service.error',payload=>add({title:'后台服务需要处理',kind:'error',message:'请查看工作区中的详细提示并重试。'},'service:'+apiErrorMessage(payload)));
 const activate=(notice:Notice)=>{setItems(old=>old.map(n=>n.id===notice.id?{...n,read:true}:n));dismiss(notice.id);setOpen(false);if(notice.conversationId)window.dispatchEvent(new CustomEvent('linknestx:open-conversation',{detail:{id:notice.conversationId}}));else if(notice.destination)window.dispatchEvent(new CustomEvent('linknestx:navigate',{detail:{page:notice.destination}}));};
 return <Context.Provider value={{items,toasts,dismiss,open,setOpen,activate,markAll:()=>setItems(old=>old.map(n=>({...n,read:true}))),clear:()=>{setItems([]);setToasts([])}}}>{children}</Context.Provider>;
}
export function NotificationCenter(){const value=useContext(Context);if(!value)return null;const {items,open,setOpen}=value;const unread=items.filter(n=>!n.read).length;return <Dialog.Root open={open} onOpenChange={setOpen}><Dialog.Trigger asChild><button className="ln-header-settings ln-notification-trigger" aria-label={'系统通知'+(unread?'，'+unread+' 条未看':'')}><Bell size={20}/>{unread>0&&<b>{unread>99?'99+':unread}</b>}</button></Dialog.Trigger><Dialog.Portal><Dialog.Overlay className="ln-notification-overlay"/><Dialog.Content className="ln-notification-panel"><div className="ln-notification-heading"><div><Dialog.Title>系统通知</Dialog.Title><Dialog.Description>当前登录期间的消息、好友与操作提醒</Dialog.Description></div><Dialog.Close asChild><button className="ln-text-button" aria-label="关闭系统通知"><X size={20}/></button></Dialog.Close></div><div className="ln-notification-actions"><button onClick={value.markAll} disabled={!unread}><Check size={16}/>全部标为已看</button><button onClick={value.clear} disabled={!items.length}>清空通知</button></div>{!items.length?<div className="ln-notification-empty"><Bell size={36}/><h3>暂时没有新通知</h3><p>新的动态会显示在这里。</p></div>:<ol className="ln-notification-list">{items.map(n=><li key={n.id} data-kind={n.kind} data-read={n.read}><button onClick={()=>value.activate(n)}><strong>{n.title}</strong>{n.message&&<span>{n.message}</span>}<time>{new Date(n.createdAt).toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit'})}</time></button></li>)}</ol>}</Dialog.Content></Dialog.Portal></Dialog.Root>}
export function NotificationToasts(){const value=useContext(Context);if(!value||value.open)return null;return <div className="ln-toast-stack" aria-label="操作提示">{value.toasts.map(n=><div key={n.id} className={'ln-toast ln-toast-'+n.kind} role={n.kind==='error'?'alert':'status'}><div><strong>{n.title}</strong>{n.message&&<p>{n.message}</p>}{(n.conversationId||n.destination)&&<button className="ln-text-button" onClick={()=>value.activate(n)}>查看</button>}</div><button aria-label="关闭提示" onClick={()=>value.dismiss(n.id)}><X size={16}/></button></div>)}</div>}
export function ConnectionBanner(){const {connection,reconnect}=useDesktop();const [busy,setBusy]=useState(false);if(connection.status==='connected'||connection.status==='connecting')return null;return <div className="ln-connection-banner"><span>实时连接暂不可用，正在自动重试。</span><button disabled={busy} onClick={()=>{setBusy(true);void reconnect().catch(e=>notify('重连失败','error',apiErrorMessage(e))).finally(()=>setBusy(false))}}><ArrowClockwise size={16}/>{busy?'正在重连…':'立即重连'}</button></div>}

export function useNotificationPanelOpen(){return useContext(Context)?.open??false;}

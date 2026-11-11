import { useCallback, useEffect, useRef, useState } from 'react';
import { apiErrorMessage, type RestClient } from '../../api/rest';
import { useDesktopEvent } from '../../runtime/DesktopProvider';
import { ErrorNotice, Loading, segment } from '../shared';
import { Avatar, displayName } from './Profile';
import type { Profile } from './types';
type Reader=Profile&{readAt:string};
export function ReadersDialog({client,conversationId,messageId,onClose}:{client:RestClient;conversationId:string;messageId:string;onClose:()=>void}){
 const [people,setPeople]=useState<Reader[]|null>(null);const [error,setError]=useState('');
 const panel=useRef<HTMLElement>(null);const closeButton=useRef<HTMLButtonElement>(null);const requestVersion=useRef(0);
 const load=useCallback(async()=>{
  const version=++requestVersion.current;setError('');setPeople(null);
  try{const result=await client.request<{readers:Reader[];count:number}>('GET','/api/v1/conversations/'+segment(conversationId)+'/messages/'+segment(messageId)+'/readers');if(requestVersion.current===version)setPeople(result.readers)}
  catch(e){if(requestVersion.current===version)setError(apiErrorMessage(e))}
 },[client,conversationId,messageId]);
 useEffect(()=>{const previous=document.activeElement as HTMLElement|null;closeButton.current?.focus();void load();return()=>{requestVersion.current++;if(previous?.isConnected)previous.focus()}},[load]);
 useDesktopEvent('server.event',event=>{if(event.type==='session.ready'||(['read.changed','membership.changed','profile.changed'].includes(event.type)&&(!event.groupId&&!event.conversationId||event.groupId===conversationId||event.conversationId===conversationId)))void load()});
 return <div className="im-modal-shade" onMouseDown={e=>{if(e.target===e.currentTarget)onClose()}}><section ref={panel} className="im-reader-dialog" role="dialog" aria-modal="true" aria-labelledby="reader-dialog-title" aria-describedby="reader-dialog-description" tabIndex={-1} onKeyDown={e=>{
  if(e.key==='Escape'){e.stopPropagation();onClose()}
  if(e.key==='Tab'){const controls=panel.current?.querySelectorAll<HTMLElement>('button:not(:disabled),a[href],input:not(:disabled),[tabindex="0"]');if(!controls?.length)return;const first=controls[0],last=controls[controls.length-1];if(e.shiftKey&&(document.activeElement===first||document.activeElement===panel.current)){e.preventDefault();last.focus()}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus()}}
 }}><div className="ln-row-heading"><h3 id="reader-dialog-title">已读成员</h3><button ref={closeButton} className="ln-text-button" onClick={onClose}>关闭</button></div><p id="reader-dialog-description" className="ln-muted">当前有权访问此消息的成员，不包含发送者。</p><ErrorNotice error={error} retry={()=>void load()}/>{!people&&!error&&<div role="status" aria-label="正在加载已读成员"><Loading/></div>}{people?.length===0&&<p role="status">暂时无人已读。</p>}{people&&people.length>0&&<><p className="ln-muted" role="status">{people.length} 人已读</p><ul className="im-readers-list">{people.map(person=><li key={person.id} className="im-contact-row"><Avatar profile={person}/><div><strong>{displayName(person)}</strong><small>{person.accountNumber} · <time dateTime={person.readAt}>{new Date(person.readAt).toLocaleString('zh-CN')}</time></small></div></li>)}</ul></>}</section></div>
}

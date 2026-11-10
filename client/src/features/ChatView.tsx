import { useRef, useEffect, useState } from 'react';
import type { RestClient } from '../api/rest';
import type { User } from '../auth';
import { useDesktop } from '../runtime/DesktopProvider';
import { ConversationView } from './messaging/ConversationView';
import { ProfilePanel } from './messaging/Profile';
import type { ComposerDraft, Profile } from './messaging/types';
import type { DirectoryUser } from './GroupsView';
export function ChatView({client,groupId,user,active,onFiles}:{client:RestClient;groupId:string;user:User;directory:DirectoryUser[];active:boolean;onFiles:()=>void}){const {desktop}=useDesktop();const [draft,setDraft]=useState<ComposerDraft>({text:'',attachments:[]});const [profile,setProfile]=useState<Profile|null>(null);const draftRef=useRef(draft);draftRef.current=draft;useEffect(()=>()=>{void Promise.allSettled(draftRef.current.attachments.map(a=>desktop.invoke('discard_attachment',{sourceHandle:a.id})))},[desktop]);return <><ConversationView conversation={{id:groupId,kind:'group',title:'群聊',memberCount:0,canSend:true,unreadCount:0}} client={client} me={user} active={active} obscured={!!profile} draft={draft} onDraft={setDraft} onProfile={setProfile} onBack={()=>{}} onFiles={onFiles} onChanged={()=>{}}/>{profile&&<ProfilePanel profile={profile} me={user} client={client} onClose={()=>setProfile(null)} onDirect={async userId=>{const conversation=await client.request<{id:string}>("POST","/api/v1/conversations/direct",{userId});window.dispatchEvent(new CustomEvent("linknestx:open-conversation",{detail:{id:conversation.id}}));setProfile(null)}} onChanged={()=>{}}/>}</>}

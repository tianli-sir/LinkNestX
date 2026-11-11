import { Users, User } from '@phosphor-icons/react';
import { Avatar, displayName } from './Profile';
import type { Conversation, Profile } from './types';
export function ConversationAvatar({conversation,onProfile}:{conversation:Conversation;onProfile?:(profile:Profile)=>void}) {
 if(conversation.kind==='group')return <span className="im-avatar im-group-avatar" aria-hidden="true"><Users size={25} weight="duotone"/></span>;
 if(conversation.peer)return <Avatar profile={conversation.peer} onClick={onProfile?()=>onProfile(conversation.peer!):undefined}/>;
 return <span className="im-avatar" aria-hidden="true"><User size={24}/></span>;
}
export function ConversationBadge({kind}:{kind:Conversation['kind']}){return <span className="im-conversation-badge">{kind==='group'?'群聊':'私聊'}</span>}
export function conversationPreview(conversation:Conversation,meId:string){
 const message=conversation.lastMessage;if(!message)return '开始沟通';if(message.expiredAt)return '消息已过期';
 const body=message.fileId?'[文件] '+(message.attachmentName??''):message.body??'';
 if(message.kind==='system')return '[系统] '+body;
 const sender=message.senderId===meId?'我':conversation.kind==='group'?displayName(message.sender):'';
 return sender?sender+'：'+body:body;
}

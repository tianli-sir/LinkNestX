import type { User } from '../../auth';
export type Profile=User;
export interface Conversation {id:string;kind:'group'|'direct';title:string;avatarUrl?:string;peer?:Profile;memberCount:number;unreadCount:number;lastMessage?:Message;canSend:boolean}
export interface Message {id:string;sequence:number;groupId:string;conversationId:string;senderId:string;sender:Profile;clientMessageId:string;body:string|null;createdAt:string;expiredAt?:string;cursor:string;fileId?:string;fileVersionId?:string;attachmentName?:string;attachmentSize?:number;attachmentStatus?:'available'|'expired'|'deleted'|'unlisted';readCount:number;readByMe:boolean}
export interface FriendRequest {id:string;from:Profile;to:Profile;status:string;createdAt:string}
export interface Contacts {friends:Profile[];incoming:FriendRequest[];outgoing:FriendRequest[]}
export interface AttachmentDraft {id:string;name:string;size:number;mimeType?:string;clientAttachmentId:string;error?:string}
export interface PendingText {id:string;body:string;failed:boolean}
export interface ComposerDraft {text:string;attachments:AttachmentDraft[];pending?:PendingText|null}
export type DraftUpdate=ComposerDraft|((previous:ComposerDraft)=>ComposerDraft);

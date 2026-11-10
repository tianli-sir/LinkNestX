export interface DocumentAttachment {id:string;documentId:string;name:string;currentVersionId:string;versionId:string;size:number;createdAt:string;status:string}
export interface DocumentRecord {id:string;title:string;body:string;bodyTruncated?:boolean;visibility:'private'|'friends'|'team';status:'draft'|'published';curated:boolean;revision:number;author:{id:string;username:string;displayName?:string;accountNumber:string;avatarUrl?:string};createdAt:string;updatedAt:string;publishedAt?:string;canEdit:boolean;canDelete:boolean;canCurate:boolean;attachments:DocumentAttachment[]}
export interface DocumentPage {documents:DocumentRecord[];nextCursor:string;hasMore:boolean}
export type DocumentScope='feed'|'mine'|'drafts'|'public';

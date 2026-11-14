'use strict';
const path = require('node:path');
const COMMANDS = Object.freeze({
  app_version: [], client_configuration: [], get_background_status: [], get_connection_status: [], reconnect_connection: [],
  open_external_url: ['url'],
  login: ['username','password','deviceId'], logout: [], session_restore: [],
  api_request: ['method','path','body'], send_server_event: ['event'],
  list_transfers: [], pause_transfer:['taskId'], resume_transfer:['taskId'],
  pick_upload_files: [], read_clipboard_attachments: [], discard_attachment:['sourceHandle'],
  pick_download_destination:['name'], start_download:['versionId','destination','groupId','messageId'],
  start_attachment_upload:['groupId','sourceHandle','clientAttachmentId','fileId'],
  start_document_attachment_upload:['documentId','sourceHandle','clientAttachmentId','fileId'],
  document_attachment_preview:['documentId','versionId'],
  file_attachment_preview:['versionId'], file_attachment_thumb:['versionId'],
  open_downloaded_file:['taskId'], reveal_downloaded_file:['taskId'], check_downloaded_files:['taskIds'],
  set_profile_avatar:['sourceHandle'], profile_avatar:['userId'],
  get_transfer_preferences:[], set_transfer_preferences:['preferences'],
  reserve_audit_export:[],export_audit:['exportId','format','action'],cancel_audit_export:['exportId'],
  get_update_status:[],check_for_updates:[],defer_update:[],install_ready_update:[],
});
const MAX_REQUEST_BYTES=3*1024*1024;
function validateRequest(command,args={}) {
  if (!Object.hasOwn(COMMANDS,command)) throw new Error('不支持的桌面操作');
  if (!args || typeof args!=='object' || Array.isArray(args) || Object.keys(args).some(k=>!COMMANDS[command].includes(k))) throw new Error('桌面参数无效');
  const serialized=JSON.stringify(args);
  if (Buffer.byteLength(serialized)>MAX_REQUEST_BYTES-1024) throw new Error('请求过大');
  if(command==='api_request') {
    if(!['GET','POST','PUT','PATCH','DELETE'].includes(args.method)||typeof args.path!=='string'||!args.path.startsWith('/api/v1/'))throw new Error('请求无效');
    const route=args.path.split('?')[0].replace(/\/$/,'');
    if(/[\\%#\x00-\x1f]/.test(route)||route.includes('//')||route.split('/').some(x=>x==='.'||x==='..')||['/api/v1/auth/login','/api/v1/auth/logout','/api/v1/auth/refresh'].includes(route))throw new Error('请求路径无效');
  }
  // Id arrays must be exactly that: a bounded array of identifier strings. The
  // renderer can never smuggle a path or coerce the native bulk check.
  if(command==='check_downloaded_files') {
    if(!Array.isArray(args.taskIds)||args.taskIds.length>200||args.taskIds.some(x=>typeof x!=='string'||!x.length||x.length>128))throw new Error('请求无效');
  }
  return JSON.parse(serialized);
}
function safeAsset(value,root) {
  const url=new URL(value);
  if(url.protocol!=='linknestx-legacy:'||url.hostname!=='app'||url.username||url.password)throw new Error('非法资源地址');
  const pathname=decodeURIComponent(url.pathname);
  if(pathname.includes('\\')||pathname.includes('\0')||pathname.split('/').includes('..'))throw new Error('非法资源路径');
  const relative=pathname==='/'?'index.html':pathname.slice(1);
  const base=path.resolve(root);const result=path.resolve(base,relative);
  if(!result.startsWith(base+path.sep))throw new Error('资源不在应用内');
  return result;
}
function mayOpenExternal(value) {
  if(typeof value!=='string'||value.length>8192||/[\x00-\x20\x7f\\]/.test(value))return false;
  try {
    const u=new URL(value);
    if(u.username||u.password)return false;
    if(['http:','https:'].includes(u.protocol))return !!u.hostname;
    if(u.protocol!=='mailto:'||u.host||u.hash)return false;
    const recipients=decodeURIComponent(u.pathname).split(',');
    return recipients.length>0&&recipients.length<=20&&recipients.every(x=>/^[^\s<>@]+@[^\s<>@]+\.[^\s<>@]+$/.test(x))&&[...u.searchParams.keys()].every(k=>['subject','body'].includes(k.toLowerCase()));
  }catch{return false;}
}
function rendererSession(session){return {id:session.id,user:session.user,deviceId:session.deviceId,expiresAt:session.expiresAt};}
module.exports={COMMANDS,MAX_REQUEST_BYTES,validateRequest,safeAsset,mayOpenExternal,rendererSession};

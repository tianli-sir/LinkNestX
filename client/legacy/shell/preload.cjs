'use strict';
const {contextBridge,ipcRenderer}=require('electron');
const commands=new Set(['app_version','client_configuration','get_background_status','get_connection_status','reconnect_connection','login','logout','api_request','send_server_event','list_transfers','pause_transfer','resume_transfer','pick_upload_files','read_clipboard_attachments','discard_attachment','pick_download_destination','start_download','start_attachment_upload','start_document_attachment_upload','document_attachment_preview','file_attachment_preview','file_attachment_thumb','open_downloaded_file','reveal_downloaded_file','check_downloaded_files','set_profile_avatar','profile_avatar','get_transfer_preferences','set_transfer_preferences','reserve_audit_export','export_audit','cancel_audit_export','get_update_status','check_for_updates','defer_update','install_ready_update']);
const events=new Set(['server.event','connection.changed','transfer.updated','service.error','sharing.changed','sharing.revoked','sharing.activity','peer.sent','service.status','attachments.prepared','update.status','window.foreground']);
commands.add('open_external_url');
contextBridge.exposeInMainWorld('__LINKNESTX_DESKTOP__',Object.freeze({
  kind:'legacy',
  invoke:async(command,args={})=>{if(!commands.has(command))throw new Error('不支持的桌面操作');return ipcRenderer.invoke('desktop:invoke',command,args);},
  listen:async(event,callback)=>{
    if(!events.has(event)||typeof callback!=='function')throw new Error('无效的事件订阅');
    const handler=(_event,name,payload)=>{if(name===event)callback(payload);};
    ipcRenderer.on('desktop:event',handler);
    if(event==='window.foreground')callback(await ipcRenderer.invoke('desktop:foreground'));
    return()=>ipcRenderer.removeListener('desktop:event',handler);
  },
}));
// The isolated preload consumes OS-created File objects. Renderer code has no
// method that accepts local paths; synthetic DOM drops cannot authorize reads.
window.addEventListener('dragover',event=>{if(event.isTrusted&&event.dataTransfer?.types.includes('Files'))event.preventDefault();},true);
window.addEventListener('drop',event=>{
  if(!event.isTrusted||!event.dataTransfer?.files.length)return;
  event.preventDefault();event.stopImmediatePropagation();
  const paths=Array.from(event.dataTransfer.files,file=>file.path).filter(value=>typeof value==='string'&&value.length);
  if(paths.length&&paths.length<=20)ipcRenderer.send('desktop:trusted-drop',paths);
},true);

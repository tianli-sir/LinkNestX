import { useEffect, useState, type ReactNode } from 'react';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { DownloadSimple } from '@phosphor-icons/react';
import { useDesktop } from '../../runtime/DesktopProvider';
import { apiErrorMessage } from '../../api/rest';
import { ErrorNotice, segment } from '../shared';
import type { DocumentAttachment, DocumentRecord } from './types';
const scheme='linknestx-file:';
export function safeExternalUrl(value: string): string {
  // Reject characters that URL parsers can silently strip or reinterpret.
  if (value.length > 4096 || /[\u0000-\u0020\u007f-\u009f\\]/.test(value)) {
    return '';
  }

  try {
    // Encoded spaces are useful in subjects and bodies; encoded control
    // characters and backslashes must not reach an external application.
    if (/[\u0000-\u001f\u007f-\u009f\\]/.test(decodeURIComponent(value))) {
      return '';
    }

    const url = new URL(value);
    if (url.username || url.password || url.href.length > 4096) {
      return '';
    }

    if (url.protocol === 'https:' || url.protocol === 'http:') {
      return url.hostname ? url.href : '';
    }

    if (url.protocol !== 'mailto:' || url.host || url.hash) {
      return '';
    }

    // Keep mail links within the common safe subset of both desktop hosts:
    // plain recipients and the two explicitly supported message fields.
    const recipients = url.pathname.split(',');
    const ordinaryAddress = /^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z0-9-]+$/;
    if (recipients.length > 20 || !recipients.every(address => ordinaryAddress.test(address))) {
      return '';
    }

    for (const key of url.searchParams.keys()) {
      if (key !== 'subject' && key !== 'body') {
        return '';
      }
    }

    return url.href;
  } catch {
    return '';
  }
}
export function findAttachment(document:DocumentRecord,url:string){return url.startsWith(scheme)?document.attachments.find(file=>file.versionId===url.slice(scheme.length)):undefined}
export function AttachmentDownload({attachment,children}:{attachment:DocumentAttachment;children?:ReactNode}){
 const {desktop,capabilities}=useDesktop();const [busy,setBusy]=useState(false);const [error,setError]=useState('');const [notice,setNotice]=useState('');
 const tooLarge=!capabilities.nativeTransfers&&attachment.size>capabilities.maxDownloadBytes;
 async function download(){if(busy||tooLarge)return;setBusy(true);setError('');try{if(capabilities.nativeTransfers){const destination=await desktop.invoke<string|null>('pick_download_destination',{name:attachment.name});if(destination)await desktop.invoke('start_download',{versionId:attachment.versionId,destination})}else{const path=`/api/v1/web/files/${segment(attachment.versionId)}/download`;const check=await fetch(path,{method:'HEAD',credentials:'same-origin'});if(!check.ok)throw new Error(check.status===413?'超过网页版 1 GB 上限，请使用客户端':check.status===409?'已有下载进行中，请稍后重试':'附件不可下载，请刷新文档后重试');const a=window.document.createElement('a');a.href=path;a.download=attachment.name;window.document.body.append(a);a.click();a.remove();setNotice('已交给浏览器下载。')}}catch(error){setError(apiErrorMessage(error))}finally{setBusy(false)}}
 return <span className="doc-download"><button type="button" className="ln-text-button" disabled={busy||tooLarge} onClick={()=>void download()}><DownloadSimple size={15}/>{tooLarge?'超过 1 GB，请使用客户端':busy?'准备下载…':children??attachment.name}</button>{error&&<span role="alert" className="ln-field-error">{error}</span>}{notice&&<span role="status" className="ln-muted">{notice}</span>}</span>;
}
function AttachmentImage({document,attachment,alt}:{document:DocumentRecord;attachment:DocumentAttachment;alt:string}){
 const {desktop,capabilities}=useDesktop();const [src,setSrc]=useState('');const [failed,setFailed]=useState(false);
 useEffect(()=>{let stopped=false;setSrc('');setFailed(false);if(!capabilities.nativeTransfers){setSrc(`/api/v1/documents/${segment(document.id)}/attachments/${segment(attachment.versionId)}/preview`)}else{desktop.invoke<string>('document_attachment_preview',{documentId:document.id,versionId:attachment.versionId}).then(value=>{if(!stopped){if(/^data:image\/(png|jpeg|gif|webp);base64,/i.test(value))setSrc(value);else setFailed(true)}}).catch(()=>{if(!stopped)setFailed(true)})}return()=>{stopped=true}},[document.id,attachment.versionId,capabilities.nativeTransfers,desktop]);
 return failed?<span className="doc-image-unavailable">图片暂不可预览 · <AttachmentDownload attachment={attachment}/></span>:src?<img src={src} alt={alt} loading="lazy" onError={()=>setFailed(true)}/>:<span role="status">正在读取图片…</span>;
}
export function SafeMarkdown({document,body}:{document:DocumentRecord;body:string}){
 const {desktop,capabilities}=useDesktop();const [error,setError]=useState('');
 return <div className="doc-markdown"><ErrorNotice error={error}/><Markdown skipHtml remarkPlugins={[remarkGfm]} urlTransform={url=>findAttachment(document,url)?url:safeExternalUrl(url)} components={{
  a:({href,children})=>{if(!href)return <span>{children}</span>;const file=findAttachment(document,href);if(file)return <AttachmentDownload attachment={file}>{children}</AttachmentDownload>;const url=safeExternalUrl(href);if(!url)return <span>{children}</span>;return <a href={url} target="_blank" rel="noopener noreferrer" onClick={event=>{if(capabilities.nativeTransfers){event.preventDefault();void desktop.invoke('open_external_url',{url}).catch(e=>setError(apiErrorMessage(e)))}}}>{children}</a>},
  img:({src,alt})=>{const file=typeof src==='string'?findAttachment(document,src):undefined;return file?<AttachmentImage key={file.versionId} document={document} attachment={file} alt={alt??file.name}/>:<span className="doc-image-unavailable">{alt||'图片'} · 仅显示本文附件图片</span>},
  table:({children})=><div className="doc-table-scroll"><table>{children}</table></div>
 }}>{body}</Markdown></div>;
}

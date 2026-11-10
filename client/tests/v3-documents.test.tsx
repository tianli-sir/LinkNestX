import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { DesktopProvider, type DesktopAdapter } from '../src/runtime/DesktopProvider';
import { SafeMarkdown } from '../src/features/documents/SafeMarkdown';
import { DocumentsView } from '../src/features/documents/DocumentsView';
import type { DocumentRecord } from '../src/features/documents/types';
import type { RestClient } from '../src/api/rest';
afterEach(cleanup);
const native={kind:'native',invoke:vi.fn(async()=>null),listen:async()=>()=>{}} as DesktopAdapter;
const browser={...native,kind:'browser'} as DesktopAdapter;
const note:DocumentRecord={id:'doc-1',title:'交付指南',body:'原文',visibility:'team',status:'published',curated:false,revision:1,author:{id:'u1',username:'lin',displayName:'林',accountNumber:'1001'},createdAt:'2026-11-10T10:00:00Z',updatedAt:'2026-11-10T10:00:00Z',canEdit:true,canDelete:true,canCurate:true,attachments:[]};
function mount(element:React.ReactNode,adapter=browser){return render(<DesktopProvider adapter={adapter}>{element}</DesktopProvider>)}
it('does not render raw HTML, external images or attachments from another document',()=>{
 mount(<SafeMarkdown document={note} body={'<img src=x onerror=alert(1)>\n\n![tracking](https://evil.example/pixel)\n\n![ чужой](linknestx-file:other)\n\n[bad](javascript:alert(1))\n\n[ok](https://example.com)'}/>);
 expect(document.querySelectorAll('img')).toHaveLength(0);
 expect(screen.getByText('bad').closest('a')).toBeNull();
 expect(screen.getByRole('link',{name:'ok'})).toHaveAttribute('rel','noopener noreferrer');
});
it('opens native external links through the safe host command',()=>{
 const invoke=vi.fn(async()=>null);mount(<SafeMarkdown document={note} body={'[参考](https://example.com)'}/>,{...native,invoke} as DesktopAdapter);
 fireEvent.click(screen.getByRole('link',{name:'参考'}));expect(invoke).toHaveBeenCalledWith('open_external_url',{url:'https://example.com/'});
});
it('allows Web draft text editing but never exposes attachment upload or curation',async()=>{
 const request=vi.fn(async(method,path,body)=>method==='POST'?{...note,...body,id:'new-1'}:(/\/documents\/[^?]+$/.test(path))?note:{documents:[note],hasMore:false,nextCursor:''});
 mount(<DocumentsView client={{request} as unknown as RestClient}/>);
 fireEvent.click(screen.getByRole('button',{name:'写文档'}));
 fireEvent.change(await screen.findByLabelText('文档标题'),{target:{value:'新指南'}});fireEvent.change(screen.getByLabelText('Markdown 正文'),{target:{value:'# 流程'}});
 expect(screen.queryByRole('button',{name:'添加附件'})).toBeNull();
 fireEvent.click(screen.getByRole('button',{name:'保存草稿'}));
 await waitFor(()=>expect(request).toHaveBeenCalledWith('POST','/api/v1/documents',{title:'新指南',body:'# 流程',visibility:'private',status:'draft'}));
});
it('preserves edits after a revision conflict and requires an explicit choice before retrying',async()=>{
 let attempt=0;const request=vi.fn(async(method:string,path:string,body?:any)=>{if(method==='PATCH'){attempt++;if(attempt===1)throw new Error('文档已更新');return {...note,...body,revision:3}}if(path==='/api/v1/documents/doc-1')return {...note,revision:attempt?2:1,body:attempt?'同事修改':'原文'};return {documents:[note],hasMore:false,nextCursor:''}});
 mount(<DocumentsView client={{request} as unknown as RestClient}/>);
 fireEvent.click(await screen.findByRole('button',{name:'阅读 交付指南'}));fireEvent.click(await screen.findByRole('button',{name:'编辑文档'}));
 fireEvent.change(screen.getByLabelText('Markdown 正文'),{target:{value:'我的修改'}});fireEvent.click(screen.getByRole('button',{name:'保存修改'}));
 expect(await screen.findByText('保存未完成，你的内容仍保留在编辑器中。')).toBeVisible();
 expect(screen.getByLabelText('Markdown 正文')).toHaveValue('我的修改');
 fireEvent.click(screen.getByRole('button',{name:'查看最新版本'}));await screen.findByText('同事修改');
 fireEvent.click(screen.getByRole('button',{name:'保留我的内容，以最新版本为基础'}));fireEvent.click(screen.getByRole('button',{name:'保存修改'}));
 await waitFor(()=>expect(request).toHaveBeenLastCalledWith('GET',expect.stringContaining('/api/v1/documents?')));
 expect(request.mock.calls.some(([method,,body])=>method==='PATCH'&&body.revision===2&&body.body==='我的修改')).toBe(true);
});
it('keeps a draft when moving to another page and asks before discarding it within documents',async()=>{
 const request=vi.fn(async()=>({documents:[],hasMore:false,nextCursor:''}));const client={request} as unknown as RestClient;
 const view=mount(<DocumentsView client={client}/>);fireEvent.click(screen.getByRole('button',{name:'写文档'}));fireEvent.change(await screen.findByLabelText('文档标题'),{target:{value:'未保存的思考'}});
 view.rerender(<DesktopProvider adapter={browser}><DocumentsView client={client} active={false}/></DesktopProvider>);view.rerender(<DesktopProvider adapter={browser}><DocumentsView client={client} active/></DesktopProvider>);
 expect(screen.getByLabelText('文档标题')).toHaveValue('未保存的思考');fireEvent.click(screen.getByRole('button',{name:'公共文档'}));expect(screen.getByRole('dialog',{name:'离开编辑？'})).toBeVisible();fireEvent.click(within(screen.getByRole('dialog',{name:'离开编辑？'})).getByRole('button',{name:'取消'}));expect(screen.getByLabelText('文档标题')).toHaveValue('未保存的思考');
});
it('never offers administrator edits to a Web reader of a curated document',async()=>{
 const curated={...note,curated:true};const request=vi.fn(async(_method,path)=>path.includes('?')?{documents:[curated],hasMore:false,nextCursor:''}:curated);
 mount(<DocumentsView client={{request} as unknown as RestClient}/>);fireEvent.click(await screen.findByRole('button',{name:'阅读 交付指南'}));await screen.findByText('团队公共文档');expect(screen.queryByRole('button',{name:'编辑文档'})).toBeNull();expect(screen.queryByRole('button',{name:'删除文档'})).toBeNull();expect(screen.queryByRole('button',{name:'移出公共文档'})).toBeNull();
});
it('queues a trusted attachment on the saved document and discards its temporary handle',async()=>{
 const request=vi.fn(async(_method,path)=>path.includes('?')?{documents:[note],hasMore:false,nextCursor:''}:note);
 const invoke=vi.fn(async(command)=>command==='pick_upload_files'?[{id:'trusted-handle',name:'guide.pdf'}]:null);
 mount(<DocumentsView client={{request} as unknown as RestClient}/>,{...native,invoke} as DesktopAdapter);
 fireEvent.click(await screen.findByRole('button',{name:'阅读 交付指南'}));fireEvent.click(await screen.findByRole('button',{name:'编辑文档'}));fireEvent.click(screen.getByRole('button',{name:'添加附件'}));
 await waitFor(()=>expect(invoke).toHaveBeenCalledWith('start_document_attachment_upload',expect.objectContaining({documentId:'doc-1',sourceHandle:'trusted-handle'})));expect(invoke).toHaveBeenCalledWith('discard_attachment',{sourceHandle:'trusted-handle'});
});
it('uses the immutable attachment version and blocks oversized browser downloads',()=>{
 const attachment={id:'file',documentId:'doc-1',name:'archive.zip',versionId:'old-version',currentVersionId:'new-version',size:1_000_000_001,createdAt:'',status:'published'};
 mount(<SafeMarkdown document={{...note,attachments:[attachment]}} body={'[older](linknestx-file:old-version)\n\n[unlisted](linknestx-file:new-version)'}/>);
 expect(screen.getByRole('button',{name:'超过 1 GB，请使用客户端'})).toBeDisabled();expect(screen.getByText('unlisted').closest('a')).toBeNull();
});

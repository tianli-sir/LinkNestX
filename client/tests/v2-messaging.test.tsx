import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { App } from '../src/main';
const me={id:'u1',username:'lin',displayName:'林晓',accountNumber:'10000001',email:'',roles:['employee'],disabled:false,mustChangePassword:false};
const peer={...me,id:'u2',username:'zhou',displayName:'周予',accountNumber:'10000002'};
const conversation={id:'c1',kind:'direct',title:'周予',peer,memberCount:2,unreadCount:0,canSend:true};
function setup(options:{messages?:any[];browser?:boolean;unread?:number}={}){let friends:any[]=[];let outgoing:any[]=[];let unread=options.unread??0;let incoming:any[]=[{id:'r1',from:peer,to:me,status:'pending'}];const listeners=new Map<string,(value:any)=>void>();const request=vi.fn(async(method:string,path:string,body?:any):Promise<any>=>{if(path==='/api/v1/conversations')return [{...conversation,unreadCount:unread}];if(path==='/api/v1/contacts')return {friends,incoming,outgoing};if(path==='/api/v1/friend-requests/r1/accept'){friends=[peer];incoming=[];return {id:'r1',status:'accepted'}}if(path.startsWith('/api/v1/profiles/'))return peer;if(path.startsWith('/api/v1/contacts/search'))return peer;if(path==='/api/v1/friend-requests'){const request={id:'r2',from:me,to:peer,status:'pending'};outgoing=[request];return request;}if(path==='/api/v1/conversations/direct')return conversation;if(path.endsWith('/read')){unread=0;return {messageIds:body.messageIds}}if(path.includes('/messages?'))return {messages:options.messages??[],hasMore:false,nextCursor:''};throw new Error(path)});const invoke=vi.fn(async(name:string):Promise<any>=>name==='list_transfers'?[]:name==='pick_upload_files'?[{id:'trusted-handle',name:'草案.pdf',size:2000}]:name==='start_attachment_upload'?'task1':undefined);const view=render(<App transport={{request,login:async()=>({id:'session',user:me,deviceId:'d',expiresAt:''}),logout:async()=>{}}} desktop={{kind:options.browser?'browser':'native',invoke,listen:async(name,callback)=>{listeners.set(name,callback);return()=>listeners.delete(name)}}}/>);return {view,request,invoke,listeners,approve:()=>{friends=[peer];incoming=[];outgoing=[]}}}
async function login(){fireEvent.change(screen.getByLabelText('账号'),{target:{value:'10000001'}});fireEvent.change(screen.getByLabelText('密码'),{target:{value:'pw'}});fireEvent.click(screen.getByRole('button',{name:'登录'}));await screen.findByRole('button',{name:'周予'});}
test('accepting a friend request exposes direct messaging only after server acceptance',async()=>{const h=setup();await login();fireEvent.click(screen.getByRole('button',{name:'联系人'}));fireEvent.click(await screen.findByRole('button',{name:'接受'}));await screen.findByText('10000002');fireEvent.click(screen.getByRole('button',{name:'发送消息'}));await waitFor(()=>expect(h.request).toHaveBeenCalledWith('POST','/api/v1/conversations/direct',{userId:'u2'}));});
test('selected native attachments stay drafts until Send and use handles not paths',async()=>{const h=setup();await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));fireEvent.click(await screen.findByRole('button',{name:'添加文件'}));await screen.findByText('草案.pdf');expect(h.invoke).not.toHaveBeenCalledWith('start_attachment_upload',expect.anything());fireEvent.click(screen.getByRole('button',{name:'发送'}));await waitFor(()=>expect(h.invoke).toHaveBeenCalledWith('start_attachment_upload',expect.objectContaining({groupId:'c1',sourceHandle:'trusted-handle',clientAttachmentId:expect.any(String)})));expect(h.invoke.mock.calls.some(([name])=>name==='start_upload')).toBe(false);});
const message=(id:string,extra={})=>({id,sequence:Number(id.slice(1)),conversationId:'c1',groupId:'c1',senderId:peer.id,sender:peer,clientMessageId:id,body:`消息 ${id}`,createdAt:'2026-11-10T06:00:00Z',cursor:id,readCount:0,readByMe:false,...extra});
test('only actually visible message IDs are read, and navigating away cancels dwell timers',async()=>{
 const observers:Array<{callback:IntersectionObserverCallback;nodes:Element[]}>=[];
 vi.spyOn(document,'hasFocus').mockReturnValue(true);
 vi.stubGlobal('IntersectionObserver',class{nodes:Element[]=[];constructor(callback:IntersectionObserverCallback){observers.push({callback,nodes:this.nodes})}observe(node:Element){this.nodes.push(node)}disconnect(){}unobserve(){}});
 const h=setup({messages:[message('m1'),message('m2'),message('m3')]});await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));await screen.findByText('消息 m3');
 vi.useFakeTimers();const latest=observers.at(-1)!;await act(async()=>{latest.callback(latest.nodes.filter(node=>['m1','m3'].includes((node as HTMLElement).dataset.readId!)).map(target=>({target,isIntersecting:true,intersectionRatio:1})) as IntersectionObserverEntry[],{} as IntersectionObserver);await vi.advanceTimersByTimeAsync(401)});
 expect(h.request).toHaveBeenCalledWith('POST','/api/v1/conversations/c1/read',{messageIds:['m1','m3']});
 const next=observers.at(-1)!;await act(async()=>{next.callback(next.nodes.filter(node=>(node as HTMLElement).dataset.readId==='m2').map(target=>({target,isIntersecting:true,intersectionRatio:1})) as IntersectionObserverEntry[],{} as IntersectionObserver);await vi.advanceTimersByTimeAsync(200)});
 fireEvent.click(screen.getByRole('button',{name:'传输'}));await act(async()=>{await vi.advanceTimersByTimeAsync(500)});expect(h.request.mock.calls.filter(([method,path])=>method==='POST'&&path.endsWith('/read'))).toHaveLength(1);vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();
});
test('browser hides native attachment controls and rejects an oversized file before starting a download',async()=>{
 const h=setup({browser:true,messages:[message('m1',{fileId:'f',fileVersionId:'v',attachmentName:'大文件.zip',attachmentSize:1000000001,attachmentStatus:'available'})]});await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));expect(await screen.findByRole('button',{name:'超过 1 GB，请使用客户端'})).toBeDisabled();expect(screen.queryByRole('button',{name:'添加文件'})).not.toBeInTheDocument();expect(screen.queryByRole('button',{name:'粘贴文件 / 截图'})).not.toBeInTheDocument();expect(h.invoke.mock.calls.some(([name])=>name==='list_transfers')).toBe(false);
});
test('chat and transfer center share one subscription and the same updated task snapshot',async()=>{
 const h=setup();await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));await screen.findByLabelText('消息内容');expect(h.invoke.mock.calls.filter(([name])=>name==='list_transfers')).toHaveLength(1);
 await act(async()=>h.listeners.get('transfer.updated')?.({id:'upload',name:'同一任务.pdf',groupId:'c1',direction:'upload',versionId:'v',state:'transferring',bytesCompleted:50,totalBytes:100,activeSources:[],metrics:{revision:1,currentBytesPerSecond:20}}));
 expect(screen.getByRole('progressbar')).toHaveAttribute('value','50');fireEvent.click(screen.getByRole('button',{name:'传输'}));expect(screen.getByRole('progressbar')).toHaveAttribute('value','50');await act(async()=>h.listeners.get('transfer.updated')?.({id:'upload',name:'同一任务.pdf',groupId:'c1',direction:'upload',versionId:'v',state:'transferring',bytesCompleted:80,totalBytes:100,activeSources:[],metrics:{revision:2,currentBytesPerSecond:30}}));expect(screen.getByRole('progressbar')).toHaveAttribute('value','80');expect(h.invoke.mock.calls.filter(([name])=>name==='list_transfers')).toHaveLength(1);
});
test('text and attachment drafts remain attached to their conversation after navigating away',async()=>{
 const h=setup();await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));fireEvent.change(await screen.findByLabelText('消息内容'),{target:{value:'稍后发送的草稿'}});fireEvent.click(screen.getByRole('button',{name:'添加文件'}));await screen.findByText('草案.pdf');await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'联系人'}))});await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'消息'}))});expect(screen.getByLabelText('消息内容')).toHaveValue('稍后发送的草稿');expect(screen.getByText('草案.pdf')).toBeInTheDocument();expect(h.invoke.mock.calls.some(([name])=>name==='start_attachment_upload')).toBe(false);
});
test('native foreground state prevents receipts even if the webview document reports focus',async()=>{
 let observe:IntersectionObserverCallback=()=>{};const nodes:Element[]=[];vi.spyOn(document,'hasFocus').mockReturnValue(true);vi.stubGlobal('IntersectionObserver',class{constructor(callback:IntersectionObserverCallback){observe=callback}observe(node:Element){nodes.push(node)}disconnect(){}});
 const h=setup({messages:[message('m1')]});await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));await screen.findByText('消息 m1');await act(async()=>h.listeners.get('window.foreground')?.(false));vi.useFakeTimers();await act(async()=>{observe(nodes.map(target=>({target,isIntersecting:true,intersectionRatio:1})) as IntersectionObserverEntry[],{} as IntersectionObserver);await vi.advanceTimersByTimeAsync(1000)});expect(h.request.mock.calls.some(([method,path])=>method==='POST'&&path.endsWith('/read'))).toBe(false);vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();
});
test('reads proceed when native foreground is true even if the webview document reports no focus',async()=>{
 let observe:IntersectionObserverCallback=()=>{};const nodes:Element[]=[];vi.spyOn(document,'hasFocus').mockReturnValue(false);vi.stubGlobal('IntersectionObserver',class{constructor(callback:IntersectionObserverCallback){observe=callback}observe(node:Element){nodes.push(node)}disconnect(){}});
 const h=setup({messages:[message('m1')]});await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));await screen.findByText('消息 m1');await act(async()=>h.listeners.get('window.foreground')?.(true));vi.useFakeTimers();await act(async()=>{observe(nodes.map(target=>({target,isIntersecting:true,intersectionRatio:1})) as IntersectionObserverEntry[],{} as IntersectionObserver);await vi.advanceTimersByTimeAsync(1000)});expect(h.request.mock.calls.some(([method,path])=>method==='POST'&&path.endsWith('/read'))).toBe(true);vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();
});
test('pointerdown on the conversation re-evaluates the read gate after a missed visibility change',async()=>{
 let observe:IntersectionObserverCallback=()=>{};const nodes:Element[]=[];vi.spyOn(document,'hasFocus').mockReturnValue(true);vi.stubGlobal('IntersectionObserver',class{constructor(callback:IntersectionObserverCallback){observe=callback}observe(node:Element){nodes.push(node)}disconnect(){}});
 Object.defineProperty(document,'visibilityState',{get:()=>'hidden',configurable:true});
 const h=setup({messages:[message('m1')]});await login();fireEvent.click(screen.getByRole('button',{name:'周予'}));await screen.findByText('消息 m1');vi.useFakeTimers();
 await act(async()=>{observe(nodes.map(target=>({target,isIntersecting:true,intersectionRatio:1})) as IntersectionObserverEntry[],{} as IntersectionObserver);await vi.advanceTimersByTimeAsync(1000)}); // 门控因 hidden 关闭：无 read
 expect(h.request.mock.calls.some(([method,path])=>method==='POST'&&path.endsWith('/read'))).toBe(false);
 Object.defineProperty(document,'visibilityState',{get:()=>'visible',configurable:true}); // 模拟 missed visibilitychange
 fireEvent.pointerDown(screen.getByText('消息 m1'));
 await act(async()=>{await vi.advanceTimersByTimeAsync(1000)});
 expect(h.request.mock.calls.some(([method,path])=>method==='POST'&&path.endsWith('/read'))).toBe(true);
 delete (document as any).visibilityState;vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();
});
test('an open profile updates from waiting to direct messaging when the friend acceptance event arrives',async()=>{
 const h=setup();await login();fireEvent.click(screen.getByRole('button',{name:'联系人'}));fireEvent.change(await screen.findByLabelText('通过数字账号添加好友'),{target:{value:peer.accountNumber}});fireEvent.click(screen.getByRole('button',{name:'查找'}));fireEvent.click(await screen.findByRole('button',{name:'查看资料'}));fireEvent.click(await screen.findByRole('button',{name:'添加好友'}));await screen.findByRole('button',{name:'等待验证'});h.approve();await act(async()=>h.listeners.get('server.event')?.({type:'contacts.changed'}));expect(await within(screen.getByRole('dialog',{name:'个人资料'})).findByRole('button',{name:'发送消息'})).toBeEnabled();
});

test('a prepared desktop update is announced from chat and opens update settings without installing',async()=>{
 const h=setup();await login();
 await act(async()=>h.listeners.get('update.status')?.({state:'ready',version:'0.2.1',downloadedBytes:10,reusedBytes:90,totalBytes:100}));
 expect(await screen.findByText('新版本 0.2.1 已准备好')).toBeInTheDocument();
 fireEvent.click(screen.getByRole('button',{name:'查看更新'}));
 expect(await screen.findByRole('heading',{name:'客户端更新'})).toBeInTheDocument();
 expect(h.invoke.mock.calls.some(([name])=>name==='install_ready_update')).toBe(false);
});

test('browser never offers desktop update controls even if a stale update event arrives',async()=>{
 const h=setup({browser:true});await login();
 await act(async()=>h.listeners.get('update.status')?.({state:'ready',version:'0.2.1',downloadedBytes:10,reusedBytes:90,totalBytes:100}));
 expect(screen.queryByRole('button',{name:'查看更新'})).not.toBeInTheDocument();
 expect(h.invoke.mock.calls.some(([name])=>name==='get_update_status')).toBe(false);
});

test('unread badge clears after visible messages are acknowledged',async()=>{
 let observe:IntersectionObserverCallback=()=>{};const nodes:Element[]=[];vi.spyOn(document,'hasFocus').mockReturnValue(true);vi.stubGlobal('IntersectionObserver',class{constructor(callback:IntersectionObserverCallback){observe=callback}observe(node:Element){nodes.push(node)}disconnect(){}});
 const h=setup({messages:[message('m1')],unread:1});await login();
 expect(await screen.findByLabelText('1条未读')).toBeInTheDocument();
 fireEvent.click(screen.getByRole('button',{name:'周予'}));await screen.findByText('消息 m1');
 vi.useFakeTimers();await act(async()=>{observe(nodes.map(target=>({target,isIntersecting:true,intersectionRatio:1})) as IntersectionObserverEntry[],{} as IntersectionObserver);await vi.advanceTimersByTimeAsync(1000)});
 expect(h.request.mock.calls.some(([method,path])=>method==='POST'&&path.endsWith('/read'))).toBe(true);
 vi.useRealTimers();
 await act(async()=>h.listeners.get('server.event')?.({type:'conversation.changed',id:'c1'}));
 await waitFor(()=>expect(screen.queryByLabelText('1条未读')).not.toBeInTheDocument());
 vi.unstubAllGlobals();vi.restoreAllMocks();
});

import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { App } from '../src/main';
const me={id:'u1',username:'lin',displayName:'林晓',accountNumber:'100001',email:'',roles:['employee'],disabled:false,mustChangePassword:false};
const peer={...me,id:'u2',username:'zhou',displayName:'周予',accountNumber:'100002'};
const direct={id:'c1',kind:'direct',title:'周予',peer,memberCount:2,unreadCount:0,canSend:true};
const message=(id='m1',extra:Record<string,unknown>={})=>({id,sequence:1,conversationId:'c1',groupId:'c1',senderId:me.id,sender:me,clientMessageId:id,body:'已发消息',createdAt:'2026-11-10T06:00:00Z',cursor:id,readCount:1,readByMe:true,...extra});
async function setup(options:{messages?:any[];conversations?:any[];readers?:()=>Promise<any>;send?:()=>Promise<any>}={}) {
 const listeners=new Map<string,(value:any)=>void>();
 const request=vi.fn(async(method:string,path:string,body?:any):Promise<any>=>{
  if(path==='/api/v1/conversations')return options.conversations??[direct];
  if(path.endsWith('/readers'))return options.readers?.()??{count:1,readers:[{...peer,readAt:'2026-11-10T06:01:00Z'}]};
  if(path.includes('/messages?'))return {messages:options.messages??[],hasMore:false,nextCursor:''};
  if(method==='POST'&&path.endsWith('/messages'))return options.send?.()??message('sent',{body:body.body,clientMessageId:body.clientMessageId});
  return [];
 });
 render(<App transport={{request,login:async()=>({id:'session',user:me,deviceId:'d',expiresAt:''}),logout:async()=>{}}} desktop={{kind:'native',invoke:async <T,>(name:string)=>(name==='list_transfers'?[]:undefined) as T,listen:async(name,callback)=>{listeners.set(name,callback);return()=>listeners.delete(name)}}}/>);
 fireEvent.change(screen.getByLabelText('账号'),{target:{value:me.accountNumber}});fireEvent.change(screen.getByLabelText('密码'),{target:{value:'pw'}});fireEvent.click(screen.getByRole('button',{name:'登录'}));
 await screen.findByRole('button',{name:(options.conversations??[direct])[0].title});return {request,listeners};
}
test('plain Enter sends by default while Shift Enter and IME confirmation preserve the draft',async()=>{
 const h=await setup();fireEvent.click(screen.getByRole('button',{name:'周予'}));const composer=await screen.findByLabelText('消息内容');fireEvent.change(composer,{target:{value:'你好'}});
 for(const keys of [{shiftKey:true},{isComposing:true},{keyCode:229}])fireEvent.keyDown(composer,{key:'Enter',...keys});
 expect(h.request.mock.calls.filter(([m,p])=>m==='POST'&&p.endsWith('/messages'))).toHaveLength(0);expect(composer).toHaveValue('你好');fireEvent.keyDown(composer,{key:'Enter'});
 await waitFor(()=>expect(h.request).toHaveBeenCalledWith('POST','/api/v1/conversations/c1/messages',expect.objectContaining({body:'你好'})));expect(screen.getByText(/Shift \+ Enter 换行/)).toBeInTheDocument();
});
test('repeated submit while a send is pending creates only one message',async()=>{
 let finish!:(value:any)=>void;const h=await setup({send:()=>new Promise(resolve=>{finish=resolve})});fireEvent.click(screen.getByRole('button',{name:'周予'}));const composer=await screen.findByLabelText('消息内容');fireEvent.change(composer,{target:{value:'只发送一次'}});fireEvent.keyDown(composer,{key:'Enter'});fireEvent.keyDown(composer,{key:'Enter'});fireEvent.click(screen.getByRole('button',{name:'发送中…'}));expect(h.request.mock.calls.filter(([m,p])=>m==='POST'&&p.endsWith('/messages'))).toHaveLength(1);await act(async()=>finish(message('sent',{body:'只发送一次'})));
});
test('reader detail retries a real response and traps then restores keyboard focus',async()=>{
 let calls=0;await setup({messages:[message()],readers:async()=>{if(++calls===1)throw new Error('网络暂时不可用');return {count:1,readers:[{...peer,avatarUrl:'/api/v1/profiles/u2/avatar',readAt:'2026-11-10T06:01:00Z'}]}}});fireEvent.click(screen.getByRole('button',{name:'周予'}));const receipt=await screen.findByRole('button',{name:'已读'});receipt.focus();fireEvent.click(receipt);const dialog=await screen.findByRole('dialog',{name:'已读成员'});expect(await within(dialog).findByText('网络暂时不可用')).toBeInTheDocument();const retry=within(dialog).getByRole('button',{name:'重试'});retry.focus();fireEvent.keyDown(retry,{key:'Tab'});expect(within(dialog).getByRole('button',{name:'关闭'})).toHaveFocus();fireEvent.click(retry);expect(await within(dialog).findByText('周予')).toBeInTheDocument();expect(within(dialog).getByText(/100002/)).toBeInTheDocument();expect(within(dialog).getByText(/2026/)).toBeInTheDocument();fireEvent.keyDown(dialog,{key:'Escape'});expect(screen.queryByRole('dialog',{name:'已读成员'})).not.toBeInTheDocument();expect(receipt).toHaveFocus();
});
test('conversation type filters and sender prefixes distinguish group and private chat',async()=>{
 const group={id:'g1',kind:'group',title:'项目组',memberCount:3,unreadCount:0,canSend:true,lastMessage:message('g-last',{senderId:peer.id,sender:peer,body:'计划已更新'})};await setup({conversations:[group,direct]});expect(within(screen.getByRole('button',{name:'项目组'})).getByText('群聊')).toBeInTheDocument();expect(within(screen.getByRole('button',{name:'周予'})).getByText('私聊')).toBeInTheDocument();expect(screen.getByText('周予：计划已更新')).toBeInTheDocument();fireEvent.click(screen.getByRole('button',{name:'仅看群聊'}));expect(screen.queryByRole('button',{name:'周予'})).not.toBeInTheDocument();fireEvent.click(screen.getByRole('button',{name:'仅看私聊'}));expect(screen.queryByRole('button',{name:'项目组'})).not.toBeInTheDocument();
});
test('persisted system messages render as neutral rows without sender actions or read buttons',async()=>{
 await setup({messages:[message('system',{kind:'system',systemType:'group.member_added',body:'周予加入了群聊'})]});fireEvent.click(screen.getByRole('button',{name:'周予'}));const notice=await screen.findByText('周予加入了群聊');expect(notice.closest('li')).toHaveClass('im-system-message');expect(within(notice.closest('li')!).queryByRole('button')).not.toBeInTheDocument();
});

test('Ctrl/Cmd Enter preference requires modifier and ignores Shift and composition',async()=>{
 localStorage.setItem('linknestx.chat.sendShortcut','ctrlEnter');
 try {const h=await setup();fireEvent.click(screen.getByRole('button',{name:'周予'}));const composer=await screen.findByLabelText('消息内容');fireEvent.change(composer,{target:{value:'指定快捷键'}});
 for(const keys of [{},{ctrlKey:true,shiftKey:true},{ctrlKey:true,isComposing:true},{metaKey:true,keyCode:229}])fireEvent.keyDown(composer,{key:'Enter',...keys});
 expect(h.request.mock.calls.filter(([m,p])=>m==='POST'&&p.endsWith('/messages'))).toHaveLength(0);fireEvent.keyDown(composer,{key:'Enter',metaKey:true});await waitFor(()=>expect(h.request).toHaveBeenCalledWith('POST','/api/v1/conversations/c1/messages',expect.objectContaining({body:'指定快捷键'})));
 } finally {localStorage.removeItem('linknestx.chat.sendShortcut')}
});

test('retrying a failed send twice while pending keeps one retry request',async()=>{
 let attempt=0;let finish!:(value:any)=>void;const h=await setup({send:()=>++attempt===1?Promise.reject(new Error('发送失败')):new Promise(resolve=>{finish=resolve})});fireEvent.click(screen.getByRole('button',{name:'周予'}));const composer=await screen.findByLabelText('消息内容');fireEvent.change(composer,{target:{value:'重试一次'}});fireEvent.click(screen.getByRole('button',{name:'发送'}));
 const retry=await screen.findByRole('button',{name:'重试发送'});await act(async()=>{retry.click();retry.click()});expect(h.request.mock.calls.filter(([m,p])=>m==='POST'&&p.endsWith('/messages'))).toHaveLength(2);await act(async()=>finish(message('sent',{body:'重试一次'})));
});

test('open reader details clear stale people immediately after membership changes',async()=>{
 let allowed=true;const h=await setup({messages:[message()],readers:async()=>allowed?{count:1,readers:[{...peer,readAt:'2026-11-10T06:01:00Z'}]}:Promise.reject(new Error('group access denied'))});fireEvent.click(screen.getByRole('button',{name:'周予'}));fireEvent.click(await screen.findByRole('button',{name:'已读'}));const dialog=await screen.findByRole('dialog',{name:'已读成员'});await within(dialog).findByText('周予');allowed=false;await act(async()=>h.listeners.get('server.event')?.({type:'membership.changed',groupId:'c1'}));expect(await within(dialog).findByText('group access denied')).toBeInTheDocument();expect(within(dialog).queryByText('周予')).not.toBeInTheDocument();
});

test('active-conversation signal clears for reader dialogs, background, and navigation',async()=>{
 const focus=vi.spyOn(document,'hasFocus').mockReturnValue(true);const announced:Array<string|null>=[];const collect=(event:Event)=>announced.push((event as CustomEvent).detail.id);window.addEventListener('linknestx:active-conversation',collect);
 try {const h=await setup({messages:[message()]});fireEvent.click(screen.getByRole('button',{name:'周予'}));await screen.findByRole('button',{name:'已读'});expect(announced.at(-1)).toBe('c1');fireEvent.click(screen.getByRole('button',{name:'已读'}));await screen.findByRole('dialog',{name:'已读成员'});expect(announced.at(-1)).toBe(null);fireEvent.click(screen.getByRole('button',{name:'关闭'}));expect(announced.at(-1)).toBe('c1');await act(async()=>h.listeners.get('window.foreground')?.(false));expect(announced.at(-1)).toBe(null);await act(async()=>h.listeners.get('window.foreground')?.(true));expect(announced.at(-1)).toBe('c1');fireEvent.click(screen.getByRole('button',{name:'传输'}));expect(announced.at(-1)).toBe(null);
 }finally{window.removeEventListener('linknestx:active-conversation',collect);focus.mockRestore()}
});

import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { harness, msg } from './workspace-fixtures';

test('system notice center deduplicates new messages and excludes the sender',async()=>{
 const h=harness();await h.login();
 await h.emit('server.event',{type:'message.created',message:{...msg('n1',1,'private body','g2'),senderId:'u2'},conversationId:'g2'});
 await h.emit('server.event',{type:'message.created',message:{...msg('n1',1,'private body','g2'),senderId:'u2'},conversationId:'g2'});
 await h.emit('server.event',{type:'message.created',message:{...msg('mine',2,'own','g2'),senderId:'u1'},conversationId:'g2'});
 fireEvent.click(screen.getByRole('button',{name:/系统通知/}));
 expect(screen.getAllByText('收到新消息')).toHaveLength(1);
 expect(screen.queryByText('private body')).not.toBeInTheDocument();
 fireEvent.click(screen.getByRole('button',{name:'全部标为已看'}));
 fireEvent.click(screen.getByRole('button',{name:'关闭系统通知'}));
 expect(screen.getByRole('button',{name:'系统通知'})).toBeInTheDocument();
});
test('connection loss and restoration produce useful notices without claiming local resync is connected',async()=>{
 const h=harness();await h.login();
 await h.emit('connection.changed',{status:'connected'});
 await h.emit('connection.changed',{status:'disconnected'});
 await screen.findByText('连接已断开，正在自动重试');
 expect(screen.getByRole('button',{name:'立即重连'})).toBeInTheDocument();
 await h.emit('server.event',{type:'session.ready',localResync:true});
 expect(screen.getByText('连接已断开，正在自动重试')).toBeInTheDocument();
 await h.emit('connection.changed',{status:'connected'});
 await screen.findByText('连接已恢复');
});
test('explicit operation notices are rendered accessibly and cleared after logout',async()=>{
 const h=harness();await h.login();
 await act(async()=>window.dispatchEvent(new CustomEvent('linknestx:notice',{detail:{kind:'success',title:'设置已保存'}})));
 expect(screen.getByText('设置已保存')).toBeInTheDocument();
 fireEvent.click(screen.getByRole('button',{name:'小林 的账户选项'}));
 fireEvent.click(screen.getByRole('button',{name:'退出登录'}));
 await waitFor(()=>expect(screen.getByRole('heading',{name:'登录工作区'})).toBeInTheDocument());
 expect(screen.queryByText('设置已保存')).not.toBeInTheDocument();
});
test('friend acceptance is detected when pending requests disappear into the friend list',async()=>{
 let accepted=false;const friend={id:'peer',username:'同事',displayName:'同事',roles:[],email:'',disabled:false,mustChangePassword:false};
 const h=harness({request:async(_m,p)=>p==='/api/v1/contacts'?{incoming:[],friends:accepted?[friend]:[],outgoing:accepted?[]:[{id:'req',status:'pending',to:friend,from:{id:'u1'}}]}:undefined});
 await h.login();accepted=true;await h.emit('server.event',{type:'contacts.changed'});
 await screen.findByText('好友申请已通过');
});

test('initial and reconnect hydration recover missed messages and deduplicate live delivery',async()=>{
 let latest=msg('before-subscription',1,'initial private body','g2');
 const h=harness({request:async(_method,path)=>path==='/api/v1/conversations'?[
  {id:'g1',kind:'group',title:'研发协作',canSend:true,memberCount:2,unreadCount:0},
  {id:'g2',kind:'group',title:'设计讨论',canSend:true,memberCount:2,unreadCount:2,lastMessage:latest},
 ]:undefined});
 await h.login();await screen.findByText('有未读消息');
 await h.emit('server.event',{type:'message.created',conversationId:'g2',message:latest});
 await h.emit('server.event',{type:'session.ready'});
 fireEvent.click(screen.getByRole('button',{name:/系统通知/}));
 expect(screen.getAllByText('有未读消息')).toHaveLength(1);
 expect(screen.getByText('设计讨论 有 2 条未读消息。')).toBeInTheDocument();
 expect(screen.queryByText('收到新消息')).not.toBeInTheDocument();
 fireEvent.click(screen.getByRole('button',{name:'关闭系统通知'}));
 latest=msg('while-disconnected',2,'reconnect private body','g2');
 await h.emit('server.event',{type:'session.ready'});
 await waitFor(()=>expect(screen.getByRole('button',{name:'系统通知，2 条未看'})).toBeInTheDocument());
 fireEvent.click(screen.getByRole('button',{name:/系统通知/}));
 expect(screen.getAllByText('有未读消息')).toHaveLength(2);
 expect(screen.getByRole('dialog')).not.toHaveTextContent('private body');
});

test('unread hydration finishing after logout cannot notify the next session',async()=>{
 let delay=false;const pending:Array<(value:any)=>void>=[];
 const h=harness({request:async(_method,path)=>{if(path==='/api/v1/conversations'&&delay)return new Promise(resolve=>pending.push(resolve));return undefined}});
 await h.login();delay=true;await h.emit('server.event',{type:'session.ready'});
 await waitFor(()=>expect(pending.length).toBeGreaterThan(0));
 fireEvent.click(screen.getByRole('button',{name:'小林 的账户选项'}));
 fireEvent.click(screen.getByRole('button',{name:'退出登录'}));
 await screen.findByRole('heading',{name:'登录工作区'});
 delay=false;await h.login();
 await act(async()=>pending.forEach(resolve=>resolve([{id:'g2',kind:'group',title:'上一会话',canSend:true,memberCount:2,unreadCount:1,lastMessage:msg('old-session',1,'private','g2')}])));
 fireEvent.click(screen.getByRole('button',{name:/系统通知/}));
 expect(screen.getByText('暂时没有新通知')).toBeInTheDocument();
 expect(screen.queryByText('有未读消息')).not.toBeInTheDocument();
});

test('notification panel cancels read dwell until the chat becomes unobscured',async()=>{
 const observers:Array<{callback:IntersectionObserverCallback;nodes:Element[]}>=[];
 const focus=vi.spyOn(document,'hasFocus').mockReturnValue(true);
 vi.stubGlobal('IntersectionObserver',class {nodes:Element[]=[];constructor(callback:IntersectionObserverCallback){observers.push({callback,nodes:this.nodes})}observe(node:Element){this.nodes.push(node)}disconnect(){}});
 const active:Array<string|null>=[];const selected=(event:Event)=>active.push((event as CustomEvent).detail.id);
 window.addEventListener('linknestx:active-conversation',selected);
 try{
  const h=harness({request:async(method,path,body)=>{if(path.includes('/messages?'))return {messages:[msg('dwell',1,'待阅读的消息')],hasMore:false,nextCursor:''};if(method==='POST'&&path.endsWith('/read'))return {messageIds:body.messageIds};return undefined}});
  await h.login();await screen.findByText('待阅读的消息');expect(active.at(-1)).toBe('g1');
  const makeVisible=()=>{const current=observers.at(-1)!;current.callback(current.nodes.map(target=>({target,isIntersecting:true,intersectionRatio:1})) as IntersectionObserverEntry[],{} as IntersectionObserver)};
  vi.useFakeTimers();await act(async()=>{makeVisible();await vi.advanceTimersByTimeAsync(200)});
  fireEvent.click(screen.getByRole('button',{name:/系统通知/}));
  expect(screen.getByRole('dialog',{name:'系统通知'})).toBeInTheDocument();expect(active.at(-1)).toBe(null);
  await act(async()=>{makeVisible();await vi.advanceTimersByTimeAsync(1000)});
  expect(h.request.mock.calls.filter(([method,path])=>method==='POST'&&path.endsWith('/read'))).toHaveLength(0);
  fireEvent.click(screen.getByRole('button',{name:'关闭系统通知'}));expect(active.at(-1)).toBe('g1');
  await act(async()=>{makeVisible();await vi.advanceTimersByTimeAsync(401)});
  expect(h.request).toHaveBeenCalledWith('POST','/api/v1/conversations/g1/read',{messageIds:['dwell']});
 }finally{vi.useRealTimers();window.removeEventListener('linknestx:active-conversation',selected);focus.mockRestore();vi.unstubAllGlobals()}
});

test('unread hydration uses current native foreground state after startup',async()=>{
 const focus=vi.spyOn(document,'hasFocus').mockReturnValue(true);vi.stubGlobal('__TAURI__',{core:{invoke:vi.fn()}});
 let unread=0;
 try{
  const h=harness({request:async(_method,path)=>path==='/api/v1/conversations'?[{id:'g1',kind:'group',title:'研发协作',canSend:true,memberCount:2,unreadCount:unread,lastMessage:msg('current-chat',1,'current body')}]:undefined});
  await h.login();await h.emit('window.foreground',true);unread=1;await h.emit('server.event',{type:'session.ready'});
  fireEvent.click(screen.getByRole('button',{name:/系统通知/}));
  expect(screen.getByText('暂时没有新通知')).toBeInTheDocument();
  expect(screen.queryByText('有未读消息')).not.toBeInTheDocument();
 }finally{focus.mockRestore();vi.unstubAllGlobals()}
});

test('incoming unread history is recovered even when the latest message was sent by me',async()=>{
 const h=harness({request:async(_method,path)=>path==='/api/v1/conversations'?[
  {id:'g1',kind:'group',title:'研发协作',canSend:true,memberCount:2,unreadCount:0},
  {id:'g2',kind:'group',title:'设计讨论',canSend:true,memberCount:2,unreadCount:3,lastMessage:{...msg('self-reply',4,'my reply','g2'),senderId:'u1'}},
 ]:undefined});
 await h.login();
 expect(await screen.findByText('有未读消息')).toBeInTheDocument();
 expect(screen.getByText('设计讨论 有 3 条未读消息。')).toBeInTheDocument();
});

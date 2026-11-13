import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { App } from '../src/main';
import { DesktopProvider } from '../src/runtime/DesktopProvider';
import { UpdatePanel } from '../src/features/UpdatePanel';
const me={id:'my-private-uuid',username:'lin',displayName:'林晓',accountNumber:'10000001',email:'',roles:['employee'],disabled:false,mustChangePassword:false};
const peer={...me,id:'colleague-private-uuid',username:'zhou',displayName:'周予',accountNumber:'10000002'};
async function setup(loadProfile:()=>Promise<any>=async()=>peer,friend=false,disabledInGroup=false){
 let members=[{userId:peer.id,role:'member',joinedAt:'2026-11-10',displayName:'周予',nickname:null,accountNumber:'10000002',hasAvatar:false,disabled:disabledInGroup}];let outgoing:any[]=[];const listeners=new Map<string,(v:any)=>void>();
 const request=vi.fn(async(method:string,path:string,body?:any):Promise<any>=>{if(path==='/api/v1/conversations')return [];if(path==='/api/v1/groups')return [{id:'g1',name:'协作群',isMember:true}];if(path.endsWith('/members'))return members;if(path.startsWith('/api/v1/profiles/'))return loadProfile();if(path.includes('/messages?'))return {messages:[],hasMore:false,nextCursor:''};if(path==='/api/v1/contacts')return {friends:friend?[peer]:[],incoming:[],outgoing};if(path==='/api/v1/conversations/direct')return {id:'direct-with-peer'};if(path==='/api/v1/friend-requests'){const item={id:'r1',from:me,to:peer,status:'pending'};outgoing=[item];return item}throw new Error(`blocked ${method} ${path}`)});
 render(<App transport={{request,login:async()=>({id:'member-session',user:me,deviceId:'web',expiresAt:''}),logout:async()=>{}}} desktop={{kind:'browser',invoke:async<T,>()=>undefined as T,listen:async(name,callback)=>{listeners.set(name,callback);return()=>listeners.delete(name)}}}/>);
 fireEvent.change(screen.getByLabelText('账号'),{target:{value:me.accountNumber}});fireEvent.change(screen.getByLabelText('密码'),{target:{value:'password'}});fireEvent.click(screen.getByRole('button',{name:'登录'}));fireEvent.click(await screen.findByRole('button',{name:'群组与成员管理'}));await screen.findByRole('heading',{name:'协作群'});
 return {request,listeners,remove:()=>{members=[]}};
}
test('Web member cards resolve named profiles and numeric accounts without loading the broad user directory',async()=>{
 const h=await setup();expect(await screen.findByText('周予')).toBeInTheDocument();expect(screen.getByText('账号 10000002')).toBeInTheDocument();expect(screen.queryByText(peer.id)).not.toBeInTheDocument();expect(h.request.mock.calls.some(([,path])=>path==='/api/v1/users')).toBe(false);expect(h.request.mock.calls.some(([method,path])=>method==='GET'&&path===`/api/v1/profiles/${peer.id}`)).toBe(false);
 fireEvent.click(screen.getByRole('button',{name:'查看周予的资料'}));const dialog=await screen.findByRole('dialog',{name:'个人资料'});await waitFor(()=>expect(h.request).toHaveBeenCalledWith('GET',`/api/v1/profiles/${peer.id}`,undefined));fireEvent.click(await within(dialog).findByRole('button',{name:'添加好友'}));await within(dialog).findByRole('button',{name:'等待验证'});expect(h.request).toHaveBeenCalledWith('POST','/api/v1/friend-requests',{accountNumber:'10000002'});
});
test('disabled member rows show the server name without account details or a profile dialog',async()=>{
 const h=await setup(async()=>peer,false,true);expect(await screen.findByText('周予（已停用）')).toBeInTheDocument();expect(h.request.mock.calls.some(([method,path])=>method==='GET'&&path===`/api/v1/profiles/${peer.id}`)).toBe(false);expect(screen.queryByText(/10000002/)).not.toBeInTheDocument();expect(screen.queryByText(peer.id)).not.toBeInTheDocument();expect(screen.queryByRole('button',{name:'查看周予的资料'})).not.toBeInTheDocument();
});
test('rolled-back update explicitly offers Retry update using check_for_updates',async()=>{
 const invoke=vi.fn(async(command:string):Promise<any>=>command==='get_update_status'?{state:'rolledBack',version:'0.2.1',error:'已恢复可用版本',downloadedBytes:0,reusedBytes:0,totalBytes:0}:command==='check_for_updates'?{state:'checking',downloadedBytes:0,reusedBytes:0,totalBytes:0}:undefined);
 render(<DesktopProvider adapter={{invoke,listen:async()=>()=>{}}}><UpdatePanel/></DesktopProvider>);expect(await screen.findByText('已恢复可用版本')).toBeInTheDocument();fireEvent.click(screen.getByRole('button',{name:'重试更新'}));await waitFor(()=>expect(invoke).toHaveBeenCalledWith('check_for_updates'));expect(await screen.findByText('正在检查更新')).toBeInTheDocument();expect(invoke.mock.calls.some(([command])=>command==='install_ready_update')).toBe(false);
});
test('a member who is already a friend can open their direct conversation from the profile',async()=>{
 const h=await setup(async()=>peer,true);fireEvent.click(await screen.findByRole('button',{name:'查看周予的资料'}));const dialog=await screen.findByRole('dialog',{name:'个人资料'});const opened=vi.fn();window.addEventListener('linknestx:open-conversation',opened);try{fireEvent.click(await within(dialog).findByRole('button',{name:'发送消息'}));await waitFor(()=>expect(h.request).toHaveBeenCalledWith('POST','/api/v1/conversations/direct',{userId:peer.id}));await waitFor(()=>expect(opened).toHaveBeenCalledWith(expect.objectContaining({detail:{id:'direct-with-peer'}})));expect(screen.queryByRole('dialog',{name:'个人资料'})).not.toBeInTheDocument()}finally{window.removeEventListener('linknestx:open-conversation',opened)}
});

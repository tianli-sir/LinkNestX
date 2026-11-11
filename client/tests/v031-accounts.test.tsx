import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { AuthProvider, useAuth, type User } from '../src/auth';
import { type NativeTransport } from '../src/api/rest';
import { DesktopProvider, type DesktopAdapter } from '../src/runtime/DesktopProvider';
import { SettingsView } from '../src/features/SettingsView';
import { AdminView } from '../src/features/AdminView';
import { AppShell } from '../src/components/AppShell';

const employee: User = { id: 'employee-id', username: 'lin', displayName: '林晓', accountNumber: '627491', email: '', roles: ['user'], disabled: false, mustChangePassword: false };
afterEach(() => {cleanup();localStorage.clear();vi.restoreAllMocks()});

function setup(page: 'settings'|'admin'|'popover', role='user', respond?: (method:string,path:string,body?:any)=>Promise<unknown>) {
 let current={...employee,roles:[role]};
 const request=vi.fn(async(method:string,path:string,body?:any)=>{
  const custom=await respond?.(method,path,body);if(custom!==undefined)return custom;
  if(path==='/api/v1/admin/users')return [current];
  if(path==='/api/v1/me/devices')return [];
  if(path==='/api/v1/auth/password')return current;
  if(method==='PATCH'&&path.endsWith('/account-number')) { current={...current,accountNumber:body.accountNumber};return current }
  if(method==='POST'&&path==='/api/v1/users')return {...employee,...body,id:'new-id'};
  return null;
 });
 const session={id:'session-id',expiresAt:'',deviceId:'device-id',user:current};
 const transport:NativeTransport={restore:async()=>session,login:async()=>session,logout:vi.fn(async()=>{}),request:request as NativeTransport['request']};
 const adapter:DesktopAdapter={kind:'browser',listen:async()=>()=>{},invoke:vi.fn(async()=>null) as DesktopAdapter['invoke']};
 function Content(){const auth=useAuth();if(!auth.session)return null;return <><output aria-label="当前会话">{auth.session.id}</output>{page==='settings'?<SettingsView onClose={()=>{}}/>:page==='admin'?<AdminView client={auth.client}/>:<AppShell selected="groups" connection={{status:'connected'}} user={auth.session.user} onNavigate={()=>{}} onSettings={()=>{}}>内容</AppShell>}</>}
 render(<AuthProvider transport={transport}><DesktopProvider adapter={adapter}><Content/></DesktopProvider></AuthProvider>);
 return {request,transport};
}

it.each(['user','admin'])('lets %s change their password from settings without logging out',async(role)=>{
 const h=setup('settings',role);await screen.findByRole('heading',{name:'设置'});
 fireEvent.change(screen.getByLabelText('当前密码'),{target:{value:'current-password'}});
 fireEvent.change(screen.getByLabelText('新密码'),{target:{value:'next-password'}});
 fireEvent.change(screen.getByLabelText('确认新密码'),{target:{value:'next-password'}});
 fireEvent.click(screen.getByRole('button',{name:'保存新密码'}));
 expect(await screen.findByText('密码已更新，当前设备保持登录。')).toBeVisible();
 expect(h.request).toHaveBeenCalledWith('POST','/api/v1/auth/password',{currentPassword:'current-password',newPassword:'next-password'});
 expect(screen.getByLabelText('当前会话')).toHaveTextContent('session-id');
 expect(screen.getByLabelText('当前密码')).toHaveValue('');expect(screen.getByLabelText('新密码')).toHaveValue('');
 expect(h.transport.logout).not.toHaveBeenCalled();
});

it('keeps password mismatch local and shows a safe current-password error from the server',async()=>{
 const h=setup('settings','user',async(_method,path)=>{if(path==='/api/v1/auth/password')throw new Error('invalid credentials')});
 await screen.findByRole('heading',{name:'设置'});
 fireEvent.change(screen.getByLabelText('当前密码'),{target:{value:'wrong-password'}});
 fireEvent.change(screen.getByLabelText('新密码'),{target:{value:'next-password'}});
 fireEvent.change(screen.getByLabelText('确认新密码'),{target:{value:'different-password'}});
 fireEvent.click(screen.getByRole('button',{name:'保存新密码'}));
 expect(await screen.findByText('两次输入的新密码不一致。')).toBeVisible();
 expect(h.request.mock.calls.some(([,path])=>path==='/api/v1/auth/password')).toBe(false);
 fireEvent.change(screen.getByLabelText('确认新密码'),{target:{value:'next-password'}});
 fireEvent.click(screen.getByRole('button',{name:'保存新密码'}));
 expect(await screen.findByText('当前密码不正确，请重新输入。')).toBeVisible();
 expect(screen.queryByText('wrong-password')).not.toBeInTheDocument();
 expect(screen.getByLabelText('当前会话')).toHaveTextContent('session-id');
});

it.each(['settings','popover'] as const)('shows and copies own number in %s',async(page)=>{
 const writeText=vi.fn(async()=>{});Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText}});
 setup(page);await screen.findByLabelText('当前会话');
 if(page==='popover')fireEvent.click(screen.getByRole('button',{name:'lin 的账户选项'}));
 expect(screen.getByText(employee.accountNumber!)).toBeVisible();
 fireEvent.click(screen.getByRole('button',{name:'复制我的账号'}));
 await waitFor(()=>expect(writeText).toHaveBeenCalledWith(employee.accountNumber));
 expect(await screen.findByText('账号已复制')).toBeVisible();
});

it('copies the visible number on a LAN browser without the clipboard API',async()=>{
 Object.defineProperty(navigator,'clipboard',{configurable:true,value:undefined});
 const command=vi.fn(()=>true);Object.defineProperty(document,'execCommand',{configurable:true,value:command});
 setup('settings');await screen.findByRole('heading',{name:'设置'});
 fireEvent.click(screen.getByRole('button',{name:'复制我的账号'}));
 await waitFor(()=>expect(command).toHaveBeenCalledWith('copy'));
 expect(await screen.findByText('账号已复制')).toBeVisible();
 Reflect.deleteProperty(document,'execCommand');
});

it('disables duplicate password submissions until the current update settles',async()=>{
 let finish!:(value:User)=>void;
 const h=setup('settings','user',async(_method,path)=>path==='/api/v1/auth/password'?new Promise<User>(resolve=>{finish=resolve}):undefined);
 await screen.findByRole('heading',{name:'设置'});
 fireEvent.change(screen.getByLabelText('当前密码'),{target:{value:'current-password'}});
 fireEvent.change(screen.getByLabelText('新密码'),{target:{value:'next-password'}});
 fireEvent.change(screen.getByLabelText('确认新密码'),{target:{value:'next-password'}});
 fireEvent.click(screen.getByRole('button',{name:'保存新密码'}));
 expect(screen.getByRole('button',{name:'正在更新…'})).toBeDisabled();
 fireEvent.submit(screen.getByRole('form',{name:'修改密码'}));
 expect(h.request.mock.calls.filter(([,path])=>path==='/api/v1/auth/password')).toHaveLength(1);
 await act(async()=>finish(employee));expect(await screen.findByText('密码已更新，当前设备保持登录。')).toBeVisible();
});

it('sets the shared send shortcut from settings',async()=>{
 setup('settings');await screen.findByRole('heading',{name:'设置'});
 fireEvent.change(screen.getByLabelText('发送消息快捷键'),{target:{value:'ctrlEnter'}});
 expect(screen.getByLabelText('发送消息快捷键')).toHaveValue('ctrlEnter');
});

it('creates an account with an optional chosen number and edits it through the admin endpoint',async()=>{
 const h=setup('admin','admin');await screen.findByText('627491');
 fireEvent.change(screen.getByLabelText('新用户名'),{target:{value:'new-member'}});
 fireEvent.change(screen.getByLabelText('数字账号（可选）'),{target:{value:'876543'}});
 fireEvent.change(screen.getByLabelText('初始密码'),{target:{value:'initial-password'}});
 fireEvent.click(screen.getByRole('button',{name:'创建账号'}));
 await screen.findByText('账号已创建。请通过可信方式告知同事初始密码。');
 expect(h.request).toHaveBeenCalledWith('POST','/api/v1/users',expect.objectContaining({accountNumber:'876543'}));
 fireEvent.click(screen.getByRole('button',{name:'修改数字账号'}));
 fireEvent.change(screen.getByLabelText('新的数字账号'),{target:{value:'7654321'}});
 fireEvent.click(screen.getByRole('button',{name:'保存数字账号'}));
 expect(await screen.findByText('7654321')).toBeVisible();
 expect(h.request).toHaveBeenCalledWith('PATCH','/api/v1/admin/users/employee-id/account-number',{accountNumber:'7654321'});
});

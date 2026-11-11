import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { DesktopProvider, useDesktop, type DesktopAdapter } from '../src/runtime/DesktopProvider';
import { createBrowserAdapter } from '../src/runtime/browserAdapter';
import { useChatPreferences } from '../src/runtime/chatPreferences';

function Connection(){const {connection}=useDesktop();return <output aria-label="连接">{connection.status}</output>}
test('restores a native socket already connected before the workspace subscribed',async()=>{
 const adapter:DesktopAdapter={kind:'native',listen:vi.fn(async()=>()=>{}),invoke:vi.fn(async(command)=>command==='get_connection_status'?{status:'connected'}:{}) as any};
 render(<DesktopProvider adapter={adapter}><Connection/></DesktopProvider>);
 await waitFor(()=>expect(screen.getByLabelText('连接')).toHaveTextContent('connected'));
 expect(adapter.invoke).toHaveBeenCalledWith('get_connection_status');
});
test('new live connection events win over an older in-flight connection snapshot',async()=>{
 const handlers=new Map<string,(p:any)=>void>();let resolve!:(p:any)=>void;
 const adapter:DesktopAdapter={kind:'native',listen:async(name,handler)=>{handlers.set(name,handler);return()=>{}},invoke:vi.fn(async(command)=>command==='get_connection_status'?new Promise(r=>{resolve=r}):{}) as any};
 render(<DesktopProvider adapter={adapter}><Connection/></DesktopProvider>);
 await waitFor(()=>expect(resolve).toBeTypeOf('function'));
 await act(async()=>{handlers.get('connection.changed')!({status:'disconnected'});resolve({status:'connected'})});
 expect(screen.getByLabelText('连接')).toHaveTextContent('disconnected');
});
test('browser snapshot and late subscriber receive real current socket state',async()=>{
 let socket:any;vi.stubGlobal('WebSocket',class {onopen:any;onclose:any;onmessage:any;onerror:any;constructor(){socket=this}close(){}});
 const adapter=createBrowserAdapter();const remove=await adapter.listen('server.event',()=>{});
 socket.onopen();const observed:any[]=[];const unlisten=await adapter.listen('connection.changed',p=>observed.push(p));
 expect(observed.at(-1)).toEqual({status:'connected'});
 expect(await adapter.invoke('get_connection_status')).toEqual({status:'connected'});
 remove();unlisten();vi.unstubAllGlobals();
});
test('chat send preference is persisted and updates every mounted consumer',()=>{
 localStorage.clear();function Pref({name}:{name:string}){const {sendShortcut,setSendShortcut}=useChatPreferences();return <button onClick={()=>setSendShortcut('ctrlEnter')}>{name}:{sendShortcut}</button>}
 render(<><Pref name="settings"/><Pref name="composer"/></>);
 fireEvent.click(screen.getByText('settings:enter'));
 expect(screen.getByText('composer:ctrlEnter')).toBeInTheDocument();
 expect(localStorage.getItem('linknestx.chat.sendShortcut')).toBe('ctrlEnter');
});
test('chat send preference updates in memory when storage writes fail but reads succeed',()=>{
 localStorage.setItem('linknestx.chat.sendShortcut','enter');
 const write=vi.spyOn(Storage.prototype,'setItem').mockImplementation(()=>{throw new DOMException('Quota exceeded','QuotaExceededError')});
 function Pref(){const {sendShortcut,setSendShortcut}=useChatPreferences();return <button onClick={()=>setSendShortcut('ctrlEnter')}>quota:{sendShortcut}</button>}
 try {
  render(<Pref/>);
  fireEvent.click(screen.getByText('quota:enter'));
  expect(screen.getByText('quota:ctrlEnter')).toBeInTheDocument();
  expect(localStorage.getItem('linknestx.chat.sendShortcut')).toBe('enter');
 } finally {write.mockRestore();}
});

import { act, fireEvent, render, screen } from '@testing-library/react';
import { LoginScreen } from '../src/auth/LoginScreen';
import { createBrowserTransport } from '../src/api/browser';
import { VisibleReadTracker } from '../src/features/messaging/readTracker';
import { TransferStore } from '../src/runtime/TransferStore';
test('login only submits account credentials and never exposes server selection', async () => {
 const login=vi.fn(async()=>{}); render(<LoginScreen onLogin={login} initialServerUrl="https://untrusted.invalid"/>);
 expect(screen.queryByLabelText('服务器地址')).not.toBeInTheDocument();
 fireEvent.change(screen.getByLabelText('账号'),{target:{value:'10000001'}});fireEvent.change(screen.getByLabelText('密码'),{target:{value:'pw'}});
 await act(async()=>fireEvent.click(screen.getByRole('button',{name:'登录'})));expect(login).toHaveBeenCalledWith({username:'10000001',password:'pw'});
});
test('browser session uses cookies and CSRF without bearer persistence',async()=>{
 const calls:Array<{path:string;init:RequestInit}>=[];
 const fetcher=async(path:any,init:any)=>{calls.push({path,init});return new Response(JSON.stringify(path.endsWith('/login')?{user:{id:'u'},csrfToken:'csrf',expiresAt:'2099-01-01',capabilities:{}}:{ok:true}),{status:200,headers:{'Content-Type':'application/json'}})};
 const transport=createBrowserTransport(fetcher as typeof fetch);await transport.login({username:'10000001',password:'secret'});
 await transport.request('POST','/api/v1/conversations/c/messages',{body:'hello'});
 expect(calls[0].init.credentials).toBe('same-origin');expect(new Headers(calls[1].init.headers).get('X-CSRF-Token')).toBe('csrf');
 expect(new Headers(calls[1].init.headers).has('Authorization')).toBe(false);expect(localStorage.getItem('accessToken')).toBeNull();
});
test('read tracker preserves holes and requires uninterrupted 400ms visibility with focus',async()=>{
 vi.useFakeTimers();const send=vi.fn(async(ids:string[])=>ids);const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);tracker.visible('m1',true);tracker.visible('m3',true);
 await vi.advanceTimersByTimeAsync(399);expect(send).not.toHaveBeenCalled();tracker.visible('m1',false);await vi.advanceTimersByTimeAsync(2);expect(send).toHaveBeenCalledWith(['m3']);
 tracker.visible('m2',true);await vi.advanceTimersByTimeAsync(200);tracker.setEnabled(false);await vi.advanceTimersByTimeAsync(1000);expect(send).toHaveBeenCalledTimes(1);
 tracker.setEnabled(true);await vi.advanceTimersByTimeAsync(399);expect(send).toHaveBeenCalledTimes(1);await vi.advanceTimersByTimeAsync(2);expect(send).toHaveBeenLastCalledWith(['m2']);tracker.dispose();vi.useRealTimers();
});
test('read tracker limits each batch to 100 actual IDs',async()=>{
 vi.useFakeTimers();const send=vi.fn(async(ids:string[])=>ids);const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);for(let i=0;i<205;i++)tracker.visible(`m${i}`,true);
 await vi.advanceTimersByTimeAsync(401);expect(send.mock.calls.every(([ids])=>ids.length<=100)).toBe(true);expect(send.mock.calls.flatMap(([ids])=>ids)).toHaveLength(205);tracker.dispose();vi.useRealTimers();
});
test('transfer store keeps newest revision when a stale refresh or event arrives',()=>{
 const store=new TransferStore();const task={id:'t',direction:'download' as const,versionId:'v',name:'file',state:'transferring' as const,bytesCompleted:40,totalBytes:100,activeSources:[],metrics:{revision:2}};
 store.upsert(task);store.replace([{...task,bytesCompleted:10,metrics:{revision:1}}]);store.upsert({...task,bytesCompleted:5,metrics:{revision:0}});
 expect(store.getSnapshot().tasks[0].bytesCompleted).toBe(40);store.clear();expect(store.getSnapshot().tasks).toEqual([]);
});
test('read acknowledgements retry while still visible and stop retrying after losing focus',async()=>{
 vi.useFakeTimers();const send=vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValue(['m1']);const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);tracker.visible('m1',true);await vi.advanceTimersByTimeAsync(401);expect(send).toHaveBeenCalledTimes(1);await vi.advanceTimersByTimeAsync(1800);expect(send).toHaveBeenCalledTimes(2);tracker.dispose();
 const offline=vi.fn().mockRejectedValue(new Error('offline'));const second=new VisibleReadTracker(offline);second.setEnabled(true);second.visible('m2',true);await vi.advanceTimersByTimeAsync(401);second.setEnabled(false);await vi.advanceTimersByTimeAsync(10000);expect(offline).toHaveBeenCalledTimes(1);second.dispose();vi.useRealTimers();
});
test('read tracker keeps a slow 30s retry after fast attempts',async()=>{
 vi.useFakeTimers();const send=vi.fn().mockRejectedValue(new Error('offline'));
 const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);tracker.visible('m1',true);
 await vi.advanceTimersByTimeAsync(401+1000+2000+4000+4000); // 3 次快速退避（1s/2s/4s，每次重试含 400ms 停留）
 expect(send).toHaveBeenCalledTimes(4);
 await vi.advanceTimersByTimeAsync(26000);expect(send).toHaveBeenCalledTimes(4); // 30 秒内不再轰炸
 await vi.advanceTimersByTimeAsync(5000);expect(send).toHaveBeenCalledTimes(5); // 慢重试在 ~30s 后落地
 tracker.dispose();vi.useRealTimers();
});
test('read tracker sheds poison ids on repeated 400 without losing the rest',async()=>{
 vi.useFakeTimers();
 const send=vi.fn(async(ids:string[])=>{if(ids.includes('bad'))throw{status:400};return ids});
 const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);
 tracker.visible('bad',true);tracker.visible('ok1',true);tracker.visible('ok2',true);
 await vi.advanceTimersByTimeAsync(401);
 const flat=()=>send.mock.calls.flatMap(([ids])=>ids as string[]);
 expect(flat()).toContain('ok1');expect(flat()).toContain('ok2');
 expect(flat().filter(id=>id==='bad').length).toBeGreaterThan(0); // 至少尝试过整批
 await vi.advanceTimersByTimeAsync(60000);
 const callsWithOnlyOk=send.mock.calls.filter(([ids])=>(ids as string[]).every(id=>id!=='bad'));
 expect(callsWithOnlyOk.length).toBeGreaterThan(0);
 expect(flat().filter(id=>id==='bad').length).toBeLessThan(10); // 没有无限重试
 tracker.dispose();vi.useRealTimers();
});
test('read tracker resets attempts when an id becomes visible again',async()=>{
 vi.useFakeTimers();let failures=0;
 const send=vi.fn(async(ids:string[])=>{if(ids.includes('m1')&&failures<2){failures++;throw new Error('offline')}return ids});
 const tracker=new VisibleReadTracker(send);tracker.setEnabled(true);tracker.visible('m1',true);
 await vi.advanceTimersByTimeAsync(401); // 首次上报失败一次，attempts=1，重试排在 1000ms 后
 tracker.visible('m1',false);tracker.visible('m1',true); // 重新可见：清空重试计时并复位 attempts
 await vi.advanceTimersByTimeAsync(2101); // 新一轮失败后仍按首轮 1s 退避：第 3 次上报落在窗口内（若未复位则要等 2s，落在窗口外）
 expect(send).toHaveBeenCalledTimes(3);expect(failures).toBe(2);
 tracker.dispose();vi.useRealTimers();
});
test('rest errors expose HTTP status for both transports',async()=>{
 const failing=async()=>new Response(JSON.stringify({error:'bad'}),{status:400,headers:{'Content-Type':'application/json'}});
 const browser=createBrowserTransport(failing as typeof fetch);
 const rest=new (await import('../src/api/rest')).RestClient(browser);
 try{await rest.request('POST','/api/v1/conversations/c/read',{messageIds:['m1']});throw new Error('should reject')}catch(e){expect((e as any).status).toBe(400);expect((e as any).message).toBe('bad')}
 const native=(await import('../src/api/rest')).createNativeTransport((async()=>{throw {error:'server rejected',status:422}}) as never);
 const rest2=new (await import('../src/api/rest')).RestClient(native);
 try{await rest2.request('POST','/api/v1/conversations/c/read',{});throw new Error('should reject')}catch(e){expect((e as any).status).toBe(422)}
});
test('browser socket connects to the authenticated API namespace without credentials in URL',async()=>{
 const urls:string[]=[];vi.stubGlobal('WebSocket',class{constructor(url:string){urls.push(url)}close(){}});const {createBrowserAdapter}=await import('../src/runtime/browserAdapter');const adapter=createBrowserAdapter();const remove=await adapter.listen('server.event',()=>{});expect(new URL(urls[0]).pathname).toBe('/api/v1/ws');expect(new URL(urls[0]).search).toBe('');remove();vi.unstubAllGlobals();
});

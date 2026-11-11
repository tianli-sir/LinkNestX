import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { RestClient } from '../src/api/rest';
import { DesktopProvider, type DesktopAdapter } from '../src/runtime/DesktopProvider';
import { ContactsView } from '../src/features/messaging/ContactsView';

afterEach(cleanup);
it('accepts six to ten digit account searches through real form validity',async()=>{
 const request=vi.fn(async(_method:string,path:string):Promise<any>=>path==='/api/v1/contacts'?{friends:[],incoming:[],outgoing:[]}:{id:'colleague',username:'同事',accountNumber:'654321',email:'',roles:['user'],disabled:false,mustChangePassword:false});
 const adapter:DesktopAdapter={kind:'browser',invoke:async<T,>()=>null as T,listen:async()=>()=>{}};
 render(<DesktopProvider adapter={adapter}><ContactsView client={new RestClient({request})} onProfile={()=>{}} onDirect={async()=>{}}/></DesktopProvider>);
 const input=await screen.findByLabelText('通过数字账号添加好友') as HTMLInputElement;
 for(const number of ['654321','7654321','10000001','9876543210']) {
  fireEvent.change(input,{target:{value:number}});expect(input.checkValidity(),number).toBe(true);
 }
 for(const number of ['12345','012345','12345678901','abcdef']) {
  fireEvent.change(input,{target:{value:number}});expect(input.checkValidity(),number).toBe(false);
 }
 expect(input.maxLength).toBe(10);
 fireEvent.change(input,{target:{value:'654321'}});
 expect(input.form!.checkValidity()).toBe(true);
 fireEvent.click(screen.getByRole('button',{name:'查找'}));
 await waitFor(()=>expect(request).toHaveBeenCalledWith('GET','/api/v1/contacts/search?accountNumber=654321',undefined));
 expect(await screen.findByText('654321')).toBeVisible();
});

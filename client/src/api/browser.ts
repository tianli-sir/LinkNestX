import { ApiError, type HttpMethod, type NativeTransport } from './rest';
import type { Session } from '../auth/types';
/** CSRF exists only in this session transport. Authentication remains HttpOnly. */
export function createBrowserTransport(fetcher:typeof fetch=fetch):NativeTransport {
 let csrf='';let generation=0;let refresh:Promise<Session>|undefined;
 async function request<T>(method:HttpMethod,path:string,body?:unknown):Promise<T>{
  const response=await fetcher(path,{method,credentials:'same-origin',headers:{Accept:'application/json',...(body===undefined?{}:{'Content-Type':'application/json'}),...(method==='GET'?{}:{'X-CSRF-Token':csrf})},...(body===undefined?{}:{body:JSON.stringify(body)})});
  if(!response.ok){let message='请求失败，请重试';try{const data=await response.json();if(typeof data.error==='string')message=data.error;}catch{}throw new ApiError(message,response.status);}
  return response.status===204?undefined as T:response.json();
 }
 async function sessionCall(path:string,method:HttpMethod='GET',body?:unknown){const run=++generation;const session=await request<Session>(method,path,body);if(run!==generation)throw new ApiError('登录已取消');csrf=session.csrfToken??'';return {...session,id:`web-${session.user.id}`,deviceId:session.deviceId??'browser'};}
 return {request,login:input=>sessionCall('/api/v1/web/login','POST',{username:input.username,password:input.password}),restore:()=>sessionCall('/api/v1/web/session'),refresh:()=>refresh??(refresh=sessionCall('/api/v1/web/refresh','POST',{}).finally(()=>{refresh=undefined;})),logout:async()=>{generation++;try{await request('POST','/api/v1/web/logout',{});}finally{csrf='';}}};
}

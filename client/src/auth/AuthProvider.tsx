import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { apiErrorMessage, ApiError, createNativeTransport, RestClient, type NativeTransport } from '../api/rest';
import { createBrowserTransport } from '../api/browser';
import { isTauri } from '../lib/tauri';
import type { LoginInput, Session, User } from './types';
export interface PreferenceStorage {getItem(key:string):string|null;setItem(key:string,value:string):void}
export interface AuthContextValue {session:Session|null;serverUrl:string;client:RestClient;login(input:LoginInput):Promise<void>;logout():Promise<void>;changePassword(currentPassword:string,newPassword:string):Promise<void>;updateUser(user:User):void}
const AuthContext=createContext<AuthContextValue|null>(null);
export interface AuthProviderProps {children:ReactNode;transport?:NativeTransport;storage?:PreferenceStorage}
export function AuthProvider({children,transport}:AuthProviderProps){
 const runtime=useMemo(()=>transport??(isTauri()?createNativeTransport():createBrowserTransport()),[transport]);
 const client=useMemo(()=>new RestClient(runtime),[runtime]);const [session,setSession]=useState<Session|null>(null);const operation=useRef(0);
 useEffect(()=>{if(!runtime.restore)return;const generation=++operation.current;void runtime.restore().then(value=>{if(generation===operation.current)setSession(value)}).catch(()=>{});return()=>{operation.current++}},[runtime]);
 useEffect(()=>{if(!session||!runtime.refresh)return;const delay=Math.max(1000,Math.min(2147483647,new Date(session.expiresAt).valueOf()-Date.now()-60000));const generation=operation.current;const timer=setTimeout(()=>{void runtime.refresh!().then(value=>{if(generation===operation.current)setSession(value)}).catch(()=>{if(generation===operation.current)setSession(null)})},Number.isFinite(delay)?delay:60000);return()=>clearTimeout(timer)},[session,runtime]);
 const login=useCallback(async({username,password,deviceId}:LoginInput)=>{const generation=++operation.current;try{const result=await runtime.login({username,password,...(deviceId?{deviceId}:{})});if(generation===operation.current)setSession(result)}catch(error){throw new ApiError(apiErrorMessage(error,'登录失败，请重试'))}},[runtime]);
 const logout=useCallback(async()=>{operation.current++;setSession(null);await runtime.logout()},[runtime]);
 const changePassword=useCallback(async(currentPassword:string,newPassword:string)=>{const generation=operation.current;const user=await client.request<User>('POST','/api/v1/auth/password',{currentPassword,newPassword});if(generation===operation.current)setSession(previous=>previous?{...previous,user}:null)},[client]);
 const updateUser=useCallback((user:User)=>setSession(previous=>previous?.user.id===user.id?{...previous,user}:previous),[]);
 const value=useMemo(()=>({updateUser,session,serverUrl:'',client,login,logout,changePassword}),[session,client,login,logout,changePassword,updateUser]);return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
export function useAuth(){const context=useContext(AuthContext);if(!context)throw new Error('useAuth must be used inside AuthProvider');return context}

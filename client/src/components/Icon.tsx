import { useId, type SVGProps } from "react";
import { ArrowsDownUp, BookOpen, CaretDown, ChatsCircle, GearSix, HouseSimple, Info, LockKey, FolderSimple, ShieldCheck, SignOut, Users, WifiHigh, WifiSlash } from "@phosphor-icons/react";
const icons={chat:ChatsCircle,documents:BookOpen,groups:Users,folder:FolderSimple,transfers:ArrowsDownUp,admin:ShieldCheck,settings:GearSix,logout:SignOut,chevron:CaretDown,server:HouseSimple,lock:LockKey,info:Info,connected:WifiHigh,disconnected:WifiSlash};
export type IconName=keyof typeof icons|"nest";
export interface IconProps extends Omit<SVGProps<SVGSVGElement>,"name">{name:IconName;size?:number;title?:string}
export function Icon({name,size=20,title,...props}:IconProps){const titleId=useId();if(name==='nest'){return <NestMark size={size} aria-hidden={title?undefined:true} aria-label={title} role={title?'img':undefined} focusable="false" {...props}/>}const Glyph=icons[name];return <Glyph size={size} weight="regular" aria-hidden={title?undefined:true} aria-label={title} role={title?'img':undefined} focusable="false" {...props}/>}
function NestMark({size=20,...props}:SVGProps<SVGSVGElement>&{size?:number}){
  return <svg width={size} height={size*36/32} viewBox="0 0 32 36" fill="none" {...props}>
    <path d="M5 8 16 2l11 6v13l-11 7L5 21Z" stroke="currentColor" strokeWidth="2"/>
    <path d="m5 8 11 7 11-7M16 15v13M5 15l11 7 11-7" stroke="currentColor" strokeWidth="2"/>
    <circle cx="16" cy="33" r="2" fill="currentColor"/>
  </svg>;
}

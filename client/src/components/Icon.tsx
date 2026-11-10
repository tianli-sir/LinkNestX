import { useId, type SVGProps } from "react";
import { ArrowsDownUp, BookOpen, CaretDown, ChatsCircle, GearSix, HouseSimple, Info, LockKey, FolderSimple, ShieldCheck, SignOut, Stack, Users, WifiHigh, WifiSlash } from "@phosphor-icons/react";
const icons={nest:Stack,chat:ChatsCircle,documents:BookOpen,groups:Users,folder:FolderSimple,transfers:ArrowsDownUp,admin:ShieldCheck,settings:GearSix,logout:SignOut,chevron:CaretDown,server:HouseSimple,lock:LockKey,info:Info,connected:WifiHigh,disconnected:WifiSlash};
export type IconName=keyof typeof icons;
export interface IconProps extends Omit<SVGProps<SVGSVGElement>,"name">{name:IconName;size?:number;title?:string}
export function Icon({name,size=20,title,...props}:IconProps){const titleId=useId();const Glyph=icons[name];return <Glyph size={size} weight="regular" aria-hidden={title?undefined:true} aria-label={title} role={title?'img':undefined} focusable="false" {...props}/>}

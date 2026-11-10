import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { CircleHalf, Moon, Sun } from '@phosphor-icons/react';
export type Appearance = 'system' | 'light' | 'dark';
const Context = createContext<{preference:Appearance;setPreference:(value:Appearance)=>void}>({preference:'system',setPreference:()=>{}});
const valid = (value:unknown):value is Appearance => value==='system'||value==='light'||value==='dark';
export function ThemeProvider({children}:{children:ReactNode}) {
  const [preference,setPreference] = useState<Appearance>(()=>{try{const value=localStorage.getItem('linknestx.appearance');return valid(value)?value:'system'}catch{return 'system'}});
  useEffect(()=>{
    const media=typeof matchMedia==='function'?matchMedia('(prefers-color-scheme: dark)'):null;
    const apply=()=>{const theme=preference==='system'?(media?.matches?'dark':'light'):preference;document.documentElement.dataset.theme=theme;document.documentElement.style.colorScheme=theme};
    apply();media?.addEventListener('change',apply);
    try{localStorage.setItem('linknestx.appearance',preference)}catch{/* Appearance still works when storage is unavailable. */}
    return()=>media?.removeEventListener('change',apply);
  },[preference]);
  return <Context.Provider value={{preference,setPreference}}>{children}</Context.Provider>;
}
export const useTheme=()=>useContext(Context);
export function ThemeControl(){const theme=useTheme();const Glyph=theme.preference==='dark'?Moon:theme.preference==='light'?Sun:CircleHalf;return <label className="ln-theme-control"><Glyph size={17}/><select aria-label="外观主题" value={theme.preference} onChange={event=>{if(valid(event.target.value))theme.setPreference(event.target.value)}}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select></label>}

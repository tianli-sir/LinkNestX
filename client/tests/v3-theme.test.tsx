import { fireEvent, render, screen, cleanup, act } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { ThemeControl, ThemeProvider } from '../src/runtime/ThemeProvider';
afterEach(()=>{cleanup();localStorage.clear();vi.unstubAllGlobals()});
it('remembers explicit appearance and responds to system changes only in system mode',()=>{
 let change:()=>void=()=>{};let dark=false;vi.stubGlobal('matchMedia',()=>({get matches(){return dark},addEventListener:(_:string,fn:()=>void)=>change=fn,removeEventListener:()=>{}}));
 render(<ThemeProvider><ThemeControl/></ThemeProvider>);expect(document.documentElement.dataset.theme).toBe('light');fireEvent.change(screen.getByLabelText('外观主题'),{target:{value:'dark'}});expect(document.documentElement.dataset.theme).toBe('dark');expect(localStorage.getItem('linknestx.appearance')).toBe('dark');
 fireEvent.change(screen.getByLabelText('外观主题'),{target:{value:'system'}});expect(document.documentElement.dataset.theme).toBe('light');dark=true;act(()=>change());expect(document.documentElement.dataset.theme).toBe('dark');
});

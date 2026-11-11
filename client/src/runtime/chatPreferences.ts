import { useSyncExternalStore } from 'react';
export type SendShortcut = 'enter' | 'ctrlEnter';
const key = 'linknestx.chat.sendShortcut';
const changed = 'linknestx:chat-preferences';
let memory: SendShortcut = 'enter';
let memoryOverride = false;
function snapshot(): SendShortcut {
  if (memoryOverride) return memory;
  try { const value = localStorage.getItem(key); memory = value === 'ctrlEnter' ? value : 'enter'; return memory; }
  catch { return memory; }
}
function subscribe(callback: () => void) {
  const storage = (event: StorageEvent) => { if (event.key === key || event.key === null) callback(); };
  window.addEventListener(changed, callback); window.addEventListener('storage', storage);
  return () => { window.removeEventListener(changed, callback); window.removeEventListener('storage', storage); };
}
function setSendShortcut(value: SendShortcut) {
  if (value !== 'enter' && value !== 'ctrlEnter') return;
  memory = value;
  try { localStorage.setItem(key, value); memoryOverride = false; } catch { memoryOverride = true; }
  window.dispatchEvent(new Event(changed));
}
export function useChatPreferences() {
  const sendShortcut = useSyncExternalStore(subscribe, snapshot, () => 'enter' as SendShortcut);
  return { sendShortcut, setSendShortcut };
}

import type { DesktopAdapter } from './DesktopProvider';
import type { ConnectionState } from '../components/AppShell';
/** One socket per authenticated workspace. Credentials never appear in its URL. */
export function createBrowserAdapter(): DesktopAdapter {
  const callbacks = new Map<string, Set<(payload: any) => void>>();
  let socket: WebSocket | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let deadline: ReturnType<typeof setTimeout> | undefined;
  let closed = false;
  let state: ConnectionState = { status: 'disconnected' };
  const emit = (name: string, payload: any) => callbacks.get(name)?.forEach(callback => callback(payload));
  const change = (status: ConnectionState['status']) => { state = { status }; emit('connection.changed', state); };
  const clearTimers = () => { clearTimeout(timer); clearTimeout(deadline); };
  const connect = () => {
    if (closed || socket) return;
    clearTimers(); change('connecting');
    try {
      socket = new WebSocket((location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host + '/api/v1/ws');
      const instance = socket;
      deadline = setTimeout(() => {
        if (socket !== instance) return;
        socket = undefined; instance.close(); change('disconnected');
        if (!closed) timer = setTimeout(connect, 3000);
      }, 15000);
      socket.onopen = () => { if (socket === instance) { clearTimeout(deadline); change('connected'); } };
      socket.onmessage = event => { if (socket === instance) { try { emit('server.event', JSON.parse(event.data)); } catch {} } };
      socket.onerror = () => { if (socket === instance) change('error'); };
      socket.onclose = () => {
        if (socket !== instance) return;
        clearTimeout(deadline); socket = undefined; change('disconnected');
        if (!closed) timer = setTimeout(connect, 3000);
      };
    } catch { socket = undefined; change('error'); if (!closed) timer = setTimeout(connect, 3000); }
  };
  return {
    kind: 'browser',
    invoke: async <T>(command: string) => {
      if (command === 'get_connection_status') return { ...state } as T;
      if (command === 'reconnect_connection') { clearTimers(); const previous = socket; socket = undefined; previous?.close(); closed = false; connect(); return undefined as T; }
      if (command === 'list_transfers') return [] as T;
      if (command === 'get_background_status') return { sharingEnabled: false, activeUploads: 0 } as T;
      throw new Error('此功能请使用桌面客户端');
    },
    listen: async (name, callback) => {
      closed = false;
      if (!callbacks.has(name)) callbacks.set(name, new Set());
      callbacks.get(name)!.add(callback); connect();
      if (name === 'connection.changed') callback({ ...state });
      return () => {
        callbacks.get(name)?.delete(callback);
        if ([...callbacks.values()].every(set => !set.size)) {
          closed = true; clearTimers(); const previous = socket; socket = undefined; previous?.close(); state = { status: 'disconnected' };
        }
      };
    },
  };
}

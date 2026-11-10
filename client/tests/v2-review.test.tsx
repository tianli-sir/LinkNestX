import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { App } from '../src/main';
import type { Transfer } from '../src/features/TransfersView';
const me = { id: 'u1', username: 'lin', displayName: '林晓', accountNumber: '10000001', email: '', roles: ['employee'], disabled: false, mustChangePassword: false };
const peer = { ...me, id: 'u2', username: 'zhou', displayName: '周予', accountNumber: '10000002' };
const conversations = [{ id: 'c1', kind: 'direct', title: '周予', peer, memberCount: 2, unreadCount: 0, canSend: true },{ id: 'c2', kind: 'direct', title: '另一会话', peer, memberCount: 2, unreadCount: 0, canSend: true }];
const message = { id: 'm1', sequence: 1, conversationId: 'c1', groupId: 'c1', senderId: peer.id, sender: peer, clientMessageId: 'm1', body: '', createdAt: '2026-11-10T06:00:00Z', cursor: 'm1', readCount: 0, readByMe: true, fileId: 'file', fileVersionId: 'original-version', attachmentName: '历史版本.txt', attachmentSize: 2000, attachmentStatus: 'available' };
const completedTask = (direction: 'upload' | 'download', historyComplete?: boolean): Transfer => ({id: 'task', direction, groupId: 'c1', versionId: 'original-version', messageId: direction === 'download' ? 'm1' : undefined, name: message.attachmentName, state: 'completed', bytesCompleted: 2000, totalBytes: 2000, activeSources: [],metrics: { revision: 1, historyComplete, activeDurationMs: 1000, averageBytesPerSecond: 2000 }});
async function setup(tasks: Transfer[] = []) {
 const listeners = new Map<string, (event: any) => void>();
 const invoke = vi.fn(async (command: string): Promise<any> => command === 'list_transfers' ? tasks : command === 'pick_download_destination' ? '/chosen/another-copy.txt' : command === 'start_download' ? 'another-download' : command === 'read_clipboard_attachments' ? [{id:'clipboard-handle',name:'系统剪贴板文件.png',size:40}] : undefined);
 render(<App transport={{login: async () => ({ id: 'review-session', user: me, deviceId: 'd', expiresAt: '' }), logout: async () => {},request: async <T,>(_method: string, path: string) => (path === '/api/v1/conversations' ? conversations : path.includes('/messages?') ? { messages: [message], hasMore: false, nextCursor: '' } : []) as T}} desktop={{ kind: 'native', invoke, listen: async (name, callback) => { listeners.set(name, callback); return () => { listeners.delete(name); }; } }} />);
 fireEvent.change(screen.getByLabelText('账号'), { target: { value: me.accountNumber } });fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'password' } });fireEvent.click(screen.getByRole('button', { name: '登录' }));await screen.findByRole('button', { name: '周予' });return { invoke, listeners };
}
test.each(['upload', 'download'] as const)('completed %s preserves exact-version download alongside statistics', async direction => {
 const h = await setup();fireEvent.click(screen.getByRole('button', { name: '周予' }));await screen.findByRole('button', { name: '下载文件' });await act(async () => h.listeners.get('transfer.updated')?.(completedTask(direction, true)));
 expect(screen.getByLabelText('历史版本.txt传输任务')).toBeInTheDocument();fireEvent.click(screen.getByRole('button', { name: '下载文件' }));await waitFor(() => expect(h.invoke).toHaveBeenCalledWith('start_download', { versionId: 'original-version', destination: '/chosen/another-copy.txt', messageId: 'm1', groupId: 'c1' }));
});
test.each(['center', 'event'] as const)('each %s navigation request selects the task conversation even when its ID repeats', async source => {
 await setup([{ ...completedTask('upload'), state: 'paused' }]);async function open() {if (source === 'event') await act(async () => window.dispatchEvent(new CustomEvent('linknestx:open-conversation', { detail: { id: 'c1' } })));else {fireEvent.click(screen.getByRole('button', { name: '传输' }));const back=await screen.findByRole('button', { name: '返回会话' });await act(async()=>fireEvent.click(back));}}
 await open();expect(await screen.findByRole('heading', { name: '周予' })).toBeInTheDocument();await act(async()=>fireEvent.click(screen.getByRole('button', { name: '另一会话' })));expect(await screen.findByRole('heading', { name: '另一会话' })).toBeInTheDocument();await open();expect(await screen.findByRole('heading', { name: '周予' })).toBeInTheDocument();expect(screen.queryByRole('heading', { name: '另一会话' })).not.toBeInTheDocument();
});
test.each([false, undefined])('legacy historyComplete=%s labels partial measurements without inventing lifetime totals', async completeness => {
 await setup([completedTask('download', completeness)]);fireEvent.click(screen.getByRole('button', { name: '传输' }));fireEvent.click(await screen.findByRole('button', { name: '已完成 · 1' }));const card = within(screen.getByLabelText('历史版本.txt传输任务'));expect(card.getByText(/统计期间用时 1 秒 · 期间平均 2.0 KB\/s/)).toBeInTheDocument();expect(card.getByText('早期历史未统计，以上仅为已记录期间的数据。')).toBeInTheDocument();expect(card.queryByText(/总用时/)).not.toBeInTheDocument();
});
test('complete transfer history keeps lifetime duration and average labels', async () => {
 await setup([completedTask('download', true)]);fireEvent.click(screen.getByRole('button', { name: '传输' }));fireEvent.click(await screen.findByRole('button', { name: '已完成 · 1' }));const card = within(screen.getByLabelText('历史版本.txt传输任务'));expect(card.getByText(/总用时 1 秒 · 平均 2.0 KB\/s/)).toBeInTheDocument();expect(card.queryByText(/早期历史未统计/)).not.toBeInTheDocument();
});
test('unmeasured legacy completion retains unknown values instead of zero time or speed',async()=>{
 const task=completedTask('download');task.metrics={revision:0,historyComplete:false,activeDurationMs:null,averageBytesPerSecond:null};await setup([task]);fireEvent.click(screen.getByRole('button',{name:'传输'}));fireEvent.click(await screen.findByRole('button',{name:'已完成 · 1'}));expect(within(screen.getByLabelText('历史版本.txt传输任务')).getByText('统计期间用时 暂无统计 · 期间平均 暂无统计')).toBeInTheDocument();
});
test('reopening a message keeps its newer active download instead of the old completed save',async()=>{
 const old=completedTask('download',true);const h=await setup([old]);fireEvent.click(screen.getByRole('button',{name:'周予'}));fireEvent.click(await screen.findByRole('button',{name:'下载文件'}));await waitFor(()=>expect(h.invoke).toHaveBeenCalledWith('start_download',expect.anything()));await act(async()=>h.listeners.get('transfer.updated')?.({...old,id:'another-download',state:'transferring',bytesCompleted:500,metrics:{revision:2,historyComplete:true}}));await act(async()=>fireEvent.click(screen.getByRole('button',{name:'另一会话'})));await act(async()=>fireEvent.click(screen.getByRole('button',{name:'周予'})));const card=screen.getByLabelText('历史版本.txt传输任务');expect(within(card).getByRole('progressbar')).toHaveAttribute('value','500');expect(within(card).getByRole('button',{name:'暂停'})).toBeEnabled();expect(screen.queryByRole('button',{name:'下载文件'})).not.toBeInTheDocument();
});
test.each([
 {label:'Files',types:['Files'],text:''},
 {label:'URI list',types:['text/uri-list'],text:''},
 {label:'image',types:['image/png'],text:''},
 {label:'empty exposed data',types:[],text:''},
 {label:'file URI text',types:['text/plain'],text:'file:///private/copied-file.png'},
])('paste with $label hints and no browser files prepares trusted native attachments',async({types,text})=>{
 const h=await setup();fireEvent.click(screen.getByRole('button',{name:'周予'}));const composer=await screen.findByLabelText('消息内容');const pasted=fireEvent.paste(composer,{clipboardData:{files:[],types,getData:()=>text}});expect(pasted).toBe(false);await waitFor(()=>expect(h.invoke).toHaveBeenCalledWith('read_clipboard_attachments'));expect(await screen.findByText('系统剪贴板文件.png')).toBeInTheDocument();expect(composer).toHaveValue('');expect(h.invoke.mock.calls.some(([command])=>command==='start_attachment_upload')).toBe(false);
});
test.each([{types:['text/plain']},{types:['text/plain','text/uri-list']}])('ordinary plain text paste keeps the browser text behavior with $types',async({types})=>{
 const h=await setup();fireEvent.click(screen.getByRole('button',{name:'周予'}));const composer=await screen.findByLabelText('消息内容');expect(fireEvent.paste(composer,{clipboardData:{files:[],types,getData:()=> '普通文字与 https://example.test'}})).toBe(true);expect(h.invoke.mock.calls.some(([command])=>command==='read_clipboard_attachments')).toBe(false);
});

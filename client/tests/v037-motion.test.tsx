import { fireEvent, render, screen } from '@testing-library/react';
import { App } from '../src/main';
import { swipeDeleteTriggered, SWIPE_THRESHOLD } from '../src/motion';

const me = { id: 'u1', username: 'lin', displayName: '林晓', accountNumber: '100001', email: '', roles: ['employee'], disabled: false, mustChangePassword: false };
const peer = { ...me, id: 'u2', username: 'zhou', displayName: '周予', accountNumber: '100002' };
const direct = { id: 'c1', kind: 'direct' as const, title: '周予', peer, memberCount: 2, unreadCount: 0, canSend: true };
const listeners = new Map<string, (value: any) => void>();

function loginApp(request: (method: string, path: string) => Promise<any>) {
  render(<App transport={{ request, login: async () => ({ id: 'session', user: me, deviceId: 'd', expiresAt: '' }), logout: async () => {} }} desktop={{ kind: 'native', invoke: async <T,>() => ([] as T), listen: async (name: string, callback: (value: any) => void) => { listeners.set(name, callback); return () => listeners.delete(name); } }} />);
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: me.accountNumber } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'pw' } });
  fireEvent.click(screen.getByRole('button', { name: '登录' }));
}

test('swipe threshold is strictly past -64 pixels', () => {
  expect(SWIPE_THRESHOLD).toBe(-64);
  expect(swipeDeleteTriggered(-96)).toBe(true);
  expect(swipeDeleteTriggered(-65)).toBe(true);
  expect(swipeDeleteTriggered(-64)).toBe(false);
  expect(swipeDeleteTriggered(-63)).toBe(false);
  expect(swipeDeleteTriggered(0)).toBe(false);
});

test('persistent messaging page leaves the accessibility tree while another view is active', async () => {
  loginApp(async (_method, path) => path === '/api/v1/conversations' ? [direct] : path === '/api/v1/contacts' ? { friends: [], incoming: [], outgoing: [] } : path.includes('/messages?') ? { messages: [], hasMore: false, nextCursor: '' } : []);
  await screen.findByRole('button', { name: '周予' });
  fireEvent.click(screen.getByRole('button', { name: '文件' }));
  expect(await screen.findByRole('heading', { name: '文件' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '周予' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: '消息' }));
  expect(await screen.findByRole('button', { name: '周予' })).toBeInTheDocument();
});

test('conversation rows still filter instantly with motion wrappers', async () => {
  const group = { id: 'g1', kind: 'group' as const, title: '项目组', memberCount: 3, unreadCount: 0, canSend: true };
  loginApp(async (_method, path) => path === '/api/v1/conversations' ? [group, direct] : path === '/api/v1/contacts' ? { friends: [], incoming: [], outgoing: [] } : path.includes('/messages?') ? { messages: [], hasMore: false, nextCursor: '' } : []);
  await screen.findByRole('button', { name: '项目组' });
  fireEvent.click(screen.getByRole('button', { name: '仅看私聊' }));
  expect(screen.queryByRole('button', { name: '项目组' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: '仅看群聊' }));
  expect(screen.queryByRole('button', { name: '周予' })).not.toBeInTheDocument();
});

test('message bubbles render visible immediately after conversation open', async () => {
  const message = { id: 'm1', sequence: 1, conversationId: 'c1', groupId: 'c1', senderId: peer.id, sender: peer, clientMessageId: 'm1', body: '你好', createdAt: '2026-11-14T01:00:00Z', cursor: 'm1', readCount: 0, readByMe: false };
  loginApp(async (_method, path) => path === '/api/v1/conversations' ? [direct] : path === '/api/v1/contacts' ? { friends: [], incoming: [], outgoing: [] } : path.includes('/messages?') ? { messages: [message], hasMore: false, nextCursor: '' } : path.endsWith('/read') ? { messageIds: [] } : []);
  fireEvent.click(await screen.findByRole('button', { name: '周予' }));
  const bubble = await screen.findByText('你好');
  expect(bubble).toBeVisible();
});

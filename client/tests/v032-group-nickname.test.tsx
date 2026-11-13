import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { App } from '../src/main';

const me = { id: 'u1', username: 'me', displayName: '我', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000001' };
const peer = { id: 'u2', username: 'peer', displayName: '周予', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000002' };
const groupConversation = { id: 'g1', kind: 'group', title: '项目群', memberCount: 2, unreadCount: 0, canSend: true };

function setup() {
  const listeners = new Map<string, (value: any) => void>();
  const patches: Array<{ path: string; body: any }> = [];
  const request = vi.fn(async (method: string, path: string, body?: any): Promise<any> => {
    if (path === '/api/v1/conversations') return [groupConversation];
    if (path === '/api/v1/groups') return [{ id: 'g1', name: '项目群', isMember: true }];
    if (path === '/api/v1/users') return [];
    if (path === '/api/v1/groups/g1/members') return [
      { userId: 'u1', role: 'member', joinedAt: '2026-11-10', displayName: '我', nickname: null, accountNumber: '10000001', hasAvatar: false, disabled: false },
      { userId: 'u2', role: 'owner', joinedAt: '2026-11-10', displayName: '群主阿周', nickname: '阿周', accountNumber: '10000002', hasAvatar: false, disabled: false },
    ];
    if (path === '/api/v1/contacts') return { friends: [], incoming: [], outgoing: [] };
    if (path.startsWith('/api/v1/profiles/')) return path.startsWith(`/api/v1/profiles/${me.id}`) ? me : peer;
    if (path.includes('/messages?')) return { messages: [], hasMore: false, nextCursor: '' };
    if (path.endsWith('/read')) return { messageIds: body.messageIds };
    if (method === 'PATCH') { patches.push({ path, body }); return {}; }
    throw new Error(path);
  });
  const invoke = vi.fn(async (name: string): Promise<any> => (name === 'list_transfers' ? [] : undefined));
  render(<App transport={{ request, login: async () => ({ id: 'session', user: me, deviceId: 'd', expiresAt: '' }), logout: async () => {} }} desktop={{ kind: 'native', invoke, listen: async (name, callback) => { listeners.set(name, callback); return () => listeners.delete(name); } }} />);
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: me.accountNumber } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'pw' } });
  fireEvent.click(screen.getByRole('button', { name: '登录' }));
  return { request, patches, listeners };
}

test('群内点自己头像只编辑群昵称，不动全局名', async () => {
  const h = setup();
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  fireEvent.click(await screen.findByRole('button', { name: '群组与成员管理' }));
  fireEvent.click(await screen.findByRole('button', { name: '查看我的资料' }));
  const dialog = await screen.findByRole('dialog', { name: '个人资料' });
  const input = dialog.querySelector('#group-nickname') as HTMLInputElement;
  expect(input).not.toBeNull();
  fireEvent.change(input, { target: { value: '群里的我' } });
  fireEvent.submit(dialog.querySelector('form')!);
  await waitFor(() => expect(h.patches.some(p => p.path === '/api/v1/groups/g1/members/u1' && JSON.stringify(p.body).includes('群里的我'))).toBe(true));
  expect(h.patches.some(p => p.path === '/api/v1/profile')).toBe(false);
});

test('群内他人资料显示本群昵称', async () => {
  const h = setup();
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  fireEvent.click(await screen.findByRole('button', { name: '群组与成员管理' }));
  fireEvent.click(await screen.findByRole('button', { name: '查看群主阿周的资料' }));
  expect(await screen.findByText(/本群昵称/)).toBeInTheDocument();
});

test('nickname.changed 事件刷新会话与成员', async () => {
  const h = setup();
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  await screen.findByLabelText('消息内容');
  const conversationsBefore = h.request.mock.calls.filter(([m, p]) => m === 'GET' && p === '/api/v1/conversations').length;
  const membersBefore = h.request.mock.calls.filter(([m, p]) => m === 'GET' && p === '/api/v1/groups/g1/members').length;
  await act(async () => { h.listeners.get('server.event')?.({ type: 'nickname.changed', groupId: 'g1', userId: 'u2' }); });
  await waitFor(() => expect(h.request.mock.calls.filter(([m, p]) => m === 'GET' && p === '/api/v1/conversations').length).toBeGreaterThan(conversationsBefore));
  await waitFor(() => expect(h.request.mock.calls.filter(([m, p]) => m === 'GET' && p === '/api/v1/groups/g1/members').length).toBeGreaterThan(membersBefore));
});

import { fireEvent, render, screen } from '@testing-library/react';
import { App } from '../src/main';

const me = { id: 'u1', username: 'me', displayName: '我', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000001' };
const groupConversation = { id: 'g1', kind: 'group', title: '项目群', memberCount: 2, unreadCount: 0, canSend: true };
const baseMessage = { id: 'm1', sequence: 1, groupId: 'g1', conversationId: 'g1', senderId: 'u2', sender: { id: 'u2', username: 'peer', displayName: '周予', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000002' }, clientMessageId: 'c1', body: 'photo.png', createdAt: '2026-11-13T01:00:00Z', cursor: '', readCount: 0, readByMe: false, fileId: 'f1', fileVersionId: 'v1', attachmentName: 'photo.png', attachmentSize: 100, attachmentStatus: 'available' };

function setup(failThumb = false) {
  const invoke = vi.fn(async (name: string): Promise<any> => {
    if (name === 'list_transfers') return [];
    if (name === 'file_attachment_thumb') return failThumb ? Promise.reject(new Error('too large')) : Promise.resolve('data:image/png;base64,QUJD');
    if (name === 'file_attachment_preview') return Promise.resolve('data:image/png;base64,REVGRQ');
    return undefined;
  });
  const request = vi.fn(async (method: string, path: string): Promise<any> => {
    if (path === '/api/v1/conversations') return [groupConversation];
    if (path === '/api/v1/groups/g1/members') return [];
    if (path === '/api/v1/contacts') return { friends: [], incoming: [], outgoing: [] };
    if (path.includes('/messages?')) return { messages: [{ ...baseMessage, attachmentKind: 'image' }], hasMore: false, nextCursor: '' };
    if (path.endsWith('/read')) return { messageIds: [] };
    return null;
  });
  render(<App transport={{ request, login: async () => ({ id: 'session', user: me, deviceId: 'd', expiresAt: '' }), logout: async () => {} }} desktop={{ kind: 'native', invoke, listen: async () => () => {} }} />);
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: me.accountNumber } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'pw' } });
  fireEvent.click(screen.getByRole('button', { name: '登录' }));
  return { invoke };
}

test('image attachment renders a thumbnail instead of the file card', async () => {
  const h = setup();
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  const thumb = await screen.findByRole('button', { name: '查看图片 photo.png' });
  expect(thumb.querySelector('img')?.getAttribute('src')).toContain('data:image/png;base64');
  expect(h.invoke).toHaveBeenCalledWith('file_attachment_thumb', { versionId: 'v1' });
  expect(screen.queryByRole('button', { name: '下载文件' })).toBeNull();
});

test('thumb failure falls back to the ordinary file card', async () => {
  setup({ failThumb: true } as never);
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  expect(await screen.findByRole('button', { name: '下载文件' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '查看图片 photo.png' })).toBeNull();
});

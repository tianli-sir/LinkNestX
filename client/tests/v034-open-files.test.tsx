import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { App } from '../src/main';

const me = { id: 'u1', username: 'me', displayName: '我', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000001' };
const groupConversation = { id: 'g1', kind: 'group', title: '项目群', memberCount: 2, unreadCount: 0, canSend: true };
const fileMessage = { id: 'm1', sequence: 1, groupId: 'g1', conversationId: 'g1', senderId: 'u2', sender: { id: 'u2', username: 'peer', displayName: '周予', email: '', roles: [], disabled: false, mustChangePassword: false, accountNumber: '10000002' }, clientMessageId: 'c1', body: 'report.pdf', createdAt: '2026-11-13T01:00:00Z', cursor: '', readCount: 0, readByMe: false, fileId: 'f1', fileVersionId: 'v1', attachmentName: 'report.pdf', attachmentSize: 4000, attachmentStatus: 'available' };
const completedDownload = { id: 't1', direction: 'download', versionId: 'v1', name: 'report.pdf', state: 'completed', bytesCompleted: 4000, totalBytes: 4000, activeSources: [], localPath: 'C:/Downloads/report.pdf' };

function setupApp(overrides: { failOpen?: boolean } = {}) {
  const invoke = vi.fn(async (name: string, args?: any): Promise<any> => {
    if (name === 'list_transfers') return [completedDownload];
    if (name === 'check_downloaded_files') return { t1: true };
    if (name === 'open_downloaded_file') {
      if (!overrides.failOpen) return undefined;
      throw { error: '文件已不在原位置' };
    }
    if (name === 'reveal_downloaded_file') return Promise.resolve();
    return undefined;
  });
  const request = vi.fn(async (method: string, path: string): Promise<any> => {
    if (path === '/api/v1/conversations') return [groupConversation];
    if (path === '/api/v1/groups/g1/members') return [];
    if (path === '/api/v1/contacts') return { friends: [], incoming: [], outgoing: [] };
    if (path.includes('/messages?')) return { messages: [fileMessage], hasMore: false, nextCursor: '' };
    if (path.endsWith('/read')) return { messageIds: [] };
    return null;
  });
  render(<App transport={{ request, login: async () => ({ id: 'session', user: me, deviceId: 'd', expiresAt: '' }), logout: async () => {} }} desktop={{ kind: 'native', invoke, listen: async () => () => {} }} />);
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: me.accountNumber } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'pw' } });
  fireEvent.click(screen.getByRole('button', { name: '登录' }));
  return { invoke };
}

test('chat file message offers open and reveal for a local completed download', async () => {
  const h = setupApp();
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  fireEvent.click(await screen.findByRole('button', { name: '打开文件' }));
  expect(h.invoke).toHaveBeenCalledWith('open_downloaded_file', { taskId: 't1' });
  fireEvent.click(await screen.findByRole('button', { name: '打开文件夹' }));
  expect(h.invoke).toHaveBeenCalledWith('reveal_downloaded_file', { taskId: 't1' });
});

test('FileMissing falls back to the download button with a notice', async () => {
  const h = setupApp({ failOpen: true });
  fireEvent.click(await screen.findByRole('button', { name: '项目群' }));
  fireEvent.click(await screen.findByRole('button', { name: '打开文件' }));
  expect(await screen.findByText(/文件已不在原位置/)).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByRole('button', { name: '打开文件' })).toBeNull());
  expect(await screen.findByRole('button', { name: '下载文件' })).toBeInTheDocument();
  expect(h.invoke).toHaveBeenCalledWith('check_downloaded_files', { taskIds: ['t1'] });
});

test('transfer center cards expose the same open action', async () => {
  const invoke = vi.fn(async (name: string, args?: any): Promise<any> => {
    if (name === 'list_transfers') return [completedDownload];
    if (name === 'check_downloaded_files') return { [args?.taskIds?.[0] ?? 't1']: true };
    return undefined;
  });
  render(<App transport={{ request: (async () => null) as never, login: async () => ({ id: 'session', user: me, deviceId: 'd', expiresAt: '' }), logout: async () => {} }} desktop={{ kind: 'native', invoke, listen: async () => () => {} }} />);
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: me.accountNumber } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'pw' } });
  fireEvent.click(screen.getByRole('button', { name: '登录' }));
  fireEvent.click(await screen.findByRole('button', { name: '传输' }));
  fireEvent.click(await screen.findByRole('button', { name: /已完成 · / }));
  fireEvent.click(await screen.findByRole('button', { name: '打开文件' }));
  expect(invoke).toHaveBeenCalledWith('open_downloaded_file', { taskId: 't1' });
});

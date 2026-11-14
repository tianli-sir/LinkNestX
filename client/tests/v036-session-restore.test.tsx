import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from '../src/main';
import type { Session } from '../src/auth';

afterEach(cleanup);
const me = { id: 'u1', username: 'lin', displayName: '林晓', accountNumber: '100001', email: '', roles: ['employee'], disabled: false, mustChangePassword: false };
const session: Session = { id: 'restored', user: me, deviceId: 'd', expiresAt: '2099-01-01T00:00:00Z' };
const conversations = [{ id: 'c1', kind: 'direct' as const, title: '周予', memberCount: 2, unreadCount: 0, canSend: true }];

function mount(restore?: () => Promise<Session>) {
  const request = vi.fn(async (_method: string, path: string): Promise<any> =>
    path === '/api/v1/conversations' ? conversations : path === '/api/v1/contacts' ? { friends: [], incoming: [], outgoing: [] } : path.includes('/messages?') ? { messages: [], hasMore: false, nextCursor: '' } : []);
  render(<App transport={{ request, login: async () => { throw new Error('should not re-login'); }, logout: async () => {}, ...(restore ? { restore } : {}) }} desktop={{ kind: 'native', invoke: async <T,>() => ([] as T), listen: async () => () => {} }} />);
  return { request };
}

it('shows the restore splash, then enters the workspace without a login form', async () => {
  let finish!: (value: Session) => void;
  const restore = vi.fn(() => new Promise<Session>(resolve => { finish = resolve; }));
  mount(restore);
  expect(restore).toHaveBeenCalledTimes(1);
  expect(screen.getByTestId('restore-splash')).toBeInTheDocument();
  expect(screen.queryByTestId('login-route')).not.toBeInTheDocument();
  finish(session);
  expect(await screen.findByRole('button', { name: '周予' })).toBeInTheDocument();
  expect(screen.queryByTestId('restore-splash')).not.toBeInTheDocument();
});

it('falls back to the login screen when restore rejects', async () => {
  mount(vi.fn(async () => { throw new Error('没有可恢复的登录'); }));
  await screen.findByTestId('login-route');
  await waitFor(() => expect(screen.queryByTestId('restore-splash')).not.toBeInTheDocument());
});

it('renders the login screen immediately when the transport cannot restore', async () => {
  mount();
  expect(screen.getByTestId('login-route')).toBeInTheDocument();
  expect(screen.queryByTestId('restore-splash')).not.toBeInTheDocument();
});

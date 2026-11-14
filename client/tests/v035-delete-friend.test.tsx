import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { RestClient } from '../src/api/rest';
import { DesktopProvider, type DesktopAdapter } from '../src/runtime/DesktopProvider';
import { ContactsView } from '../src/features/messaging/ContactsView';

afterEach(cleanup);
const adapter: DesktopAdapter = { kind: 'browser', invoke: async <T,>() => null as T, listen: async () => () => {} };
const friend = { id: 'u2', username: 'zhou', displayName: '周予', accountNumber: '100002', email: '', roles: ['user'], disabled: false, mustChangePassword: false };

function setup(reject = false) {
  let friends = [friend];
  const request = vi.fn(async (method: string, path: string): Promise<any> => {
    if (path === '/api/v1/contacts') return { friends, incoming: [], outgoing: [] };
    if (method === 'DELETE' && path === '/api/v1/contacts/u2') {
      if (reject) throw { error: '网络暂时不可用' };
      friends = [];
      return undefined;
    }
    return null;
  });
  render(<DesktopProvider adapter={adapter}><ContactsView client={new RestClient({ request })} onProfile={() => {}} onDirect={async () => {}}/></DesktopProvider>);
  return { request };
}

it('confirms before deleting a friend and clears the row afterwards', async () => {
  const h = setup();
  fireEvent.click(await screen.findByRole('button', { name: '删除' }));
  expect(screen.getByRole('group', { name: '确认删除好友' })).toHaveTextContent('删除后将同时删除你们的私聊聊天记录，确定？');
  fireEvent.click(screen.getByRole('button', { name: '取消' }));
  expect(h.request.mock.calls.some(([method, path]) => method === 'DELETE')).toBe(false);
  expect(screen.queryByRole('group', { name: '确认删除好友' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: '删除' }));
  fireEvent.click(screen.getByRole('button', { name: '确认删除' }));
  await waitFor(() => expect(h.request).toHaveBeenCalledWith('DELETE', '/api/v1/contacts/u2', undefined));
  await waitFor(() => expect(screen.queryByText('100002')).not.toBeInTheDocument());
});

it('keeps the friend when the server rejects the deletion', async () => {
  setup(true);
  fireEvent.click(await screen.findByRole('button', { name: '删除' }));
  fireEvent.click(screen.getByRole('button', { name: '确认删除' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('网络暂时不可用');
  expect(screen.getByText('100002')).toBeInTheDocument();
});

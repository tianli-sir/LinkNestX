import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { RestClient } from '../src/api/rest';
import { DocumentsView } from '../src/features/documents/DocumentsView';
import type { DocumentRecord } from '../src/features/documents/types';
import { DesktopProvider, type DesktopAdapter } from '../src/runtime/DesktopProvider';

afterEach(cleanup);

const attachment = {
  id: 'image-file', documentId: 'doc-1', name: 'diagram.png',
  versionId: 'image-version', currentVersionId: 'image-version',
  size: 123, createdAt: '2026-11-10T10:00:00Z', status: 'published',
};
const note: DocumentRecord = {
  id: 'doc-1', title: '好友经验', body: '好友专属内容\n\n![流程图片](linknestx-file:image-version)',
  visibility: 'friends', status: 'published', curated: false, revision: 1,
  author: { id: 'u1', username: 'lin', displayName: '林', accountNumber: '1001' },
  createdAt: '2026-11-10T10:00:00Z', updatedAt: '2026-11-10T10:00:00Z',
  canEdit: true, canDelete: true, canCurate: false, attachments: [attachment],
};

function setup(request: ReturnType<typeof vi.fn>, kind: DesktopAdapter['kind'] = 'browser') {
  const callbacks = new Map<string, (payload: any) => void>();
  const invoke = vi.fn(async () => null) as ReturnType<typeof vi.fn> & DesktopAdapter['invoke'];
  const adapter: DesktopAdapter = {
    kind,
    invoke,
    listen: async (name, callback) => {
      callbacks.set(name, callback);
      return () => { callbacks.delete(name); };
    },
  };
  render(
    <DesktopProvider adapter={adapter}>
      <DocumentsView client={{ request } as unknown as RestClient} />
    </DesktopProvider>,
  );
  return {
    invoke,
    emit: (name: string, payload: unknown) => act(() => { callbacks.get(name)?.(payload); }),
  };
}

it.each(['POST', 'PATCH'] as const)('locks document changes while a %s save is pending', async (method) => {
  let stored = { ...note };
  let completeSave: () => void = () => {};
  const request = vi.fn(async (verb: string, path: string, body?: any) => {
    if (verb === method) {
      return new Promise<DocumentRecord>(resolve => {
        completeSave = () => {
          stored = { ...stored, ...body, revision: 2 };
          resolve(stored);
        };
      });
    }
    return path.includes('?') ? { documents: [stored], hasMore: false, nextCursor: '' } : stored;
  });
  const { invoke, emit } = setup(request, 'native');
  if (method === 'POST') {
    fireEvent.click(screen.getByRole('button', { name: '写文档' }));
    fireEvent.change(await screen.findByLabelText('文档标题'), { target: { value: '稳定草稿' } });
  } else {
    fireEvent.click(await screen.findByRole('button', { name: '阅读 好友经验' }));
    fireEvent.click(await screen.findByRole('button', { name: '编辑文档' }));
  }
  const textarea = screen.getByLabelText('Markdown 正文');
  fireEvent.change(textarea, { target: { value: '此次提交的正文' } });
  fireEvent.click(screen.getByRole('button', { name: method === 'POST' ? '保存草稿' : '保存修改' }));

  expect(textarea).toBeDisabled();
  expect(screen.getByLabelText('文档标题')).toBeDisabled();
  expect(screen.getByLabelText('可见范围')).toBeDisabled();
  expect(screen.getByRole('button', { name: '添加附件' })).toBeDisabled();
  if (method === 'PATCH') {
    const insert = screen.getByRole('button', { name: '插入正文' });
    expect(insert).toBeDisabled();
    fireEvent.click(insert);
  }
  expect(textarea).toHaveValue('此次提交的正文');
  emit('attachments.prepared', [{ id: 'pending-save-handle', name: 'later.pdf' }]);
  await waitFor(() => expect(invoke).toHaveBeenCalledWith('discard_attachment', { sourceHandle: 'pending-save-handle' }));
  expect(invoke).not.toHaveBeenCalledWith('start_document_attachment_upload', expect.anything());
  expect(request.mock.calls.filter(([verb]) => verb === method)).toHaveLength(1);
  expect(request).toHaveBeenCalledWith(method, method === 'POST' ? '/api/v1/documents' : '/api/v1/documents/doc-1', expect.objectContaining({ body: '此次提交的正文' }));

  await act(async () => { completeSave(); });
  fireEvent.click(await screen.findByRole('button', { name: '编辑文档' }));
  expect(screen.getByLabelText('Markdown 正文')).toBeEnabled();
  expect(screen.getByLabelText('Markdown 正文')).toHaveValue('此次提交的正文');
});

it('refreshes the feed when a friendship is accepted or removed', async () => {
  let readable = false;
  const request = vi.fn(async () => ({ documents: readable ? [note] : [], hasMore: false, nextCursor: '' }));
  const { emit } = setup(request);
  await screen.findByRole('heading', { name: '从第一篇文档开始' });

  readable = true;
  emit('server.event', { type: 'contacts.changed' });
  expect(await screen.findByRole('button', { name: '阅读 好友经验' })).toBeVisible();
  readable = false;
  emit('server.event', { type: 'contacts.changed' });
  await screen.findByRole('heading', { name: '从第一篇文档开始' });
  expect(screen.queryByText('好友专属内容')).toBeNull();
  expect(screen.queryByRole('button', { name: '阅读 好友经验' })).toBeNull();
});

it('removes an open document, its image, and download actions after friendship access is revoked', async () => {
  let revoked = false;
  const request = vi.fn(async (_method: string, path: string) => {
    if (path.includes('?')) return { documents: revoked ? [] : [note], hasMore: false, nextCursor: '' };
    if (revoked) throw new Error('文档不可访问');
    return note;
  });
  const { emit } = setup(request);
  fireEvent.click(await screen.findByRole('button', { name: '阅读 好友经验' }));
  expect(await screen.findByRole('img', { name: '流程图片' })).toBeVisible();
  expect(screen.getByRole('button', { name: '下载' })).toBeVisible();

  revoked = true;
  emit('server.event', { type: 'contacts.changed' });
  await screen.findByText('文档不可访问');
  expect(screen.queryByText('好友专属内容')).toBeNull();
  expect(screen.queryByRole('img')).toBeNull();
  expect(screen.queryByRole('button', { name: '下载' })).toBeNull();
  expect(screen.queryByRole('button', { name: '编辑文档' })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: '返回文档' }));
  await screen.findByRole('heading', { name: '从第一篇文档开始' });
});

it('does not retain old feed summaries when permission refresh fails', async () => {
  let failed = false;
  const request = vi.fn(async () => {
    if (failed) throw new Error('读取权限失败');
    return { documents: [note], hasMore: false, nextCursor: '' };
  });
  const { emit } = setup(request);
  await screen.findByRole('button', { name: '阅读 好友经验' });
  failed = true;
  emit('server.event', { type: 'contacts.changed' });
  await screen.findByText('读取权限失败');
  expect(screen.queryByRole('button', { name: '阅读 好友经验' })).toBeNull();
  expect(screen.queryByText('好友专属内容')).toBeNull();
});

it('preserves the local editor text when a contact change removes document access', async () => {
  let revoked = false;
  const request = vi.fn(async (_method: string, path: string) => {
    if (path.includes('?')) return { documents: revoked ? [] : [note], hasMore: false, nextCursor: '' };
    if (revoked) throw new Error('文档不可访问');
    return note;
  });
  const { emit } = setup(request);
  fireEvent.click(await screen.findByRole('button', { name: '阅读 好友经验' }));
  fireEvent.click(await screen.findByRole('button', { name: '编辑文档' }));
  fireEvent.change(screen.getByLabelText('Markdown 正文'), { target: { value: '仍需复制保存的本地修改' } });

  revoked = true;
  emit('server.event', { type: 'contacts.changed' });
  await screen.findByText('文档不可访问');
  expect(screen.getByLabelText('Markdown 正文')).toHaveValue('仍需复制保存的本地修改');
  expect(screen.queryByRole('button', { name: '下载' })).toBeNull();
  expect(screen.queryByRole('img')).toBeNull();
});

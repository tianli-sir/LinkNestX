import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SafeMarkdown, safeExternalUrl } from '../src/features/documents/SafeMarkdown';
import type { DocumentRecord } from '../src/features/documents/types';
import { DesktopProvider, type DesktopAdapter } from '../src/runtime/DesktopProvider';

afterEach(cleanup);

describe('document external links', () => {
  it.each([
    'https://user:password@example.com/guide',
    'https://user@example.com/guide',
    'https://example.com/\nargument',
    'https://example.com/\targument',
    'https://example.com/\u0085argument',
    'https://example.com/\\argument',
    'https://example.com/%0d%0aargument',
    'https://example.com/%5cargument',
    'mailto:team@example.com?attachment=/private/report.pdf',
    'mailto:team@example.com?attach=C:/report.pdf',
    'mailto:team@example.com?%61ttachment=secret',
    'mailto:team@example.com?subject=Hello&bcc=other@example.com',
    'mailto:team@example.com?subject=Hello%0d%0aAttachment:secret',
    'mailto:team@example.com?body=Hello%00attachment',
    'mailto:team@example.com?SUBJECT=Hello',
    'mailto:team@example.com#attachment',
    'mailto://host/team@example.com',
    'mailto:?subject=Hello',
    'mailto:Team%20%3Cteam@example.com%3E',
    'mailto:team@example.com%3Fattachment=secret',
    'mailto:team@@example.com',
    'mailto:team@example.com,',
    `mailto:${Array.from({ length: 21 }, (_, index) => `user${index}@example.com`).join(',')}`,
    'javascript:alert(1)',
    'file:///private/report.pdf',
    'ms-settings:display',
    '//example.com/guide',
    `https://example.com/${'x'.repeat(4096)}`,
  ])('rejects unsafe external URL %j', (url) => {
    expect(safeExternalUrl(url)).toBe('');
  });

  it.each([
    ['https://example.com', 'https://example.com/'],
    ['http://192.168.1.10/guide?q=team', 'http://192.168.1.10/guide?q=team'],
    ['https://example.com/guide#section-2', 'https://example.com/guide#section-2'],
    ['mailto:team@example.com', 'mailto:team@example.com'],
    ['mailto:team+notes@example.com,other@example.org?subject=Hello%20team&body=Please%20read.', 'mailto:team+notes@example.com,other@example.org?subject=Hello%20team&body=Please%20read.'],
    ['mailto:team@example.com?subject=%E5%8D%8F%E4%BD%9C&body=%E4%BD%A0%E5%A5%BD', 'mailto:team@example.com?subject=%E5%8D%8F%E4%BD%9C&body=%E4%BD%A0%E5%A5%BD'],
  ])('preserves supported external URL %j', (url, expected) => {
    expect(safeExternalUrl(url)).toBe(expected);
  });

  it('renders rejected browser mail and credential URLs as plain text', () => {
    const adapter: DesktopAdapter = {
      kind: 'browser',
      invoke: vi.fn(async () => undefined) as DesktopAdapter['invoke'],
      listen: async () => () => {},
    };
    const document = { attachments: [] } as unknown as DocumentRecord;
    render(
      <DesktopProvider adapter={adapter}>
        <SafeMarkdown
          document={document}
          body={'[邮件](mailto:team@example.com?attachment=secret)\n\n[网页](https://user:password@example.com)\n\n[联系团队](mailto:team@example.com?subject=Hello)'}
        />
      </DesktopProvider>,
    );

    expect(screen.getByText('邮件').closest('a')).toBeNull();
    expect(screen.getByText('网页').closest('a')).toBeNull();
    expect(screen.getByRole('link', { name: '联系团队' })).toHaveAttribute(
      'href',
      'mailto:team@example.com?subject=Hello',
    );
    expect(adapter.invoke).not.toHaveBeenCalledWith('open_external_url', expect.anything());
  });
});

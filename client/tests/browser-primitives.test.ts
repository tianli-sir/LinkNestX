import { copyAccountNumber, messageId } from '../src/lib/browserPrimitives';

test('message identifiers work without secure-context randomUUID', () => {
  const getRandomValues = crypto.getRandomValues.bind(crypto);
  vi.stubGlobal('crypto', { getRandomValues });
  try {
    const first = messageId(), second = messageId();
    expect(first).toMatch(/^[\da-f]{8}-[\da-f]{4}-4[\da-f]{3}-[89ab][\da-f]{3}-[\da-f]{12}$/);
    expect(second).not.toBe(first);
  } finally { vi.unstubAllGlobals(); }
});

test('LAN account copying has a user gesture fallback and restores focus', async () => {
  const previous = document.createElement('button');
  document.body.append(previous); previous.focus();
  const clipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
  const command = Object.getOwnPropertyDescriptor(document, 'execCommand');
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  const copy = vi.fn(() => {
    expect((document.activeElement as HTMLTextAreaElement).value).toBe('10000001');
    return true;
  });
  Object.defineProperty(document, 'execCommand', { configurable: true, value: copy });
  try {
    await copyAccountNumber('10000001');
    expect(copy).toHaveBeenCalledWith('copy');
    expect(document.activeElement).toBe(previous);
    expect(document.querySelector('textarea')).toBeNull();
    copy.mockImplementation(() => false);
    await expect(copyAccountNumber('10000001')).rejects.toThrow('选中账号后复制');
    expect(document.querySelector('textarea')).toBeNull();
  } finally {
    if (clipboard) Object.defineProperty(navigator, 'clipboard', clipboard); else Reflect.deleteProperty(navigator, 'clipboard');
    if (command) Object.defineProperty(document, 'execCommand', command); else Reflect.deleteProperty(document, 'execCommand');
    previous.remove();
  }
});

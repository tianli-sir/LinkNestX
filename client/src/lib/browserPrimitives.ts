/** getRandomValues is available on the explicitly supported LAN HTTP origin. */
export function messageId(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const value = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
  return `${value.slice(0, 8)}-${value.slice(8, 12)}-${value.slice(12, 16)}-${value.slice(16, 20)}-${value.slice(20)}`;
}

export async function copyAccountNumber(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  // The synchronous user gesture fallback works on LAN HTTP browsers. The
  // number remains visible/selectable if the browser disallows both methods.
  const previous = document.activeElement as HTMLElement | null;
  const input = document.createElement('textarea');
  input.value = value;
  input.readOnly = true;
  input.setAttribute('aria-label', '复制数字账号');
  input.style.cssText = 'position:fixed;left:-9999px;top:0;';
  document.body.append(input);
  try {
    input.focus();
    input.select();
    if (!document.execCommand?.('copy')) throw new Error('无法自动复制，请选中账号后复制');
  } finally {
    input.remove();
    previous?.focus();
  }
}

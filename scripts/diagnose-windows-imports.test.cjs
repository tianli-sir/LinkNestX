const { test } = require('node:test');
const assert = require('node:assert/strict');
const { inspect } = require('./diagnose-windows-imports.cjs');
function image({ wide = false, delayed = false, ordinal = false, absoluteDelay = false } = {}) {
  const b = Buffer.alloc(4096);
  b.writeUInt16LE(0x5a4d); b.writeUInt32LE(0x80, 0x3c); b.writeUInt32LE(0x4550, 0x80);
  b.writeUInt16LE(wide ? 0x8664 : 0x14c, 0x84); b.writeUInt16LE(1, 0x86);
  const optionalSize = wide ? 240 : 224; b.writeUInt16LE(optionalSize, 0x94);
  const o = 0x98, s = o + optionalSize, dirs = o + (wide ? 112 : 96);
  b.writeUInt16LE(wide ? 0x20b : 0x10b, o); b.writeUInt32LE(512, o + 60);
  if (wide) b.writeBigUInt64LE(0x140000000n, o + 24); else b.writeUInt32LE(0x400000, o + 28);
  b.writeUInt32LE(16, o + (wide ? 108 : 92));
  b.writeUInt32LE(2048, s + 8); b.writeUInt32LE(0x1000, s + 12); b.writeUInt32LE(2048, s + 16); b.writeUInt32LE(512, s + 20);
  const pointer = address => address + (absoluteDelay ? 0x400000 : 0);
  const index = delayed ? 13 : 1;
  b.writeUInt32LE(0x1000, dirs + index * 8); b.writeUInt32LE(delayed ? 64 : 40, dirs + index * 8 + 4);
  if (delayed) { b.writeUInt32LE(absoluteDelay ? 0 : 1, 512); b.writeUInt32LE(pointer(0x1080), 516); b.writeUInt32LE(pointer(0x10a0), 528); }
  else { b.writeUInt32LE(0x10a0, 512); b.writeUInt32LE(0x1080, 524); b.writeUInt32LE(0x10a0, 528); }
  b.write('KERNEL32.dll\0', 640, 'ascii');
  const thunk = ordinal ? (wide ? (1n << 63n) | 42n : (1n << 31n) | 42n) : BigInt(pointer(0x10c0));
  if (wide) b.writeBigUInt64LE(thunk, 672); else b.writeUInt32LE(Number(thunk), 672);
  b.write('GetLastError\0', 706, 'ascii');
  // Exported name + a forwarder allow checking adjacent DLLs without loading them.
  b.writeUInt32LE(0x1200, dirs); b.writeUInt32LE(256, dirs + 4);
  const e = 1024; b.writeUInt32LE(7, e + 16); b.writeUInt32LE(1, e + 20); b.writeUInt32LE(1, e + 24);
  b.writeUInt32LE(0x1240, e + 28); b.writeUInt32LE(0x1250, e + 32); b.writeUInt32LE(0x1260, e + 36);
  b.writeUInt32LE(0x1280, 1088); b.writeUInt32LE(0x1270, 1104); b.writeUInt16LE(0, 1120);
  b.write('Forwarded\0', 1136, 'ascii'); b.write('KERNEL32.GetLastError\0', 1152, 'ascii');
  return b;
}
test('reads x86/x64 ordinary and delayed imports, named and ordinal', () => {
  for (const wide of [false, true]) for (const delayed of [false, true]) for (const ordinal of [false, true]) {
    const parsed = inspect(image({ wide, delayed, ordinal }));
    assert.equal(parsed.architecture, wide ? 'x64' : 'x86');
    assert.deepEqual(parsed.imports, [{ dll: 'kernel32.dll', ...(ordinal ? { ordinal: 42 } : { symbol: 'GetLastError' }), delayed }]);
    assert.deepEqual(parsed.exports, [{ ordinal: 7, names: ['Forwarded'], forwarder: 'KERNEL32.GetLastError' }]);
  }
  assert.equal(inspect(image({ delayed: true, absoluteDelay: true })).imports[0].symbol, 'GetLastError');
});
test('malformed offsets, unsafe library paths and truncated images fail closed', () => {
  assert.throws(() => inspect(Buffer.alloc(16)));
  assert.throws(() => inspect(image().subarray(0, 700)));
  const bad = image(); bad.write('../evil.dll\0', 640, 'ascii'); assert.throws(() => inspect(bad), /Unsafe/);
  const outside = image(); outside.writeUInt32LE(0xffffff00, 524); assert.throws(() => inspect(outside), /RVA/);
});

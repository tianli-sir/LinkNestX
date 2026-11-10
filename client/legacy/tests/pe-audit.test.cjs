const { test } = require('node:test');
const assert = require('node:assert/strict');
const { inspect, audit } = require('../tools/audit-pe.cjs');
function image(symbol = 'GetLastError', dll = 'KERNEL32.dll') {
  const b = Buffer.alloc(2048);
  b.writeUInt16LE(0x5a4d); b.writeUInt32LE(0x80, 0x3c); b.writeUInt32LE(0x4550, 0x80);
  b.writeUInt16LE(0x14c, 0x84); b.writeUInt16LE(1, 0x86); b.writeUInt16LE(224, 0x94);
  const o = 0x98, s = o + 224;
  b.writeUInt16LE(0x10b, o); b.writeUInt16LE(6, o + 48); b.writeUInt16LE(1, o + 50); b.writeUInt16LE(3, o + 68);
  b.writeUInt32LE(16, o + 92); b.writeUInt32LE(0x1000, o + 104); b.writeUInt32LE(40, o + 108);
  b.writeUInt32LE(1024, s + 8); b.writeUInt32LE(0x1000, s + 12); b.writeUInt32LE(1024, s + 16); b.writeUInt32LE(512, s + 20);
  b.writeUInt32LE(0x1060, 512); b.writeUInt32LE(0x1040, 524); b.writeUInt32LE(0x1060, 528);
  b.write(dll + '\0', 576, 'ascii'); b.writeUInt32LE(0x1080, 608); b.write(symbol + '\0', 642, 'ascii');
  return b;
}
test('PE audit resolves imported names and refuses known newer APIs and DLLs', () => {
  const compatible = inspect(image());
  assert.deepEqual(audit(compatible, 'x86'), []);
  assert.equal(compatible.imports[0].symbol, 'GetLastError');
  assert(audit(inspect(image('WaitOnAddress')), 'x86').some(x => x.includes('WaitOnAddress')));
  assert(audit(inspect(image('ProcessPrng', 'bcryptprimitives.dll')), 'x86').some(x => x.includes('bcryptprimitives')));
  assert(audit(compatible, 'x64').some(x => x.includes('Architecture')));
});
test('PE audit fails closed on malformed and unauditable inputs', () => {
  assert.throws(() => inspect(Buffer.alloc(32)));
  const ordinal = image(); ordinal.writeUInt32LE(0x80000001, 608); assert.throws(() => inspect(ordinal), /Ordinal/);
  const outside = image(); outside.writeUInt32LE(0xffffff00, 524); assert.throws(() => inspect(outside), /RVA/);
  const truncated = image().subarray(0, 640); assert.throws(() => inspect(truncated));
});

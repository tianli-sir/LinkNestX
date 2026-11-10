'use strict';
// This catches known loader incompatibilities. It supplements a Win7 SP1 runtime
// test; it does not prove that every API used dynamically is present on Win7.
const fs = require('node:fs');
const FORBIDDEN = new Set([
  'GetSystemTimePreciseAsFileTime', 'WaitOnAddress', 'WakeByAddressAll', 'WakeByAddressSingle',
  'GetSystemCpuSetInformation', 'GetCurrentThreadStackLimits', 'SetThreadDescription', 'GetThreadDescription',
  'SetProcessInformation', 'GetProcessInformation', 'GetTempPath2W', 'GetTempPath2A',
  'SetThreadSelectedCpuSets', 'GetThreadSelectedCpuSets', 'VirtualAlloc2', 'VirtualAlloc2FromApp',
  'CreateFile2', 'CopyFile2', 'DiscardVirtualMemory', 'PrefetchVirtualMemory', 'OfferVirtualMemory',
  'ReclaimVirtualMemory', 'SetProcessDefaultCpuSets', 'RoGetActivationFactory', 'RoInitialize',
  'CoIncrementMTAUsage', 'CoDecrementMTAUsage', 'GetDpiForWindow', 'SetThreadDpiAwarenessContext',
  'BCryptGenRandom@bcryptprimitives', 'ProcessPrng',
]);
const BASELINE_DLLS = new Set(['advapi32.dll','bcrypt.dll','cabinet.dll','comctl32.dll','comdlg32.dll','crypt32.dll','dbghelp.dll','dnsapi.dll','dwmapi.dll','gdi32.dll','imm32.dll','iphlpapi.dll','kernel32.dll','kernelbase.dll','msimg32.dll','msvcrt.dll','ncrypt.dll','netapi32.dll','normaliz.dll','ntdll.dll','ole32.dll','oleaut32.dll','powrprof.dll','propsys.dll','psapi.dll','rpcrt4.dll','secur32.dll','setupapi.dll','shell32.dll','shlwapi.dll','user32.dll','userenv.dll','usp10.dll','version.dll','winhttp.dll','wininet.dll','winmm.dll','winspool.drv','wintrust.dll','winusb.dll','ws2_32.dll','wtsapi32.dll']);
function inspect(buffer) {
  function range(offset, size) { if (!Number.isSafeInteger(offset) || offset < 0 || offset + size > buffer.length) throw new Error('Truncated PE image'); }
  function u16(offset) { range(offset, 2); return buffer.readUInt16LE(offset); }
  function u32(offset) { range(offset, 4); return buffer.readUInt32LE(offset); }
  if (u16(0) !== 0x5a4d) throw new Error('Not a Windows executable');
  const pe = u32(0x3c); if (u32(pe) !== 0x4550) throw new Error('Invalid PE header');
  const machine = u16(pe + 4), count = u16(pe + 6), optionalSize = u16(pe + 20), optional = pe + 24;
  const magic = u16(optional), wide = magic === 0x20b;
  if (!wide && magic !== 0x10b) throw new Error('Unknown PE optional header');
  if (count > 96 || optionalSize < (wide ? 112 : 96)) throw new Error('Invalid PE header size');
  range(optional, optionalSize + count * 40);
  const sections = Array.from({ length: count }, (_, index) => {
    const offset = optional + optionalSize + index * 40;
    return { va: u32(offset + 12), length: u32(offset + 8), raw: u32(offset + 20), rawSize: u32(offset + 16) };
  });
  function rva(address, size = 1) {
    const section = sections.find(s => address >= s.va && address - s.va + size <= s.rawSize);
    if (!section) throw new Error('PE RVA is outside file-backed sections');
    const offset = section.raw + address - section.va; range(offset, size); return offset;
  }
  function string(address) {
    const offset = rva(address), end = buffer.indexOf(0, offset);
    if (end < offset || end - offset > 4096) throw new Error('Invalid PE import name');
    rva(address, end - offset + 1);
    const value = buffer.toString('ascii', offset, end);
    if (!/^[\x20-\x7e]+$/.test(value)) throw new Error('Invalid PE import encoding');
    return value;
  }
  const imports = [];
  const directories = optional + (wide ? 112 : 96);
  const directoryCount = Math.min(u32(optional + (wide ? 108 : 92)), Math.floor((optionalSize - (wide ? 112 : 96)) / 8));
  function readImports(directoryIndex, delayed) {
    if (directoryIndex >= directoryCount) return;
    const base = u32(directories + directoryIndex * 8), size = u32(directories + directoryIndex * 8 + 4);
    if (!base && !size) return;
    const width = delayed ? 32 : 20;
    let terminated = false;
    for (let position = 0; position + width <= size; position += width) {
      const d = rva(base + position, width);
      if (buffer.subarray(d, d + width).every(byte => byte === 0)) { terminated = true; break; }
      if (delayed && u32(d) !== 1) throw new Error('Unsupported delayed import addressing');
      const dll = string(u32(d + (delayed ? 4 : 12))).toLowerCase();
      const thunk = delayed ? u32(d + 16) : (u32(d) || u32(d + 16));
      if (!thunk) throw new Error('Missing PE import lookup');
      let thunkTerminated = false;
      for (let i = 0; i < 65536; i++) {
        const p = rva(thunk + i * (wide ? 8 : 4), wide ? 8 : 4);
        const value = wide ? buffer.readBigUInt64LE(p) : BigInt(u32(p));
        if (!value) { thunkTerminated = true; break; }
        const ordinal = value & (wide ? 1n << 63n : 1n << 31n);
        if (ordinal) throw new Error(`Ordinal import from ${dll} cannot be audited`);
        if (value > 0xffffffffn) throw new Error('Invalid PE import address');
        imports.push({ dll, symbol: string(Number(value) + 2), delayed });
      }
      if (!thunkTerminated) throw new Error('Unterminated PE imports');
    }
    if (!terminated) throw new Error('Unterminated PE import descriptors');
  }
  readImports(1, false); readImports(13, true);
  return { architecture: machine === 0x8664 ? 'x64' : machine === 0x14c ? 'x86' : `unknown-${machine}`, subsystem: u16(optional + 68), minimumSubsystem: `${u16(optional + 48)}.${u16(optional + 50)}`, imports };
}
function audit(info, expectedArchitecture) {
  const failures = [];
  if (info.architecture !== expectedArchitecture) failures.push('Architecture mismatch');
  const [major, minor] = info.minimumSubsystem.split('.').map(Number);
  if (major > 6 || (major === 6 && minor > 1)) failures.push('PE subsystem requires a newer Windows version');
  if (!info.imports.length) failures.push('No auditable imports');
  for (const item of info.imports) {
    if (!BASELINE_DLLS.has(item.dll)) failures.push(`DLL not in Win7 baseline: ${item.dll}`);
    if (FORBIDDEN.has(item.symbol)) failures.push(`API newer than Win7: ${item.symbol}`);
  }
  return [...new Set(failures)];
}
if (require.main === module) {
  try {
    const [file, arch, report] = process.argv.slice(2);
    if (!file || !['x86','x64'].includes(arch) || !report) throw new Error('Usage: audit-pe.cjs executable x86|x64 report.json');
    const info = inspect(fs.readFileSync(file)); const failures = audit(info, arch);
    fs.writeFileSync(report, JSON.stringify({ ...info, failures, runtimeValidation: 'Win7 SP1 hardware test still required' }, null, 2) + '\n');
    if (failures.length) throw new Error(failures.join('\n'));
    console.log(`PE import checks passed (${arch}, ${info.imports.length} imports). Runtime validation is separate.`);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
module.exports = { inspect, audit };

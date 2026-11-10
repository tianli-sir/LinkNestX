'use strict';
// Static PE inspection only: no target executable or adjacent DLL is loaded.
const fs = require('node:fs');
const path = require('node:path');
function inspect(buffer) {
  function range(offset, size) { if (!Number.isSafeInteger(offset) || !Number.isSafeInteger(size) || offset < 0 || size < 0 || offset + size > buffer.length) throw new Error('Truncated PE image'); }
  function u16(offset) { range(offset, 2); return buffer.readUInt16LE(offset); }
  function u32(offset) { range(offset, 4); return buffer.readUInt32LE(offset); }
  function u64(offset) { range(offset, 8); return buffer.readBigUInt64LE(offset); }
  if (u16(0) !== 0x5a4d) throw new Error('Invalid DOS signature');
  const pe = u32(0x3c); if (u32(pe) !== 0x4550) throw new Error('Invalid PE signature');
  const machine = u16(pe + 4), count = u16(pe + 6), optionalSize = u16(pe + 20), optional = pe + 24;
  const magic = u16(optional), wide = magic === 0x20b;
  if (!wide && magic !== 0x10b) throw new Error('Unknown PE format');
  if (count > 96 || optionalSize < (wide ? 112 : 96)) throw new Error('Invalid PE header size');
  range(optional, optionalSize + count * 40);
  const headerSize = u32(optional + 60), imageBase = wide ? u64(optional + 24) : BigInt(u32(optional + 28));
  const sections = Array.from({ length: count }, (_, index) => {
    const p = optional + optionalSize + index * 40;
    return { va: u32(p + 12), raw: u32(p + 20), rawSize: u32(p + 16) };
  });
  function rva(address, size = 1) {
    if (address >= 0 && address + size <= headerSize) { range(address, size); return address; }
    const s = sections.find(s => address >= s.va && address - s.va + size <= s.rawSize);
    if (!s) throw new Error('PE RVA is outside file-backed sections');
    const result = s.raw + address - s.va; range(result, size); return result;
  }
  function string(address) {
    const offset = rva(address), end = buffer.indexOf(0, offset);
    if (end < offset || end - offset > 4096) throw new Error('Invalid PE string');
    rva(address, end - offset + 1);
    const value = buffer.toString('ascii', offset, end);
    if (!/^[\x20-\x7e]+$/.test(value)) throw new Error('Invalid PE string encoding');
    return value;
  }
  const directoryStart = optional + (wide ? 112 : 96);
  const directoryCount = Math.min(u32(optional + (wide ? 108 : 92)), Math.floor((optionalSize - (wide ? 112 : 96)) / 8));
  function directory(index) { return index < directoryCount ? [u32(directoryStart + index * 8), u32(directoryStart + index * 8 + 4)] : [0, 0]; }
  const imports = [];
  function readImports(index, delayed) {
    const [base, size] = directory(index); if (!base && !size) return;
    const width = delayed ? 32 : 20; let terminated = false;
    for (let offset = 0; offset + width <= size; offset += width) {
      const d = rva(base + offset, width);
      if (buffer.subarray(d, d + width).every(byte => byte === 0)) { terminated = true; break; }
      const addressesAreRva = !delayed || (u32(d) & 1) !== 0;
      function address(raw) {
        const value = BigInt(raw) - (addressesAreRva ? 0n : imageBase);
        if (value < 0n || value > 0xffffffffn) throw new Error('Invalid delayed import address');
        return Number(value);
      }
      const dll = string(address(u32(d + (delayed ? 4 : 12)))).toLowerCase();
      if (!/^[a-z0-9_.-]+\.(dll|drv)$/.test(dll)) throw new Error('Unsafe import DLL name');
      const thunk = address(delayed ? u32(d + 16) : (u32(d) || u32(d + 16)));
      let thunkTerminated = false;
      for (let n = 0; n < 65536; n++) {
        const p = rva(thunk + n * (wide ? 8 : 4), wide ? 8 : 4), value = wide ? u64(p) : BigInt(u32(p));
        if (!value) { thunkTerminated = true; break; }
        if (value & (wide ? 1n << 63n : 1n << 31n)) imports.push({ dll, ordinal: Number(value & 0xffffn), delayed });
        else {
          if (value > 0xffffffffn) throw new Error('Invalid import address');
          imports.push({ dll, symbol: string(address(Number(value)) + 2), delayed });
        }
      }
      if (!thunkTerminated) throw new Error('Unterminated import lookup');
    }
    if (!terminated) throw new Error('Unterminated import directory');
  }
  readImports(1, false); readImports(13, true);
  const exports = [], [exportRva, exportSize] = directory(0);
  if (exportRva) {
    const e = rva(exportRva, 40), ordinalBase = u32(e + 16), functionCount = u32(e + 20), nameCount = u32(e + 24);
    if (functionCount > 1000000 || nameCount > 1000000) throw new Error('Invalid export counts');
    const functions = u32(e + 28), names = u32(e + 32), ordinals = u32(e + 36), byIndex = new Map();
    for (let i = 0; i < nameCount; i++) {
      const index = u16(rva(ordinals + i * 2, 2));
      if (index >= functionCount) throw new Error('Invalid export ordinal');
      const list = byIndex.get(index) || []; list.push(string(u32(rva(names + i * 4, 4)))); byIndex.set(index, list);
    }
    for (let i = 0; i < functionCount; i++) {
      const address = u32(rva(functions + i * 4, 4)); if (!address) continue;
      const item = { ordinal: ordinalBase + i, names: byIndex.get(i) || [] };
      if (address >= exportRva && address < exportRva + exportSize) item.forwarder = string(address);
      exports.push(item);
    }
  }
  return { architecture: machine === 0x8664 ? 'x64' : machine === 0x14c ? 'x86' : `machine-${machine}`, imports, exports };
}
function scan(directory) {
  const entries = fs.readdirSync(directory, { withFileTypes: true });
  const executables = entries.filter(entry => entry.isFile() && /\.exe$/i.test(entry.name)).sort((a,b) => a.name.localeCompare(b.name));
  if (executables.length > 100) throw new Error('Too many test executables to inspect');
  const names = new Map(entries.filter(entry => entry.isFile()).map(entry => [entry.name.toLowerCase(), entry.name]));
  const images = [], adjacent = new Map(), queue = executables.map(entry => entry.name);
  const inspected = new Set();
  while (queue.length) {
    const file = queue.shift(); if (inspected.has(file)) continue; inspected.add(file);
    if (inspected.size > 256) throw new Error('Too many adjacent dependencies');
    const filename = path.join(directory, file);
    if (fs.statSync(filename).size > 512 * 1024 * 1024) throw new Error('PE image exceeds diagnostic limit');
    try {
      const image = inspect(fs.readFileSync(filename));
      const { exports, ...publicImage } = image; images.push({ file, ...publicImage });
      if (/\.dll$/i.test(file)) adjacent.set(file.toLowerCase(), { file, architecture: image.architecture, exports });
      for (const item of image.imports) {
        const local = names.get(item.dll); if (local && !inspected.has(local)) queue.push(local);
      }
    } catch (error) { images.push({ file, diagnosticError: error.message }); }
  }
  return { images, adjacent: [...adjacent.values()] };
}
if (require.main === module) {
  try {
    const [directory, output] = process.argv.slice(2);
    if (!directory || !output) throw new Error('Usage: diagnose-windows-imports.cjs directory output.json');
    fs.writeFileSync(output, JSON.stringify(scan(directory), null, 2) + '\n');
  } catch (error) { console.error(error.code ? `Diagnostic filesystem error: ${error.code}` : error.message); process.exitCode = 1; }
}
module.exports = { inspect, scan };

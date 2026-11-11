'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { NativeHost } = require('../shell/native.cjs');
const executable = process.env.LINKNESTX_LEGACY_TEST_BINARY;

test('real native process preserves framing, concurrent requests and opaque destinations', { skip: !executable, timeout: 15000 }, async () => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'linknestx-legacy-test-'));
  const host = new NativeHost(executable, directory);
  try {
    const values = await Promise.all(Array.from({ length: 12 }, () => host.request('client_configuration')));
    assert(values.every(value => value.platform === 'windows-legacy' && value.capabilities.nativeTransfers && !value.capabilities.updates));
    assert(values.every(value => value.serverOrigin && !('accessToken' in value)));
    assert.deepEqual(await host.request('session_identity'),{userId:null,scope:null});
    await assert.rejects(host.request('login', { username: 'test', password: 'test', serverUrl: 'https://invalid.example' }), /格式/);
    await assert.rejects(host.request('start_download', { versionId: 'version', destination: path.join(directory, 'never.txt') }));
    assert.equal(await fs.stat(path.join(directory, 'never.txt')).then(() => true, () => false), false);
    await assert.rejects(host.request('api_request', { method: 'POST', path: '/api/v1/auth/refresh' }));
    const reservation = await host.request('reserve_audit_export');
    await assert.rejects(host.request('export_audit', { exportId: reservation, format: 'csv', action: '', scope: 'expired', destination: null }));
    const next = await host.request('reserve_audit_export');
    await host.request('cancel_audit_export', { exportId: next });
  } finally {
    await host.stop();
    await fs.rm(directory, { recursive: true, force: true });
  }
  assert.equal(host.closed, true);
  assert.equal(host.pending.size, 0);
});

test('real native process rejects oversized framed input before allocating it', { skip: !executable, timeout: 15000 }, async () => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'linknestx-legacy-invalid-'));
  try {
    const child = spawn(executable, ['--stdio', directory], { stdio: ['pipe', 'pipe', 'pipe'] });
    child.stdout.resume(); child.stderr.resume();
    const exited = once(child, 'exit');
    const header = Buffer.alloc(4); header.writeUInt32LE(0x7fffffff);
    child.stdin.end(header);
    const [code] = await exited;
    assert.equal(code, 1);
  } finally { await fs.rm(directory, { recursive: true, force: true }); }
});

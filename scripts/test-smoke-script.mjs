// Exercise POSIX smoke forwarding and cleanup with no Go, Cargo, or sockets.
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const script = fileURLToPath(new URL('./smoke-test.sh', import.meta.url));
const stubSource = `
const fs = require('node:fs');
const path = require('node:path');
const kind = path.basename(process.argv[1], '.js');
const args = process.argv.slice(2);
fs.appendFileSync(process.env.SMOKE_SCRIPT_TEST_LOG, JSON.stringify({
  kind, args, server: process.env.LINKNESTX_SMOKE_SERVER,
  marker: process.env.LINKNESTX_SMOKE_PID_MARKER, temp: process.env.TMPDIR,
}) + '\\n');
if (kind === 'go-stub') {
  fs.writeFileSync(args[args.indexOf('-o') + 1], 'owned build artifact');
  process.exit(Number(process.env.SMOKE_SCRIPT_GO_EXIT || 0));
}
if (args.length === 1 && args[0] === '--version') process.exit(0);
if (process.env.SMOKE_SCRIPT_FOREIGN_PID) {
  const pid = process.env.SMOKE_SCRIPT_FOREIGN_PID;
  fs.writeFileSync(process.env.LINKNESTX_SMOKE_PID_MARKER, pid + ' ' + pid + '\\n');
}
process.exit(Number(process.env.SMOKE_SCRIPT_CARGO_EXIT || 0));
`;

function runCase(shell, { options = [], goExit = 0, cargoExit = 0, foreignPid, failExpansion = false } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'smoke script regression '));
  try {
    for (const name of ['go-stub', 'cargo-stub']) {
      fs.writeFileSync(path.join(root, name + '.js'), stubSource);
      fs.writeFileSync(path.join(root, name), '#!/bin/sh\nexec "$SMOKE_SCRIPT_NODE" "$0.js" "$@"\n', { mode: 0o700 });
    }
    const log = path.join(root, 'calls.jsonl');
    const env = {
      ...process.env, GO: path.join(root, 'go-stub'), CARGO: path.join(root, 'cargo-stub'),
      TMPDIR: root, GOCACHE: path.join(root, 'go cache'), SMOKE_SCRIPT_NODE: process.execPath,
      SMOKE_SCRIPT_TEST_LOG: log, SMOKE_SCRIPT_GO_EXIT: String(goExit),
      SMOKE_SCRIPT_CARGO_EXIT: String(cargoExit),
    };
    delete env.SMOKE_SCRIPT_FOREIGN_PID;
    if (foreignPid !== undefined) env.SMOKE_SCRIPT_FOREIGN_PID = String(foreignPid);
    let testScript = script;
    if (failExpansion) {
      // Inject at the asynchronous expansion boundary, where Bash 3.2 can hide
      // an error if the child inherits the parent's EXIT cleanup trap.
      testScript = path.join(root, 'scripts', 'smoke-test.sh');
      fs.mkdirSync(path.dirname(testScript));
      fs.writeFileSync(testScript, fs.readFileSync(script, 'utf8').replace(
        '"$@" -- --include-ignored',
        '"${SMOKE_SCRIPT_TEST_MISSING:?injected argument failure}" -- --include-ignored',
      ));
      delete env.SMOKE_SCRIPT_TEST_MISSING;
    }
    const result = spawnSync(shell, [testScript, ...options], { env, encoding: 'utf8', timeout: 15_000 });
    assert.ifError(result.error);
    const calls = fs.existsSync(log) ? fs.readFileSync(log, 'utf8').trim().split('\n').map(JSON.parse) : [];
    assert.deepEqual(fs.readdirSync(root).filter(name => name.startsWith('linknestx-smoke-build.')), [],
      'owned temporary build directory must be removed');
    return { ...result, calls };
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
}

const cargoTests = calls => calls.filter(call => call.kind === 'cargo-stub' && call.args[0] !== '--version');
const shells = ['/bin/bash', '/opt/homebrew/bin/bash', '/usr/local/bin/bash']
  .filter((shell, index) => index === 0 || fs.existsSync(shell));
for (const shell of shells) {
  const settings = { skip: process.platform === 'win32' && 'POSIX regression; Windows real smoke remains mandatory' };
  for (const [options, filter] of [[[], []], [['--export-only'], ['actual_go_native_audit_export']]]) {
    const mode = options.length ? 'export-only' : 'default';
    test(`${shell}: ${mode} forwards every required smoke argument`, settings, () => {
      const result = runCase(shell, { options });
      assert.equal(result.status, 0, result.stderr);
      const calls = cargoTests(result.calls);
      assert.equal(calls.length, 1, result.stderr);
      assert.deepEqual(calls[0].args, [
        'test', '--locked', '--manifest-path', path.join(path.dirname(script), '../client/src-tauri/Cargo.toml'),
        '--test', 'server_smoke', ...filter, '--', '--include-ignored', '--nocapture', '--test-threads=1',
      ]);
      assert.equal(path.dirname(calls[0].server), calls[0].temp);
      assert.equal(path.dirname(calls[0].marker), calls[0].temp);
    });
    test(`${shell}: ${mode} preserves Cargo failure through cleanup`, settings, () => {
      const result = runCase(shell, { options, cargoExit: 37 });
      assert.equal(result.status, 37, result.stderr);
    });
  }
  test(`${shell}: Go failure stops before native tests and survives cleanup`, settings, () => {
    const result = runCase(shell, { goExit: 23 });
    assert.equal(result.status, 23, result.stderr);
    assert.deepEqual(cargoTests(result.calls), []);
  });
  test(`${shell}: invalid options fail before either tool runs`, settings, () => {
    const result = runCase(shell, { options: ['--unknown'] });
    assert.equal(result.status, 2, result.stderr);
    assert.deepEqual(result.calls, []);
  });
  test(`${shell}: asynchronous argument failure cannot become success`, settings, () => {
    const result = runCase(shell, { failExpansion: true });
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stderr, /injected argument failure/);
    assert.deepEqual(cargoTests(result.calls), []);
  });
  test(`${shell}: cleanup leaves unrelated marker processes alive`, settings, async () => {
    const unrelated = spawn('/bin/sleep', ['30']);
    const exited = new Promise(resolve => unrelated.once('exit', resolve));
    try {
      const result = runCase(shell, { options: ['--export-only'], cargoExit: 37, foreignPid: unrelated.pid });
      assert.equal(result.status, 37, result.stderr);
      assert.doesNotThrow(() => process.kill(unrelated.pid, 0), 'cleanup must not signal unrelated marker PIDs');
    } finally {
      unrelated.kill();
      await exited;
    }
  });
}

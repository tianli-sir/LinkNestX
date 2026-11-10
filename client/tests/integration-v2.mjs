// Real built Web UI + production Go router, isolated loopback data only.
// Build first: go build -o .cache/v2-integration-server ./server/cmd/server
// Then: cd client && npm run build && node tests/integration-v2.mjs
import assert from 'node:assert/strict';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const root = fileURLToPath(new URL('../../', import.meta.url));
const output = path.join(root, 'docs/verification/v2');
const scratch = await fs.mkdtemp(path.join(root, '.cache/v2-web-'));
await fs.mkdir(output, { recursive: true });
const probe = net.createServer();
probe.listen(0, '127.0.0.1');
await once(probe, 'listening');
const port = probe.address().port;
await new Promise(resolve => probe.close(resolve));
const origin = `http://127.0.0.1:${port}`;
// A .test hostname resolved locally exercises the same insecure-context APIs as
// the approved LAN HTTP deployment; localhost alone is treated as secure.
const browserOrigin = `http://linknestx.test:${port}`;
const initialPassword = randomBytes(24).toString('base64url');
const password = randomBytes(24).toString('base64url');
const processErrors = [];
const server = spawn(path.join(root, '.cache/v2-integration-server'), [], {
  cwd: scratch,
  env: { ...process.env,
    LINKNESTX_BIND_ADDRESS: `127.0.0.1:${port}`,
    LINKNESTX_STORAGE_ROOT: path.join(scratch, 'storage'),
    LINKNESTX_SQLITE_PATH: path.join(scratch, 'test.sqlite3'),
    LINKNESTX_WEB_ASSETS_DIR: path.join(root, 'client/dist'),
    LINKNESTX_RELEASE_DIR: process.env.LINKNESTX_TEST_RELEASE_ASSETS ?? path.join(scratch, 'releases'),
    LINKNESTX_BOOTSTRAP_ADMIN_USERNAME: 'test-admin',
    LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD: initialPassword,
    LINKNESTX_JWT_SIGNING_KEY: randomBytes(32).toString('hex'),
    LINKNESTX_JWT_REFRESH_KEY: randomBytes(32).toString('hex'),
  }, stdio: ['ignore', 'ignore', 'pipe'],
});
server.on('error', e => processErrors.push(e.message));
server.stderr.on('data', bytes => processErrors.push(bytes.toString()));
let browser;
const evidence = { checks: [], browserErrors: [], externalRequests: [], screenshots: [] };
const pass = name => { evidence.checks.push(name); console.log(`PASS ${name}`); };
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, name) {
  const end = Date.now() + 15000;
  while (Date.now() < end) { if (await check()) return; await delay(100); }
  throw new Error(`Timed out: ${name}`);
}
async function api(method, route, body, session, expected) {
  const response = await fetch(origin + '/api/v1' + route, {
    method, headers: { 'Content-Type': 'application/json', ...(session ? { Authorization: `Bearer ${session.accessToken}` } : {}) },
    body: body === undefined ? undefined : Buffer.isBuffer(body) ? body : JSON.stringify(body),
  });
  const text = await response.text();
  assert.equal(response.status, expected ?? (method === 'POST' ? 200 : 200), `${method} ${route}: ${text}`);
  return text ? JSON.parse(text) : undefined;
}
async function login(username, currentPassword = password) {
  return api('POST', '/auth/login', { username, password: currentPassword });
}
async function screenshot(page, name) {
  const filename = `real-${name}.png`;
  await page.screenshot({ path: path.join(output, filename) });
  evidence.screenshots.push(filename);
}
try {
  await until(async () => { try { return (await fetch(origin + '/healthz')).ok; } catch { return false; } }, 'local server ready');
  if (process.env.LINKNESTX_TEST_RELEASE_ASSETS) {
    const catalog = await api('GET', '/releases');
    assert.ok(catalog.releases.length > 0);
    const landing = await (await fetch(origin + '/downloads')).text();
    for (const item of catalog.releases) {
      assert.ok(landing.includes(item.name));
      const result = await fetch(origin + item.url);
      assert.equal(result.status, 200);
      const bytes = Buffer.from(await result.arrayBuffer());
      assert.equal(bytes.length, item.size);
      assert.equal(createHash('sha256').update(bytes).digest('hex'), item.sha256);
      assert.equal((await fetch(`${origin}/updates/${item.platform}/${item.arch}/latest.json`)).status, 200);
    }
    pass('actual public installer catalog, downloads and update manifest routes');
  }
  const admin = await login('test-admin', initialPassword);
  await api('POST', '/auth/password', { currentPassword: initialPassword, newPassword: password }, admin);
  const accounts = [];
  for (const [username, displayName] of [['alice', '林晓'], ['bob', '陈远']]) {
    const user = await api('POST', '/users', { username, password: initialPassword }, admin, 201);
    const session = await login(username, initialPassword);
    await api('POST', '/auth/password', { currentPassword: initialPassword, newPassword: password }, session);
    const profile = await api('PATCH', '/profile', { displayName }, session);
    accounts.push({ ...session, user: { ...user, ...profile } });
  }
  const [alice, bob] = accounts;
  const group = await api('POST', '/groups', { name: '产品协作', ownerId: alice.user.id }, admin, 201);
  await api('POST', `/groups/${group.id}/members`, { userId: bob.user.id, role: 'member' }, alice, 204);
  const send = (conversation, session, body) => api('POST', `/conversations/${conversation}/messages`, { clientMessageId: randomUUID(), body }, session, 201);
  const greeting = await send(group.id, bob, '欢迎加入产品协作，我们在这里整理讨论与文件。');
  const payload = Buffer.from('LinkNestX 0.2 本地真实文件下载验证\n'.repeat(5000));
  const hash = createHash('sha256').update(payload).digest('hex');
  const upload = await api('POST', '/files/uploads', { groupId: group.id, name: '协作说明.txt', size: payload.length, sha256: hash, chunks: [{ index: 0, hash, size: payload.length }] }, bob, 201);
  await api('PUT', `/uploads/${upload.uploadId}/chunks/0`, payload, bob, 204);
  await api('POST', `/uploads/${upload.uploadId}/complete`, {}, bob);
  pass('real API bootstrap, password change, profiles, group and file publication');

  const cachedBrowser = path.join(root, '.cache/playwright/chromium_headless_shell-1254/chrome-headless-shell-mac-arm64/chrome-headless-shell');
  browser = await chromium.launch({ headless: true, executablePath: process.env.LINKNESTX_TEST_BROWSER ?? cachedBrowser, args: ['--host-resolver-rules=MAP linknestx.test 127.0.0.1', '--no-proxy-server'] });
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, acceptDownloads: true });
  const page = await context.newPage();
  page.on('pageerror', e => evidence.browserErrors.push(e.message));
  page.on('request', r => { if (!r.url().startsWith(browserOrigin) && !r.url().startsWith('data:')) evidence.externalRequests.push(r.url()); });
  await page.goto(browserOrigin + '/app/');
  assert.equal(await page.evaluate(() => window.isSecureContext), false);
  assert.equal(await page.evaluate(() => typeof crypto.randomUUID), 'undefined');
  evidence.insecureHttpContext = true;
  await page.getByRole('heading', { name: '登录工作区' }).waitFor();
  await screenshot(page, 'login-desktop');
  await page.getByLabel('账号', { exact: true }).fill(alice.user.accountNumber);
  await page.getByLabel('密码', { exact: true }).fill(password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await page.getByRole('button', { name: '产品协作', exact: true }).click();
  await page.getByRole('region', { name: '聊天记录' }).getByText(greeting.body, { exact: true }).waitFor();
  await page.bringToFront();
  await page.getByLabel('消息内容').focus();
  await until(async () => (await api('GET', `/conversations/${group.id}/messages/${greeting.id}/readers`, undefined, bob)).count === 1, 'visible message read receipt');
  assert.equal(await page.getByRole('button', { name: '添加文件', exact: true }).count(), 0);
  pass('built /app/ assets, numeric cookie login, visible read receipt, Web upload hidden');

  await page.getByLabel('消息内容').fill('已收到，我会把讨论结果整理到项目文件中。');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await until(async () => (await api('GET', `/conversations/${group.id}/messages`, undefined, bob)).messages.some(m => m.body === '已收到，我会把讨论结果整理到项目文件中。'), 'Web message persisted');
  const history = await api('GET', `/conversations/${group.id}/messages`, undefined, bob);
  const own = history.messages.find(m => m.senderId === alice.user.id);
  await api('POST', `/conversations/${group.id}/read`, { messageIds: [own.id] }, bob);
  await page.getByRole('button', { name: '1 人已读', exact: true }).click();
  await page.getByRole('dialog', { name: '已读成员' }).getByText('陈远', { exact: true }).waitFor();
  await screenshot(page, 'group-readers');
  const obscured = await send(group.id, bob, '这条消息被已读名单遮挡，关闭名单后才应标记已读。');
  await page.getByRole('region', { name: '聊天记录' }).getByText(obscured.body, { exact: true }).waitFor();
  await delay(700);
  assert.equal((await api('GET', `/conversations/${group.id}/messages/${obscured.id}/readers`, undefined, bob)).count, 0);
  await page.getByRole('dialog', { name: '已读成员' }).getByRole('button', { name: '关闭', exact: true }).click();
  await page.getByLabel('消息内容').focus();
  await until(async () => (await api('GET', `/conversations/${group.id}/messages/${obscured.id}/readers`, undefined, bob)).count === 1, 'unobscured message read');
  const live = await send(group.id, bob, '这条消息通过实时连接送达，无需刷新页面。');
  await page.getByRole('region', { name: '聊天记录' }).getByText(live.body, { exact: true }).waitFor();
  pass('Web message persisted, cookie WebSocket live delivery and group reader list');
  const downloadEvent = page.waitForEvent('download');
  await page.getByRole('button', { name: '下载文件', exact: true }).click();
  const download = await downloadEvent;
  assert.equal(download.suggestedFilename(), '协作说明.txt');
  assert.equal(await download.failure(), null);
  assert.deepEqual(await fs.readFile(await download.path()), payload);
  await screenshot(page, 'chat-desktop');
  pass('actual browser file download and byte-for-byte verification');

  const blocked = await page.evaluate(async () => {
    const session = await (await fetch('/api/v1/web/session')).json();
    const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': session.csrfToken };
    return {
      upload: (await fetch('/api/v1/files/uploads', { method: 'POST', headers, body: '{}' })).status,
      noCsrf: (await fetch('/api/v1/friend-requests', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })).status,
      p2p: (await fetch('/api/v1/p2p/config')).status,
      cookieHidden: !document.cookie.includes('lnx_web'),
      noTokens: !JSON.stringify(localStorage).match(/accessToken|refreshToken|csrfToken/),
    };
  });
  assert.equal(blocked.upload, 403); assert.equal(blocked.noCsrf, 403); assert.equal(blocked.p2p, 403);
  assert.ok(blocked.cookieHidden); assert.ok(blocked.noTokens);
  pass('server denies browser upload and missing CSRF; HttpOnly cookie and no persisted tokens');

  await page.getByRole('button', { name: '联系人', exact: true }).click();
  const inactive = await send(group.id, bob, '切换到联系人时，后台会话中的消息保持未读。');
  await delay(700);
  assert.equal((await api('GET', `/conversations/${group.id}/messages/${inactive.id}/readers`, undefined, bob)).count, 0);
  pass('modal-obscured and inactive conversation messages remain unread');
  await page.getByLabel('通过数字账号添加好友').fill(bob.user.accountNumber);
  await page.getByRole('button', { name: '查找', exact: true }).click();
  await page.getByRole('button', { name: '查看资料', exact: true }).click();
  await page.getByRole('button', { name: '添加好友', exact: true }).click();
  const contacts = await api('GET', '/contacts', undefined, bob);
  assert.equal(contacts.incoming.length, 1);
  await api('POST', `/friend-requests/${contacts.incoming[0].id}/accept`, {}, bob);
  await page.getByRole('dialog', { name: '个人资料' }).getByRole('button', { name: '发送消息', exact: true }).click();
  await page.getByRole('heading', { name: '陈远', exact: true }).waitFor();
  await page.getByLabel('消息内容').fill('你好，这是通过好友申请后开启的私聊。');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  const direct = await api('POST', '/conversations/direct', { userId: alice.user.id }, bob);
  await until(async () => (await api('GET', `/conversations/${direct.id}/messages`, undefined, bob)).messages.length === 1, 'direct persisted');
  const directMessage = (await api('GET', `/conversations/${direct.id}/messages`, undefined, bob)).messages[0];
  await page.getByRole('button', { name: '未读', exact: true }).waitFor();
  await api('POST', `/conversations/${direct.id}/read`, { messageIds: [directMessage.id] }, bob);
  await page.getByRole('button', { name: '已读', exact: true }).waitFor();
  await screenshot(page, 'direct-desktop');
  await page.setViewportSize({ width: 390, height: 844 });
  await screenshot(page, 'direct-compact');
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  pass('number search, friend request approval, direct chat unread/read, compact layout');
  await page.reload();
  await page.getByRole('button', { name: '陈远', exact: true }).waitFor();
  pass('cookie session restores after page reload');
  assert.deepEqual(evidence.browserErrors, []);
  assert.deepEqual(evidence.externalRequests, []);
  evidence.status = 'passed';
} catch (error) {
  evidence.status = 'failed'; evidence.failure = error.stack;
  throw error;
} finally {
  await fs.writeFile(path.join(output, 'real-browser-integration.json'), JSON.stringify(evidence, null, 2) + '\n');
  if (browser) await browser.close();
  const exited = server.exitCode !== null ? Promise.resolve() : once(server, 'exit');
  server.kill('SIGTERM');
  await Promise.race([exited, delay(12000)]);
  if (server.exitCode === null) server.kill('SIGKILL');
  await fs.writeFile(path.join(scratch, 'server.log'), processErrors.join(''));
}

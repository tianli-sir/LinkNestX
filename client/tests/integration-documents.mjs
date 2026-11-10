// Real built Web UI + production Go router, isolated loopback data only.
// Build first: go build -o .cache/v3/integration-server ./server/cmd/server
// Then: cd client && npm run build && node tests/integration-documents.mjs
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
const output = path.resolve(process.env.LINKNESTX_TEST_OUTPUT ?? path.join(root, '.cache/verification/documents'));
const scratch = await fs.mkdtemp(path.join(root, '.cache/v3-web-'));
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
const server = spawn(process.env.LINKNESTX_TEST_SERVER ?? path.join(root, '.cache/v3/integration-server'), [], {
  cwd: scratch,
  env: { ...process.env,
    LINKNESTX_BIND_ADDRESS: `127.0.0.1:${port}`,
    LINKNESTX_STORAGE_ROOT: path.join(scratch, 'storage'),
    LINKNESTX_SQLITE_PATH: path.join(scratch, 'test.sqlite3'),
    LINKNESTX_WEB_ASSETS_DIR: process.env.LINKNESTX_TEST_WEB_DIR ?? path.join(root, 'client/dist'),
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
  const admin = await login('test-admin', initialPassword);
  await api('POST', '/auth/password', { currentPassword: initialPassword, newPassword: password }, admin);
  for (const username of ['writer', 'reader']) {
    await api('POST', '/users', { username, password: initialPassword }, admin, 201);
    const user = await login(username, initialPassword);
    await api('POST', '/auth/password', { currentPassword: initialPassword, newPassword: password }, user);
  }
  const writer = await login('writer');
  const reader = await login('reader');
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}), args: ['--host-resolver-rules=MAP linknestx.test 127.0.0.1', '--no-proxy-server'] });
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, acceptDownloads: true });
  const page = await context.newPage();
  page.on('pageerror', e => evidence.browserErrors.push(e.message));
  page.on('request', r => { if (!r.url().startsWith(browserOrigin) && !r.url().startsWith('data:')) evidence.externalRequests.push(r.url()); });
  await page.goto(browserOrigin + '/app/');
  await page.getByLabel('账号', { exact: true }).fill('writer');
  await page.getByLabel('密码', { exact: true }).fill(password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await page.getByRole('button', { name: '文档', exact: true }).click();
  await page.getByRole('button', { name: '写文档', exact: true }).click();
  await page.getByLabel('文档标题', { exact: true }).fill('团队入门教程');
  await page.getByLabel('Markdown 正文').fill('# 欢迎加入\n\n这是 **Markdown** 教程。\n\n<script>window.documentUnsafe=true</script>\n\n![外部图片](https://example.invalid/tracker.png)\n\n[危险链接](javascript:alert(1))');
  await page.getByLabel('可见范围').selectOption('team');
  assert.equal(await page.getByRole('button', { name: '添加附件', exact: true }).count(), 0);
  await page.getByRole('button', { name: '保存草稿', exact: true }).click();
  await page.getByRole('heading', { name: '团队入门教程', exact: true }).waitFor();
  let docs = await api('GET', '/documents?scope=mine', undefined, writer);
  assert.equal(docs.documents.length, 1);
  let doc = docs.documents[0];
  assert.equal(doc.status, 'draft');
  await api('GET', `/documents/${doc.id}`, undefined, reader, 404);
  await api('GET', `/documents/${doc.id}`, undefined, admin, 404);
  assert.equal(await page.evaluate(() => window.documentUnsafe), undefined);
  assert.equal(await page.locator('.doc-markdown a[href^="javascript:"]').count(), 0);
  pass('real Web draft creation, restricted visibility, safe Markdown and no browser upload');

  await page.getByRole('button', { name: '编辑文档', exact: true }).click();
  await page.getByRole('button', { name: '发布文档', exact: true }).click();
  await until(async () => (await api('GET', `/documents/${doc.id}`, undefined, reader)).status === 'published', 'published team document');
  const bytes = Buffer.from('Document attachment test\n'.repeat(3000));
  const hash = createHash('sha256').update(bytes).digest('hex');
  const upload = await api('POST', '/files/uploads', { documentId: doc.id, name: '教程附件.txt', size: bytes.length, sha256: hash, chunks: [{ index: 0, hash, size: bytes.length }] }, writer, 201);
  await api('PUT', `/uploads/${upload.uploadId}/chunks/0`, bytes, writer, 204);
  await api('POST', `/uploads/${upload.uploadId}/complete`, {}, writer);
  doc = await api('GET', `/documents/${doc.id}`, undefined, writer);
  assert.equal(doc.attachments.length, 1);
  assert.equal(doc.attachments[0].expiresAt, null);
  await api('PATCH', `/documents/${doc.id}`, { revision: doc.revision, title: doc.title, body: doc.body + `\n\n[下载教程](linknestx-file:${doc.attachments[0].versionId})`, visibility: 'team', status: 'published' }, writer);
  await page.getByRole('button', { name: '下载教程', exact: true }).waitFor();
  const nextDownload = page.waitForEvent('download');
  await page.getByRole('button', { name: '下载教程', exact: true }).click();
  const download = await nextDownload;
  assert.equal(await download.failure(), null);
  assert.deepEqual(await fs.readFile(await download.path()), bytes);
  pass('published document and real authenticated Markdown attachment download');
  doc = await api('GET', `/documents/${doc.id}`, undefined, writer);
  await api('POST', `/documents/${doc.id}/curation`, { revision: doc.revision, curated: true }, admin);
  await page.getByText('团队公共文档', { exact: true }).waitFor();
  await until(async () => await page.getByRole('button', { name: '编辑文档', exact: true }).count() === 0, 'curated author read-only');
  assert.equal(await page.getByRole('button', { name: '删除文档', exact: true }).count(), 0);
  await screenshot(page, 'document-curated-light');
  await page.getByRole('button', { name: '返回文档', exact: true }).click();
  await page.getByRole('button', { name: '公共文档', exact: true }).click();
  await page.getByRole('button', { name: '阅读 团队入门教程', exact: true }).waitFor();
  assert.equal((await api('GET', '/documents?scope=public', undefined, reader)).documents.length, 1);
  pass('administrator curation reflected live and author edit/delete controls removed');
  const friendRequest = await api('POST', '/friend-requests', { accountNumber: reader.user.accountNumber }, writer, 201);
  await api('POST', `/friend-requests/${friendRequest.id}/accept`, {}, reader);
  const friendsNote = await api('POST', '/documents', { title: '仅好友的协作记录', body: '好友可读的记录内容', visibility: 'friends', status: 'published' }, reader, 201);
  await page.getByRole('button', { name: '知识动态', exact: true }).click();
  await page.getByRole('button', { name: '阅读 仅好友的协作记录', exact: true }).click();
  await page.getByRole('heading', { name: '仅好友的协作记录', exact: true }).waitFor();
  await api('DELETE', `/contacts/${writer.user.id}`, undefined, reader, 204);
  await until(async () => await page.getByRole('heading', { name: '仅好友的协作记录', exact: true }).count() === 0, 'revoked friends document removed');
  await api('GET', `/documents/${friendsNote.id}`, undefined, writer, 404);
  await page.getByRole('button', { name: '返回文档', exact: true }).click();
  assert.equal(await page.getByRole('button', { name: '阅读 仅好友的协作记录', exact: true }).count(), 0);
  pass('friend removal invalidates open document and cached feed through live server events');
  const anonymous = await fetch(`${origin}/api/v1/documents/${doc.id}`);
  assert.equal(anonymous.status, 401);
  const anonymousAttachment = await fetch(`${origin}/api/v1/web/files/${doc.attachments[0].versionId}/download`);
  assert.equal(anonymousAttachment.status, 401);
  await page.setViewportSize({ width: 390, height: 844 });
  await screenshot(page, 'documents-compact');
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  assert.deepEqual(evidence.browserErrors, []);
  assert.deepEqual(evidence.externalRequests, []);
  pass('anonymous document/attachment access denied and compact layout fits');
  evidence.status = 'passed';
} catch (error) {
  evidence.status = 'failed'; evidence.failure = error.stack;
  throw error;
} finally {
  await fs.writeFile(path.join(output, 'documents-browser-integration.json'), JSON.stringify(evidence, null, 2) + '\n');
  if (browser) await browser.close();
  const exited = server.exitCode !== null ? Promise.resolve() : once(server, 'exit');
  server.kill('SIGTERM');
  await Promise.race([exited, delay(12000)]);
  if (server.exitCode === null) server.kill('SIGKILL');
  await fs.writeFile(path.join(scratch, 'server.log'), processErrors.join(''));
}

const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { validateRequest, safeAsset, mayOpenExternal, rendererSession } = require('../shell/boundary.cjs');

test('renderer cannot invoke privileged path or lifecycle pipe operations', () => {
  for (const command of ['import_files','authorize_destination','selection_scope','session_identity','shutdown','import_clipboard_image','import_clipboard_files','read_file']) {
    assert.throws(() => validateRequest(command, {}));
  }
  assert.throws(() => validateRequest('login',{username:'u',password:'p',serverUrl:'https://evil.test'}));
  assert.throws(() => validateRequest('pick_upload_files',{paths:['C:\\secrets.txt']}));
  assert.throws(() => validateRequest('export_audit',{exportId:'x',format:'csv',action:'',destination:'C:\\secret'}));
  assert.throws(() => validateRequest('api_request',{method:'POST',path:'/api/v1/auth/refresh'}));
  assert.throws(() => validateRequest('api_request',{method:'GET',path:'/api/v1/../secret'}));
  assert.doesNotThrow(() => validateRequest('start_download',{versionId:'version',destination:'opaque-token'}));
});
test('only local bundled assets and web URLs are accepted', () => {
  assert.equal(safeAsset('linknestx-legacy://app/assets/a.js','/opt/ui'),path.resolve('/opt/ui/assets/a.js'));
  for (const url of ['file:///etc/passwd','https://evil.test/','linknestx-legacy://else/a','linknestx-legacy://app/%2e%2e%2fsecret']) assert.throws(()=>safeAsset(url,'/opt/ui'));
  assert(mayOpenExternal('https://example.org/tutorial'));
  assert(!mayOpenExternal('file:///etc/passwd')); assert(!mayOpenExternal('javascript:alert(1)'));
  assert(mayOpenExternal('mailto:help@example.org?subject=Help'));
  for(const url of ['https://user:pass@example.org','https://example.org/\nfile','mailto:help@example.org?attach=C:/secret','ms-settings:display'])assert(!mayOpenExternal(url));
});
test('native credentials never cross the renderer boundary',()=>{
  const session=rendererSession({id:'x',user:{id:'u'},deviceId:'d',expiresAt:'date',accessToken:'secret',refreshToken:'secret'});
  assert.equal(session.accessToken,undefined);assert.equal(session.refreshToken,undefined);
  assert.equal(session.user.id,'u');
});
test('image preview and file launch commands accept only identifiers', () => {
  for (const command of ['file_attachment_preview','file_attachment_thumb']) assert.doesNotThrow(()=>validateRequest(command,{versionId:'version'}));
  assert.doesNotThrow(()=>validateRequest('open_downloaded_file',{taskId:'task'}));
  assert.doesNotThrow(()=>validateRequest('reveal_downloaded_file',{taskId:'task'}));
  assert.doesNotThrow(()=>validateRequest('check_downloaded_files',{taskIds:['a','b']}));
  // 渲染层永远不能携带路径：额外参数、路径参数、错误的 taskIds 类型全部拒绝
  assert.throws(()=>validateRequest('open_downloaded_file',{taskId:'a',path:'C:\\evil.exe'}));
  assert.throws(()=>validateRequest('reveal_downloaded_file',{path:'C:\\evil.exe'}));
  assert.throws(()=>validateRequest('check_downloaded_files',{taskIds:'a'}));
  assert.throws(()=>validateRequest('check_downloaded_files',{taskIds:[1,2]}));
  assert.throws(()=>validateRequest('file_attachment_preview',{versionId:'v',url:'file:///etc/passwd'}));
});

// Scoped acceptance: real built SPA + isolated local Go server, mobile group management.
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import fs from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {chromium} from 'playwright';
const root=fileURLToPath(new URL('../../../',import.meta.url));
const output=path.join(root,'docs/verification/v2');
const scratch=await fs.mkdtemp(path.join(root,'.cache/v2-mobile-review-'));
const probe=net.createServer();probe.listen(0,'127.0.0.1');await once(probe,'listening');const port=probe.address().port;await new Promise(resolve=>probe.close(resolve));
const origin=`http://127.0.0.1:${port}`;
const password=randomBytes(24).toString('base64url');const nextPassword=randomBytes(24).toString('base64url');
const server=spawn(path.join(root,'.cache/v2-integration-server'),[],{cwd:scratch,env:{...process.env,LINKNESTX_BIND_ADDRESS:`127.0.0.1:${port}`,LINKNESTX_STORAGE_ROOT:path.join(scratch,'storage'),LINKNESTX_SQLITE_PATH:path.join(scratch,'test.sqlite3'),LINKNESTX_WEB_ASSETS_DIR:path.join(root,'client/dist'),LINKNESTX_RELEASE_DIR:path.join(scratch,'releases'),LINKNESTX_BOOTSTRAP_ADMIN_USERNAME:'review-admin',LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD:password,LINKNESTX_JWT_SIGNING_KEY:randomBytes(32).toString('hex'),LINKNESTX_JWT_REFRESH_KEY:randomBytes(32).toString('hex')},stdio:['ignore','ignore','pipe']});
let serverLog='';server.stderr.on('data',data=>serverLog+=data.toString());let browser;
const result={source:'Real production built Web SPA + isolated local Go server; no fixture transport.',status:'pending',checks:[],browserErrors:[],directoryRequests:[],screenshots:[]};
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function api(route,body,session,expected=200,method='POST'){const response=await fetch(origin+'/api/v1'+route,{method,headers:{'Content-Type':'application/json',...(session?{Authorization:`Bearer ${session.accessToken}`}:{})},body:JSON.stringify(body)});assert.equal(response.status,expected,route);return response.status===204?null:response.json()}
try{
 for(let i=0;i<150;i++){try{if((await fetch(origin+'/healthz')).ok)break}catch{}await delay(100)}
 const session=await api('/auth/login',{username:'review-admin',password});await api('/auth/password',{currentPassword:password,newPassword:nextPassword},session);
 const profile=await api('/profile',{displayName:'林晓'},session,200,'PATCH');assert.match(profile.accountNumber,/^\d+$/);
 await api('/groups',{name:'移动协作团队',ownerId:session.user.id},session,201);
 browser=await chromium.launch({headless:true,executablePath:path.join(root,'.cache/playwright/chromium_headless_shell-1254/chrome-headless-shell-mac-arm64/chrome-headless-shell')});
 const page=await browser.newPage({viewport:{width:390,height:844}});page.on('pageerror',e=>result.browserErrors.push(e.message));page.on('request',request=>{if(new URL(request.url()).pathname==='/api/v1/users')result.directoryRequests.push(request.url())});await page.goto(origin+'/app/');await page.getByLabel('账号',{exact:true}).fill('review-admin');await page.getByLabel('密码',{exact:true}).fill(nextPassword);await page.getByRole('button',{name:'登录',exact:true}).click();
 await page.getByRole('button',{name:'移动协作团队',exact:true}).waitFor();assert.equal(await page.getByRole('heading',{name:'移动协作团队',exact:true}).count(),0);
 await page.getByRole('button',{name:'群组与成员管理',exact:true}).click();await page.getByRole('heading',{name:'移动协作团队',exact:true}).waitFor({state:'visible',timeout:3000});
 assert.equal(await page.getByRole('button',{name:'群组与成员管理',exact:true}).isVisible(),false);result.checks.push('At 390px, group management opens before selecting any conversation');
 await page.getByText('林晓',{exact:true}).waitFor({state:'visible'});await page.getByText(`账号 ${profile.accountNumber}`,{exact:true}).waitFor({state:'visible'});assert.equal((await page.locator('body').innerText()).includes(profile.id),false);assert.deepEqual(result.directoryRequests,[]);result.checks.push('Member row shows a name, avatar and numeric account from the permitted profile route; no internal UUID or broad directory request');
 await page.getByRole('button',{name:'查看林晓的资料',exact:true}).click();const dialog=page.getByRole('dialog',{name:'个人资料'});await dialog.waitFor({state:'visible'});await dialog.getByText(`账号 ${profile.accountNumber}`,{exact:true}).waitFor({state:'visible'});await dialog.getByRole('button',{name:'关闭',exact:true}).click();await dialog.waitFor({state:'hidden'});result.checks.push('Member avatar opens the existing personal profile and closes cleanly');
 const filename='real-group-management-mobile.png';await page.screenshot({path:path.join(output,filename),animations:'disabled'});result.screenshots.push(filename);
 await page.getByRole('button',{name:'返回聊天',exact:true}).click();await page.getByRole('button',{name:'群组与成员管理',exact:true}).waitFor({state:'visible'});result.checks.push('Back returns to the conversation list without selecting a conversation');
 result.mobileOverflow=await page.evaluate(()=>({scrollWidth:document.documentElement.scrollWidth,clientWidth:document.documentElement.clientWidth}));assert.equal(result.mobileOverflow.scrollWidth,result.mobileOverflow.clientWidth);assert.deepEqual(result.browserErrors,[]);result.status='passed';console.log(JSON.stringify(result));
}catch(error){result.status='failed';result.failure=error.stack;throw error}finally{await fs.writeFile(path.join(output,'real-mobile-review.json'),JSON.stringify(result,null,2)+'\n');if(browser)await browser.close();const exited=server.exitCode!==null?Promise.resolve():once(server,'exit');server.kill('SIGTERM');await Promise.race([exited,delay(5000)]);if(server.exitCode===null)server.kill('SIGKILL');await fs.writeFile(path.join(scratch,'server.log'),serverLog)}

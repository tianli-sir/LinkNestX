import assert from 'node:assert/strict';
import {chromium} from 'playwright';
import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=fileURLToPath(new URL('../../../',import.meta.url));
const output=process.env.LINKNESTX_TEST_OUTPUT??path.join(root,'.cache/v3/visual');
await fs.mkdir(output,{recursive:true});
const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_EXECUTABLE_PATH}:{})});
const page=await browser.newPage({viewport:{width:1440,height:960},deviceScaleFactor:1});
const errors=[];page.on('pageerror',e=>errors.push(e.message));
const captures=[];
async function capture(name){await page.waitForTimeout(150);await page.screenshot({animations:'disabled',path:path.join(output,name+'.png')});const fits=await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth);assert(fits,name+' overflows');captures.push(name);}
try{
 await page.goto((process.env.VISUAL_ORIGIN??'http://127.0.0.1:1434')+'/preview-v3.html');
 await page.getByRole('heading',{name:'登录工作区'}).waitFor();await capture('login-light');
 await page.getByLabel('账号',{exact:true}).fill('10000001');await page.getByLabel('密码',{exact:true}).fill('preview');await page.getByRole('button',{name:'登录',exact:true}).click();
 await page.getByRole('button',{name:'产品设计协作',exact:true}).click();await page.getByRole('region',{name:'聊天记录'}).getByText('整体方向很清晰。我们今天先把核心流程走通，细节我会直接标在文档里。').waitFor();
 for(const theme of ['light','dark']){
  await page.getByRole('banner').getByLabel('外观主题',{exact:true}).selectOption(theme);
  await page.getByRole('button',{name:'消息',exact:true}).click();await capture('chat-'+theme);
  await page.getByRole('button',{name:'文档',exact:true}).click();await page.getByRole('button',{name:'阅读 把一次好的协作，变成团队的日常',exact:true}).waitFor();await capture('documents-'+theme);
  await page.getByRole('button',{name:'阅读 把一次好的协作，变成团队的日常',exact:true}).click();await page.getByRole('heading',{name:'把一次好的协作，变成团队的日常',exact:true}).waitFor();await capture('article-'+theme);
  await page.getByRole('button',{name:'编辑文档',exact:true}).click();await page.getByLabel('文档标题').waitFor();await capture('editor-'+theme);
  await page.getByRole('button',{name:'返回文档',exact:true}).click();
  for(const [label,name,ready] of [['联系人','contacts','联系人'],['文件','files','公共文件池'],['传输','transfers','传输中心']]){await page.getByRole('button',{name:label,exact:true}).click();await page.waitForTimeout(150);await capture(name+'-'+theme);}
  await page.getByRole('button',{name:'工作区设置',exact:true}).click();await capture('settings-'+theme);
 }
 await page.setViewportSize({width:390,height:844});
 for(const [label,name]of [['消息','chat'],['文档','documents'],['联系人','contacts'],['传输','transfers']]){await page.getByRole('button',{name:label,exact:true}).click();await capture(name+'-mobile');}
 assert.deepEqual(errors,[]);
 await fs.writeFile(path.join(output,'result.json'),JSON.stringify({source:'Production UI with deterministic sample data',errors,captures},null,2));
 console.log(JSON.stringify({output,captures:captures.length,errors}));
}finally{await browser.close()}

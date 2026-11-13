'use strict';
const {app,BrowserWindow,Menu,Tray,Notification,dialog,clipboard,ipcMain,nativeImage,protocol,session,shell}=require('electron');
const fs=require('node:fs/promises');
const path=require('node:path');
const os=require('node:os');
const {NativeHost}=require('./native.cjs');
const {createNoticeReducer}=require('./notices.cjs');
const reduceNotice=createNoticeReducer();
const {createSessionRecovery}=require('./session-recovery.cjs');
const sessionRecovery=createSessionRecovery(reduceNotice);
const {validateRequest,safeAsset,mayOpenExternal,rendererSession}=require('./boundary.cjs');
const ORIGIN='linknestx-legacy://app';
protocol.registerSchemesAsPrivileged([{scheme:'linknestx-legacy',privileges:{standard:true,secure:true,supportFetchAPI:true,corsEnabled:false}}]);
app.setName('LinkNestX Legacy');
let window,host,tray,quitting=false,dialogActive=false;
let resyncRunning=false,resyncAgain=false;
let connectionRevision=0;
const appRoot=app.isPackaged?path.join(process.resourcesPath,'app'):path.resolve(__dirname,'..');
const uiRoot=path.join(appRoot,'ui');
const nativeRoot=app.isPackaged?path.join(process.resourcesPath,'native'):path.join(appRoot,'native-bin');
const executable=path.join(nativeRoot,process.platform==='win32'?'linknestx-legacy-native.exe':'linknestx-legacy-native');
const loginItemName=`LinkNestX-Legacy-${process.arch==='ia32'?'x86':'x64'}`;
const CSP="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";
function active(){return !!window&&!window.isDestroyed()&&window.isFocused()&&!window.isMinimized()&&window.isVisible()&&!dialogActive;}
function emit(name,payload){if(window&&!window.isDestroyed())window.webContents.send('desktop:event',name,payload);}
async function resync(){
  if(resyncRunning){resyncAgain=true;return;}
  resyncRunning=true;
  try {
    do {
      resyncAgain=false;
      await sessionRecovery.resync(command=>host.request(command),emit,()=>connectionRevision);
    } while(resyncAgain&&!quitting);
  }catch{ /* Logged-out sessions have no snapshots to restore. */ }
  finally{resyncRunning=false;}
}
function show(){if(window&&!window.isDestroyed()){window.show();window.restore();window.focus();}}
function trusted(event){return !!window&&!window.isDestroyed()&&event.sender===window.webContents&&event.senderFrame===window.webContents.mainFrame&&event.senderFrame.url.startsWith(ORIGIN+'/');}
function safeName(value){return typeof value==='string'?value.replace(/[\\/:*?"<>|\x00-\x1f]/g,'_').slice(0,180)||'download':'download';}
async function withDialog(action){if(dialogActive)throw new Error('请先完成当前文件选择');dialogActive=true;emit('window.foreground',false);try{return await action();}finally{dialogActive=false;emit('window.foreground',active());}}
async function importFiles(paths,scope){
  if(!Array.isArray(paths)||paths.length>20||paths.some(p=>typeof p!=='string'||!path.isAbsolute(p)))throw new Error('请选择本机文件');
  return host.request('import_files',{scope,paths});
}
async function invoke(command,input){
  const args=validateRequest(command,input??{});
  if(command==='open_external_url'){
    if(!mayOpenExternal(args.url))throw new Error('仅允许打开网页或邮件链接');
    await shell.openExternal(args.url);return null;
  }
  if(command==='pick_upload_files'){
    if(!active())throw new Error('请在当前窗口中选择附件');
    const scope=await host.request('selection_scope');
    const result=await withDialog(()=>dialog.showOpenDialog(window,{title:'选择附件',properties:['openFile','multiSelections']}));
    return result.canceled?[]:importFiles(result.filePaths,scope);
  }
  if(command==='read_clipboard_attachments'){
    if(!active())throw new Error('请在当前窗口中粘贴附件');
    const scope=await host.request('selection_scope');
    const files=await host.request('import_clipboard_files',{scope});
    if(files.length)return files;
    const image=clipboard.readImage();if(image.isEmpty())return [];
    const size=image.getSize();if(size.width*size.height>20_000_000)throw new Error('截图尺寸过大，请保存后选择文件');
    const png=image.toPNG();if(png.length>2*1024*1024)throw new Error('截图过大，请保存后选择文件');
    return host.request('import_clipboard_image',{scope,png:png.toString('base64')});
  }
  if(command==='pick_download_destination'){
    const scope=await host.request('selection_scope');
    const result=await withDialog(()=>dialog.showSaveDialog(window,{title:'保存文件',defaultPath:safeName(args.name),properties:['createDirectory']}));
    if(result.canceled||!result.filePath)return null;
    // Renderer receives only a one-use token, never authority to select arbitrary paths.
    return host.request('authorize_destination',{scope,path:result.filePath});
  }
  if(command==='export_audit'){
    if(!['csv','json'].includes(args.format))throw new Error('导出格式无效');
    const scope=await host.request('selection_scope');
    let result;
    try{result=await withDialog(()=>dialog.showSaveDialog(window,{title:'导出全部审计记录（请选择新文件名）',defaultPath:`linknestx-audit.${args.format}`,filters:[{name:args.format.toUpperCase(),extensions:[args.format]}]}));}
    catch(error){await host.request('cancel_audit_export',{exportId:args.exportId});throw error;}
    return host.request('export_audit',{...args,scope,destination:result.canceled?null:result.filePath});
  }
  if(command==='open_downloaded_file'||command==='reveal_downloaded_file'){
    // native host 反查路径并做存在性检查；这里仅执行系统打开动作。
    // 路径不回流渲染层（invoke 返回 null）。
    const result=await host.request(command,args);
    if(command==='open_downloaded_file'){
      const failure=await shell.openPath(result.path);
      if(failure)throw new Error(`无法打开文件：${failure}`);
    }else{
      shell.showItemInFolder(result.path);
    }
    return null;
  }
  if(command==='file_attachment_preview'||command==='file_attachment_thumb'||command==='check_downloaded_files'){
    return host.request(command,args);
  }
  if(command==='set_transfer_preferences'){
    const result=await host.request(command,args);
    if(process.platform==='win32')app.setLoginItemSettings({name:loginItemName,openAtLogin:!!args.preferences.autostart,path:process.execPath,args:['--hidden']});
    return result;
  }
  if(command==='login'||command==='logout'){
    const revision=sessionRecovery.beginChange();
    try{
      const result=await host.request(command,args);
      if(command==='login')sessionRecovery.completeLogin(result,revision);
      else sessionRecovery.completeLogout(revision);
      return command==='login'?rendererSession(result):result;
    }finally{sessionRecovery.finishChange(revision);void resync();}
  }
  const result=await host.request(command,args);
  return command==='login'?rendererSession(result):result;
}
async function shutdown(){
  if(quitting)return;quitting=true;
  if(host)await host.stop();
  tray?.destroy();app.quit();
}
if(!app.requestSingleInstanceLock()){app.quit();}
else{
 app.on('second-instance',show);
 app.on('before-quit',event=>{if(!quitting){event.preventDefault();void shutdown();}});
 app.on('window-all-closed',()=>{});
 app.whenReady().then(async()=>{
  if(process.platform==='win32'){
    const [major,minor,build]=os.release().split('.').map(Number);
    if(major<6||(major===6&&(minor<1||(minor===1&&build<7601)))){dialog.showErrorBox('系统版本不支持','兼容版至少需要 Windows 7 SP1。');app.quit();return;}
    app.setAppUserModelId('com.linknestx.legacy');
  }
  await fs.access(executable);await fs.access(path.join(uiRoot,'index.html'));
  session.defaultSession.setPermissionRequestHandler((_contents,_permission,callback)=>callback(false));
  session.defaultSession.setPermissionCheckHandler(()=>false);
  session.defaultSession.webRequest.onBeforeRequest((details,callback)=>callback({cancel:!details.url.startsWith(ORIGIN+'/')&&!details.url.startsWith('data:')}));
  protocol.registerBufferProtocol('linknestx-legacy',async(request,callback)=>{
    try{
      const filename=safeAsset(request.url,uiRoot);
      const real=await fs.realpath(filename),base=await fs.realpath(uiRoot);
      if(!real.startsWith(base+path.sep))throw new Error('资源路径无效');
      const ext=path.extname(filename).toLowerCase();
      const type={'.html':'text/html','.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.png':'image/png','.woff2':'font/woff2','.ico':'image/x-icon'}[ext]||'application/octet-stream';
      callback({statusCode:200,mimeType:type,charset:'utf-8',data:await fs.readFile(real),headers:{'Content-Security-Policy':CSP,'X-Content-Type-Options':'nosniff'}});
    }catch{callback({statusCode:404,data:Buffer.from('Not found')});}
  });
  host=new NativeHost(executable,path.join(app.getPath('userData'),'native'));
  host.on('event',(name,payload)=>{
    if(name==='connection.changed')connectionRevision++;
    if(name==='native.resync'){void resync();return;}
    if(name==='session.identity'){sessionRecovery.observe(payload);emit(name,payload);return;}
    emit(name,payload);
    const body=reduceNotice(name,payload,!active());
    if(body&&Notification.isSupported()){const notice=new Notification({title:'LinkNestX',body});notice.on('click',show);notice.show();}
  });
  host.on('unavailable',()=>{emit('service.error',{error:'后台服务已退出，请重新启动应用。'});});
  window=new BrowserWindow({title:'LinkNestX',width:1280,height:840,minWidth:820,minHeight:600,backgroundColor:'#f5f7fa',show:false,autoHideMenuBar:true,icon:path.join(appRoot,'icon.png'),webPreferences:{preload:path.join(__dirname,'preload.cjs'),contextIsolation:true,sandbox:true,nodeIntegration:false,webSecurity:true,allowRunningInsecureContent:false,webviewTag:false,devTools:!app.isPackaged}});
  window.webContents.setWindowOpenHandler(({url})=>{if(mayOpenExternal(url))void shell.openExternal(url);return {action:'deny'};});
  window.webContents.on('will-navigate',(event,url)=>{event.preventDefault();if(mayOpenExternal(url))void shell.openExternal(url);});
  window.webContents.on('will-attach-webview',event=>event.preventDefault());
  window.on('close',event=>{if(!quitting){event.preventDefault();window.hide();}});
  for(const event of ['focus','blur','minimize','restore','show','hide'])window.on(event,()=>emit('window.foreground',active()));
  ipcMain.handle('desktop:foreground',event=>{if(!trusted(event))throw new Error('窗口无权访问');return active();});
  ipcMain.handle('desktop:invoke',(event,command,args)=>{if(!trusted(event))throw new Error('窗口无权访问');return invoke(command,args);});
  ipcMain.on('desktop:trusted-drop',async(event,paths)=>{
    if(!trusted(event)||!active())return;
    try{const scope=await host.request('selection_scope');const attachments=await importFiles(paths,scope);emit('attachments.prepared',attachments);}catch(error){emit('service.error',{error:error.message});}
  });
  const iconPath=path.join(appRoot,'icon.png');
  tray=new Tray(nativeImage.createFromPath(iconPath));tray.setToolTip('LinkNestX');
  tray.setContextMenu(Menu.buildFromTemplate([{label:'打开 LinkNestX',click:show},{type:'separator'},{label:'退出',click:()=>void shutdown()}]));tray.on('double-click',show);
  window.once('ready-to-show',()=>{if(!process.argv.includes('--hidden'))show();});
  await window.loadURL(ORIGIN+'/index.html');
 }).catch(()=>{dialog.showErrorBox('无法启动 LinkNestX','应用文件不完整或后台服务启动失败，请重新安装与系统架构匹配的完整兼容包。');void shutdown();});
}

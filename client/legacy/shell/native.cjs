'use strict';
const {spawn}=require('node:child_process');
const {EventEmitter}=require('node:events');
const {MAX_REQUEST_BYTES}=require('./boundary.cjs');
const MAX_RESPONSE_BYTES=72*1024*1024;
class NativeHost extends EventEmitter {
  constructor(executable,dataRoot) {
    super();this.sequence=0;this.pending=new Map();this.buffer=Buffer.alloc(0);this.closed=false;
    this.child=spawn(executable,['--stdio',dataRoot],{stdio:['pipe','pipe','pipe'],windowsHide:true,shell:false,env:{...process.env,RUST_BACKTRACE:'0'}});
    this.child.stdout.on('data',chunk=>this.read(chunk));
    this.child.stdin.on('error',()=>this.fail(new Error('后台连接已关闭')));
    this.child.stdout.on('error',()=>this.fail(new Error('后台连接已关闭')));
    // Keep native stderr off the renderer; never log framed request bodies/credentials.
    this.child.stderr.on('data',()=>{});
    this.child.on('error',()=>this.fail(new Error('无法启动兼容版传输服务')));
    this.child.on('exit',()=>this.fail(new Error('后台服务已退出，请重新启动应用')));
  }
  read(chunk) {
    if(this.closed)return;
    if(this.buffer.length+chunk.length>MAX_RESPONSE_BYTES+4){this.fail(new Error('后台响应超过限制'));this.child.kill();return;}
    this.buffer=Buffer.concat([this.buffer,chunk]);
    while(this.buffer.length>=4){
      const size=this.buffer.readUInt32LE(0);
      if(size===0||size>MAX_RESPONSE_BYTES){this.fail(new Error('后台响应格式无效'));this.child.kill();return;}
      if(this.buffer.length<size+4)return;
      const body=this.buffer.subarray(4,size+4);this.buffer=this.buffer.subarray(size+4);
      let value;try{value=JSON.parse(body.toString('utf8'));}catch{this.fail(new Error('后台响应格式无效'));this.child.kill();return;}
      if(!value||typeof value!=='object'||Array.isArray(value)){this.fail(new Error('后台响应格式无效'));return;}
      if(value.event&&typeof value.event==='string'){this.emit('event',value.event,value.payload);continue;}
      const pending=this.pending.get(value.id);if(!pending)continue;
      this.pending.delete(value.id);clearTimeout(pending.timer);
      if(value.error)pending.reject(new Error(value.error.error||'桌面操作失败'));else pending.resolve(value.result);
    }
  }
  request(command,args={}) {
    if(this.closed)return Promise.reject(new Error('后台服务不可用'));
    if(this.pending.size>=32)return Promise.reject(new Error('桌面操作繁忙，请稍后重试'));
    const id=++this.sequence;const payload=Buffer.from(JSON.stringify({id,command,args}));
    if(payload.length>MAX_REQUEST_BYTES)return Promise.reject(new Error('请求超过限制'));
    const frame=Buffer.allocUnsafe(payload.length+4);frame.writeUInt32LE(payload.length);payload.copy(frame,4);
    return new Promise((resolve,reject)=>{
      const timer=setTimeout(()=>{this.pending.delete(id);reject(new Error('桌面操作超时，请检查任务状态'));},command==='export_audit'||command.includes('attachment_upload')?30*60*1000:120000);
      this.pending.set(id,{resolve,reject,timer});
      this.child.stdin.write(frame,error=>{if(error){clearTimeout(timer);this.pending.delete(id);reject(new Error('后台连接已关闭'));}});
    });
  }
  fail(error){if(this.closed)return;this.closed=true;for(const pending of this.pending.values()){clearTimeout(pending.timer);pending.reject(error);}this.pending.clear();if(this.child.exitCode===null&&!this.child.killed)this.child.kill();this.emit('unavailable',error);}
  async stop(){
    if(this.closed)return;
    const payload=Buffer.from(JSON.stringify({id:++this.sequence,command:'shutdown'}));const frame=Buffer.alloc(payload.length+4);frame.writeUInt32LE(payload.length);payload.copy(frame,4);this.child.stdin.end(frame);
    await new Promise(resolve=>{const timeout=setTimeout(()=>{this.child.kill();resolve();},15000);this.child.once('exit',()=>{clearTimeout(timeout);resolve();});});
  }
}
module.exports={NativeHost};

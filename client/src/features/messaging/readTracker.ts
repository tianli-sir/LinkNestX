/** Tracks individual visible message IDs, never a sequence frontier. */
export class VisibleReadTracker {
 private enabled=false;private disposed=false;private visibleIds=new Set<string>();private confirmed=new Set<string>();private shedIds=new Set<string>();private pending=new Set<string>();private attempts=new Map<string,number>();private inFlight=new Set<string>();private timers=new Map<string,ReturnType<typeof setTimeout>>();private flushTimer?:ReturnType<typeof setTimeout>;
 constructor(private send:(ids:string[])=>Promise<string[]>){ }
 setEnabled(enabled:boolean){if(this.disposed)return;if(this.enabled===enabled)return;this.enabled=enabled;this.timers.forEach(clearTimeout);this.timers.clear();this.pending.clear();if(enabled){this.attempts.clear();this.visibleIds.forEach(id=>this.schedule(id));}}
 visible(id:string,visible:boolean){if(this.disposed)return;if(visible){if(this.shedIds.has(id))return;const wasVisible=this.visibleIds.has(id);this.visibleIds.add(id);if(!wasVisible)this.attempts.delete(id);this.schedule(id);}else{this.visibleIds.delete(id);const timer=this.timers.get(id);if(timer)clearTimeout(timer);this.timers.delete(id);this.pending.delete(id);}}
 private schedule(id:string){if(!this.enabled||this.confirmed.has(id)||this.timers.has(id)||this.pending.has(id)||this.inFlight.has(id))return;this.timers.set(id,setTimeout(()=>{this.timers.delete(id);if(!this.enabled||!this.visibleIds.has(id))return;this.pending.add(id);if(!this.flushTimer)this.flushTimer=setTimeout(()=>{this.flushTimer=undefined;void this.flush();},0);},400));}
 private isBadRequest(error:unknown){return !!error&&typeof error==='object'&&(error as {status?:unknown}).status===400}
 private async flush(){if(this.disposed||!this.enabled)return;const ids=[...this.pending];this.pending.clear();for(let i=0;i<ids.length;i+=100){if(this.disposed||!this.enabled)return;const batch=ids.slice(i,i+100).filter(id=>this.visibleIds.has(id));if(!batch.length)continue;await this.deliver(batch);}}
 private async deliver(batch:string[]){batch.forEach(id=>this.inFlight.add(id));try{const confirmed=await this.send(batch);if(!this.disposed)confirmed.forEach(id=>this.confirmed.add(id));}catch(error){if(this.disposed)return;if(this.isBadRequest(error)){
  // 服务端整批校验：先原样重试一次排除瞬态 400，再二分定位并剔除毒丸 ID。
  try{const confirmed=await this.send(batch);if(!this.disposed){confirmed.forEach(id=>this.confirmed.add(id));return}}catch{ /* 继续隔离 */ }
  await this.isolate(batch);return;
 }if(this.enabled)this.scheduleRetries(batch);}finally{batch.forEach(id=>this.inFlight.delete(id));}}
 private async isolate(batch:string[]){if(batch.length===1){const id=batch[0];this.visibleIds.delete(id);this.pending.delete(id);this.timers.delete(id);this.attempts.delete(id);this.shedIds.add(id);return}const half=Math.ceil(batch.length/2);await this.deliver(batch.slice(0,half));await this.deliver(batch.slice(half));}
 private scheduleRetries(batch:string[]){for(const id of batch){const attempts=(this.attempts.get(id)??0)+1;this.attempts.set(id,attempts);if(this.visibleIds.has(id)){const delay=attempts<=3?Math.min(4000,1000*2**(attempts-1)):30000;this.timers.set(id,setTimeout(()=>{this.timers.delete(id);this.schedule(id)},delay));}}}
 dispose(){this.disposed=true;this.timers.forEach(clearTimeout);if(this.flushTimer)clearTimeout(this.flushTimer);this.timers.clear();this.pending.clear();this.visibleIds.clear();}
}

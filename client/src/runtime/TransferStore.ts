import type { Transfer } from '../features/TransfersView';
export class TransferStore {
 private value:{tasks:Transfer[];error:string;loading:boolean}={tasks:[],error:'',loading:true};private listeners=new Set<()=>void>();private bindings=new Map<string,{messageId:string;groupId:string}>();
 getSnapshot=()=>this.value;
 subscribe=(listener:()=>void)=>{this.listeners.add(listener);return()=>this.listeners.delete(listener);};
 private emit(){this.listeners.forEach(listener=>listener());}
 bind(id:string,messageId:string,groupId:string){this.bindings.set(id,{messageId,groupId});const task=this.value.tasks.find(t=>t.id===id);if(task)this.upsert({...task,messageId,groupId});}
 upsert(task:Transfer){task={...task,...this.bindings.get(task.id)};const old=this.value.tasks.find(t=>t.id===task.id);if(old?.metrics?.revision!==undefined&&(task.metrics?.revision??-1)<old.metrics.revision)return;this.value={...this.value,tasks:[...this.value.tasks.filter(t=>t.id!==task.id),task]};this.emit();}
 replace(tasks:Transfer[]){const merged=new Map(this.value.tasks.map(task=>[task.id,task]));for(let task of tasks){task={...task,...this.bindings.get(task.id)};const old=merged.get(task.id);if(!old||(task.metrics?.revision??0)>=(old.metrics?.revision??0))merged.set(task.id,task);}this.value={tasks:[...merged.values()],loading:false,error:''};this.emit();}
 status(loading:boolean,error=''){this.value={...this.value,loading,error};this.emit();}
 clear(){this.bindings.clear();this.value={tasks:[],loading:false,error:''};this.emit();}
}

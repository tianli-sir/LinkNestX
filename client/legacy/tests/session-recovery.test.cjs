'use strict';
const test=require('node:test');const assert=require('node:assert/strict');
const {createNoticeReducer}=require('../shell/notices.cjs');
function recovery(){
 let create;try{create=require('../shell/session-recovery.cjs').createSessionRecovery}catch(error){if(error.code!=='MODULE_NOT_FOUND')throw error}
 assert.equal(typeof create,'function','shell must recover identity without depending on best-effort events');
 const reduce=createNoticeReducer();return {reduce,state:create(reduce)};
}
const message=(id,senderId)=>({type:'message.created',message:{id,senderId}});
test('trusted login completion restores sender suppression when identity event was dropped',()=>{
 const {reduce,state}=recovery();const revision=state.beginChange();
 state.completeLogin({id:'session-a',user:{id:'alice'}},revision);
 assert.equal(reduce('server.event',message('mine','alice'),true),null);
 assert.equal(reduce('server.event',message('theirs','bob'),true),'收到新消息，请打开 LinkNestX 查看。');
});
test('queued identity events cannot revive a previous account while logout is pending',()=>{
 const {reduce,state}=recovery();state.observe({userId:'alice'});
 const revision=state.beginChange();state.observe({userId:'alice'});
 assert.equal(reduce('server.event',message('queued','bob'),true),null);
 state.completeLogout(revision);
 assert.equal(reduce('server.event',message('logged-out','bob'),true),null);
});
test('late login response cannot restore an identity after a newer logout starts',()=>{
 const {reduce,state}=recovery();const login=state.beginChange();const logout=state.beginChange();
 state.completeLogin({id:'old',user:{id:'alice'}},login);
 assert.equal(reduce('server.event',message('stale','alice'),true),null);
 state.completeLogout(logout);
 state.observe({userId:'bob'});
 assert.equal(reduce('server.event',message('bob-own','bob'),true),null);
 assert.equal(reduce('server.event',message('alice-other','alice'),true),'收到新消息，请打开 LinkNestX 查看。');
});
test('scope checked resync restores missing identity without clearing same-user dedupe',async()=>{
 const {reduce,state}=recovery();const events=[];
 const request=async command=>({session_identity:{userId:'alice',scope:'alice:session-a'},list_transfers:[],get_background_status:{},get_connection_status:{status:'connected'}}[command]);
 assert.equal(await state.resync(request,(...event)=>events.push(event),()=>0),true);
 assert.equal(reduce('server.event',message('mine','alice'),true),null);
 assert.equal(reduce('server.event',message('other','bob'),true),'收到新消息，请打开 LinkNestX 查看。');
 await state.resync(request,()=>{},()=>0);
 assert.equal(reduce('server.event',message('other','bob'),true),null);
 assert(events.some(([name,payload])=>name==='session.identity'&&payload.userId==='alice'));
});
test('snapshot crossing a session change cannot overwrite the current identity or connection',async()=>{
 const {reduce,state}=recovery();const events=[];let reads=0;
 const request=async command=>command==='session_identity'?(++reads===1?{scope:'old',userId:'alice'}:{scope:'new',userId:'bob'}):command==='list_transfers'?[]:{};
 state.observe({userId:'bob'});
 assert.equal(await state.resync(request,(...event)=>events.push(event),()=>0),false);
 assert.equal(events.length,0);assert.equal(reduce('server.event',message('mine','bob'),true),null);
});
test('logged-out snapshot clears identity and skips session-only requests',async()=>{
 const {reduce,state}=recovery();state.observe({userId:'alice'});const calls=[];
 const request=async command=>{calls.push(command);assert.equal(command,'session_identity');return {scope:null,userId:null}};
 await state.resync(request,()=>{},()=>0);
 assert.equal(reduce('server.event',message('after-logout','alice'),true),null);
 assert.equal(calls.length,2);
});
test('a newer connection event wins over a resync connection snapshot',async()=>{
 const {state}=recovery();const events=[];let revision=1;let finish;
 const connection=new Promise(resolve=>{finish=resolve});
 const request=async command=>{if(command==='session_identity')return {scope:'current',userId:'alice'};if(command==='list_transfers')return [];if(command==='get_connection_status')return connection;return {}};
 const syncing=state.resync(request,(...event)=>events.push(event),()=>revision);
 await new Promise(resolve=>setImmediate(resolve));
 revision++;
 finish({status:'connected'});
 await syncing;
 assert.equal(events.some(([name])=>name==='connection.changed'),false);
});
test('failed login can recover the previous trusted identity with a fresh scope snapshot',async()=>{
 const {reduce,state}=recovery();state.observe({userId:'alice'});const revision=state.beginChange();
 state.finishChange(revision);
 const request=async command=>command==='session_identity'?{scope:'alice:existing',userId:'alice'}:command==='list_transfers'?[]:{};
 assert.equal(await state.resync(request,()=>{},()=>0),true);
 assert.equal(reduce('server.event',message('self','alice'),true),null);
});

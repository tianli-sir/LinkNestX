'use strict';
function createNoticeReducer(){
 let userId=null;const seen=new Set();
 return (name,payload,hidden)=>{
  if(name==='session.identity'){const next=payload?.userId??null;if(next!==userId)seen.clear();userId=next;return null;}
  if(name==='server.event'&&!userId)return null;
  let key,body;
  if(name==='transfer.updated'&&['completed','failed'].includes(payload?.state)){key='transfer:'+payload.id+':'+payload.state;body=payload.state==='completed'?'文件传输已完成。':'传输需要处理，请打开应用查看。';}
  if(name==='server.event'&&payload?.type==='message.created'&&payload.message?.id&&payload.message.senderId!==userId){key='message:'+payload.message.id;body=payload.message.kind==='system'?'群组动态已更新，请打开 LinkNestX 查看。':'收到新消息，请打开 LinkNestX 查看。';}
  if(name==='server.event'&&payload?.type==='contacts.changed'){key='contacts:'+(payload.eventId||Math.floor(Date.now()/5000));body='好友申请或联系人状态已更新，请打开 LinkNestX 查看。';}
  if(!key||seen.has(key))return null;
  seen.add(key);if(seen.size>256)seen.delete(seen.values().next().value);
  return hidden?body:null;
 };
}
module.exports={createNoticeReducer};

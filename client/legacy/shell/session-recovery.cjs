'use strict';

function createSessionRecovery(reduceNotice){
 let revision=0,pending=false;
 function apply(userId){reduceNotice('session.identity',{userId:typeof userId==='string'&&userId?userId:null},false)}
 return {
  beginChange(){revision++;pending=true;apply(null);return revision},
  completeLogin(session,expected){if(revision===expected){pending=false;apply(session?.user?.id)}},
  completeLogout(expected){if(revision===expected){pending=false;apply(null)}},
  finishChange(expected){if(revision===expected)pending=false},
  observe(identity){if(!pending){revision++;apply(identity?.userId)}},
  async resync(request,emit,connectionRevision){
   const expected=revision;
   const identity=await request('session_identity');
   const snapshotRevision=connectionRevision();
   const [tasks,status,connection]=identity.scope
    ?await Promise.all([request('list_transfers'),request('get_background_status'),request('get_connection_status')])
    :[[],null,null];
   const current=await request('session_identity');
   if(pending||revision!==expected||identity.scope!==current.scope||identity.userId!==current.userId)return false;
   apply(current.userId);
   emit('session.identity',{userId:current.userId});
   if(current.scope){
    for(const task of tasks)emit('transfer.updated',task);
    emit('service.status',status);
    emit('server.event',{type:'session.ready',localResync:true});
    if(connectionRevision()===snapshotRevision)emit('connection.changed',connection);
   }
   return true;
  }
 };
}
module.exports={createSessionRecovery};

import { useState, type FormEvent } from 'react';
import { useAuth } from '../auth';
import { apiErrorMessage } from '../api/rest';
import { ErrorNotice, Field } from './shared';
import '../styles/accounts.css';

export function PasswordSettings(){
 const {changePassword}=useAuth();
 const [current,setCurrent]=useState('');const [next,setNext]=useState('');const [confirm,setConfirm]=useState('');
 const [busy,setBusy]=useState(false);const [error,setError]=useState('');const [notice,setNotice]=useState('');
 async function submit(event:FormEvent){
  event.preventDefault();if(busy)return;setError('');setNotice('');
  if(next!==confirm){setError('两次输入的新密码不一致。');return}
  if(Array.from(next).length<8 || new TextEncoder().encode(next).length>1024){setError('新密码需至少 8 个字符，且不超过 1024 字节。');return}
  if(current===next){setError('新密码不能与当前密码相同。');return}
  setBusy(true);
  try {
   await changePassword(current,next);setCurrent('');setNext('');setConfirm('');
   setNotice('密码已更新，当前设备保持登录。');
   window.dispatchEvent(new CustomEvent('linknestx:notice',{detail:{kind:'success',title:'密码已更新',message:'当前设备保持登录，其他设备需要重新登录。'}}));
  }catch(cause){
   const message=apiErrorMessage(cause);
   const safe=message==='invalid credentials'?'当前密码不正确，请重新输入。':message==='unauthorized'||message==='session is revoked'||message==='session is expired'?'登录状态已失效，请重新登录。':'密码修改失败，请稍后重试。';
   setError(safe);window.dispatchEvent(new CustomEvent('linknestx:notice',{detail:{kind:'error',title:'密码修改失败',message:safe}}));
  }finally{setBusy(false)}
 }
 return <form className="ln-settings-form ln-password-settings" onSubmit={submit} aria-label="修改密码"><h3>修改密码</h3><p className="ln-muted">更新后，当前设备保持登录，其他设备需要使用新密码重新登录。</p><ErrorNotice error={error}/>{notice&&<p role="status" className="ln-notice">{notice}</p>}<div className="ln-password-fields"><Field label="当前密码" id="settings-current-password"><input id="settings-current-password" type="password" required autoComplete="current-password" value={current} disabled={busy} onChange={event=>setCurrent(event.target.value)}/></Field><Field label="新密码" id="settings-new-password"><input id="settings-new-password" type="password" required autoComplete="new-password" aria-describedby="settings-password-hint" value={next} disabled={busy} onChange={event=>setNext(event.target.value)}/></Field><Field label="确认新密码" id="settings-confirm-password"><input id="settings-confirm-password" type="password" required autoComplete="new-password" value={confirm} disabled={busy} onChange={event=>setConfirm(event.target.value)}/></Field></div><p className="ln-field-hint" id="settings-password-hint">至少 8 个字符，请设置与当前密码不同的新密码。</p><button className="ln-button ln-button-primary" disabled={busy}>{busy?'正在更新…':'保存新密码'}</button></form>;
}

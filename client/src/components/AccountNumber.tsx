import { useState } from 'react';
import { Copy } from '@phosphor-icons/react';
import { copyAccountNumber } from '../lib/browserPrimitives';
import '../styles/accounts.css';

export function AccountNumber({number,compact=false}:{number?:string;compact?:boolean}) {
 const [notice,setNotice]=useState('');
 const [busy,setBusy]=useState(false);
 if(!number)return null;
 async function copy(){
  if(busy)return;setBusy(true);setNotice('');
  try {
   await copyAccountNumber(number!);setNotice('账号已复制');
   window.dispatchEvent(new CustomEvent('linknestx:notice',{detail:{kind:'success',title:'账号已复制'}}));
  } catch {
   setNotice('暂时无法复制，请选择账号后手动复制。');
   window.dispatchEvent(new CustomEvent('linknestx:notice',{detail:{kind:'info',title:'请手动复制账号',message:'请选择显示的数字账号后复制。'}}));
  } finally {setBusy(false)}
 }
 return <div className={compact?'ln-own-number is-compact':'ln-own-number'}><span className="ln-own-number-label">我的数字账号</span><div className="ln-own-number-value"><strong>{number}</strong><button type="button" className="ln-button ln-button-secondary" aria-label="复制我的账号" disabled={busy} onClick={()=>void copy()}><Copy size={17} aria-hidden="true"/><span>复制</span></button></div>{notice&&<span className="ln-own-number-notice" role="status">{notice}</span>}</div>;
}

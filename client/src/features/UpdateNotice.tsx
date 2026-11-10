import { useEffect, useRef, useState } from 'react';
import { useDesktop, useDesktopEvent } from '../runtime/DesktopProvider';

interface NoticeStatus { state: string; version?: string }

/** Surface background completion while leaving the restart decision to the user. */
export function UpdateNotice({ onOpen }: { onOpen: () => void }) {
  const { desktop, capabilities } = useDesktop();
  const [status, setStatus] = useState<NoticeStatus>();
  const [dismissed, setDismissed] = useState<string>();
  const revision = useRef(0);
  useEffect(() => {
    const generation = ++revision.current;
    if (capabilities.updates) {
      void desktop.invoke<NoticeStatus>('get_update_status').then(value => {
        if (generation === revision.current) setStatus(value);
      }).catch(() => {});
    }
    return () => { revision.current++; };
  }, [desktop, capabilities.updates]);
  useDesktopEvent('update.status', (value: NoticeStatus) => {
    revision.current++;
    setStatus(value);
  });
  if (!capabilities.updates || !status?.version || status.version === dismissed || !['ready', 'deferred'].includes(status.state)) return null;
  return <div className="im-web-banner im-update-notice" role="status">
    <span>新版本 {status.version} 已准备好</span>
    <button className="ln-text-button" onClick={onOpen}>查看更新</button>
    <button aria-label="稍后查看更新" onClick={() => setDismissed(status.version)}>×</button>
  </div>;
}

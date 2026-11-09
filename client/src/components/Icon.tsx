import { useId, type SVGProps } from "react";

const paths = {
  nest: <><path d="m4 8 8-5 8 5v10a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2Z" /><path d="m8 10 4 3 4-3M8 14l4 3 4-3" /></>,
  groups: <><circle cx="9" cy="8" r="3" /><path d="M3 20v-2a6 6 0 0 1 12 0v2M16 5a3 3 0 0 1 0 6M18 14a5 5 0 0 1 3 4v2" /></>,
  folder: <path d="M3 7V5a2 2 0 0 1 2-2h5l3 4h6a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z" />,
  transfers: <><path d="M7 3v16m-4-4 4 4 4-4M17 21V5m-4 4 4-4 4 4" /></>,
  admin: <><path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6Z" /><path d="m8 12 3 3 5-5" /></>,
  settings: <><path d="m10 3-1 3-3 1-3 3v4l3 3 3 1 1 3h4l1-3 3-1 3-3v-4l-3-3-3-1-1-3Z" /><circle cx="12" cy="12" r="3" /></>,
  logout: <><path d="M9 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h4M13 7l5 5-5 5M8 12h13" /></>,
  chevron: <path d="m7 10 5 5 5-5" />,
  server: <><rect x="3" y="3" width="18" height="7" rx="2" /><rect x="3" y="14" width="18" height="7" rx="2" /><path d="M7 6.5h.01M7 17.5h.01M11 6.5h6M11 17.5h6" /></>,
  lock: <><rect x="5" y="10" width="14" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3" /></>,
  info: <><circle cx="12" cy="12" r="9" /><path d="M12 11v6M12 7h.01" /></>,
  connected: <><path d="M2 8a16 16 0 0 1 20 0M5 11a11 11 0 0 1 14 0M8 14a6 6 0 0 1 8 0" /><circle cx="12" cy="18" r="1" /></>,
  disconnected: <><path d="m3 3 18 18M2 8a16 16 0 0 1 3-2M9 5a16 16 0 0 1 13 3M5 11a11 11 0 0 1 3-2M14 9a11 11 0 0 1 5 2M8 14a6 6 0 0 1 7-1" /><circle cx="12" cy="18" r="1" /></>,
} as const;

export type IconName = keyof typeof paths;
export interface IconProps extends Omit<SVGProps<SVGSVGElement>, "name"> { name: IconName; size?: number; title?: string }

export function Icon({ name, size = 20, title, ...props }: IconProps) {
  const titleId = useId();
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden={title ? undefined : true} role={title ? "img" : undefined} aria-labelledby={title ? titleId : undefined} focusable="false" {...props}>
    {title && <title id={titleId}>{title}</title>}{paths[name]}
  </svg>;
}

import { MotionConfig } from 'framer-motion';
import type { ReactNode } from 'react';
/** Test builds render final states directly: jsdom gives no layout/rAF timing
 * guarantees and vitest does not load CSS, so initial/exit values are gated
 * off to keep role/visibility assertions deterministic. */
export const MOTION_ENABLED = import.meta.env.MODE !== 'test';
export const DURATION = 0.18;
export const EASE: 'easeOut' = 'easeOut';
export const STAGGER = 0.02;
export const STAGGER_CAP = 8;
/** View-level enter (page switch, modal shade); false in tests renders the final state at once. */
export const viewInitial = MOTION_ENABLED ? { opacity: 0, y: 8 } : false;
/** Exit fade shared by every AnimatePresence usage; undefined removes instantly in tests. */
export const exitFade = MOTION_ENABLED ? { opacity: 0 } : undefined;
/** Staggered list enter, capped so long lists render at full speed after the first rows. */
export function listInitial(index: number) { return MOTION_ENABLED && index < STAGGER_CAP ? { opacity: 0, y: 8, transition: { delay: index * STAGGER, duration: DURATION, ease: EASE } } : false; }
/** Swipe-to-delete gesture threshold: strictly past -64px on release. */
export const SWIPE_THRESHOLD = -64;
export const swipeDeleteTriggered = (offsetX: number) => offsetX < SWIPE_THRESHOLD;
export function MotionProvider({ children }: { children: ReactNode }) { return <MotionConfig reducedMotion="user" transition={{ duration: DURATION, ease: EASE }}>{children}</MotionConfig>; }

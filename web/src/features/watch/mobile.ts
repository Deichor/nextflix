"use client";

import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';

// Phone / tablet helpers for the player. Everything here is a no-op on desktop (fine pointer).

const TOUCH_QUERY = '(hover: none) and (pointer: coarse)';

/** True on touch-first devices (phones, tablets): the player shows its touch layout. */
export function useTouchUi(): boolean {
  const [touch, setTouch] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia(TOUCH_QUERY);
    const update = () => setTouch(mq.matches);
    update();
    mq.addEventListener('change', update);
    return () => mq.removeEventListener('change', update);
  }, []);
  return touch;
}

/** Keeps the screen on while `active` (Wake Lock API; re-acquired when the page is shown again). */
export function useWakeLock(active: boolean): void {
  useEffect(() => {
    const wl = (navigator as Navigator & { wakeLock?: { request(type: 'screen'): Promise<{ release(): Promise<void> }> } }).wakeLock;
    if (!active || !wl) return;
    let lock: { release(): Promise<void> } | null = null;
    let stopped = false;
    const acquire = () => {
      if (document.visibilityState !== 'visible' || lock) return;
      wl.request('screen').then(l => {
        if (stopped) l.release().catch(() => {});
        else {
          lock = l;
          // The browser drops the lock when the page is hidden
          (l as unknown as EventTarget).addEventListener?.('release', () => { if (lock === l) lock = null; });
        }
      }).catch(() => {});
    };
    acquire();
    document.addEventListener('visibilitychange', acquire);
    return () => {
      stopped = true;
      document.removeEventListener('visibilitychange', acquire);
      lock?.release().catch(() => {});
      lock = null;
    };
  }, [active]);
}

/** Edge-to-edge while the player is open: adds viewport-fit=cover (safe-area insets apply), restored on close. */
export function useViewportFitCover(active: boolean): void {
  useEffect(() => {
    if (!active) return;
    const meta = document.querySelector<HTMLMetaElement>('meta[name="viewport"]');
    if (!meta) return;
    const prev = meta.content;
    if (/viewport-fit\s*=\s*cover/.test(prev)) return;
    meta.content = `${prev.replace(/,?\s*viewport-fit\s*=\s*\w+/, '')}, viewport-fit=cover`;
    return () => { meta.content = prev; };
  }, [active]);
}

/** Stops page bounce / pull-to-refresh under the player: touchmove is cancelled outside [data-scroll] lists. */
export function useNoBounce(ref: RefObject<HTMLElement | null>, active: boolean): void {
  useEffect(() => {
    const el = ref.current;
    if (!active || !el) return;
    const onMove = (e: TouchEvent) => {
      const target = e.target as Element | null;
      if (target?.closest?.('[data-scroll]')) return;
      if (e.cancelable) e.preventDefault();
    };
    el.addEventListener('touchmove', onMove, { passive: false });
    return () => el.removeEventListener('touchmove', onMove);
  }, [ref, active]);
}

type FsDocument = Document & { webkitFullscreenElement?: Element | null; webkitFullscreenEnabled?: boolean; webkitExitFullscreen?: () => Promise<void> | void };
type FsElement = HTMLElement & { webkitRequestFullscreen?: () => Promise<void> | void };
type LockableOrientation = ScreenOrientation & { lock?: (o: 'landscape' | 'portrait') => Promise<void> };

const fsElement = () => document.fullscreenElement ?? (document as FsDocument).webkitFullscreenElement ?? null;

/** Element fullscreen works here (not on iPhone Safari, which only has the native video player). */
export const fullscreenSupported = () =>
  typeof document !== 'undefined' && !!(document.fullscreenEnabled || (document as FsDocument).webkitFullscreenEnabled);

const orientation = () => (typeof screen !== 'undefined' ? (screen.orientation as LockableOrientation | undefined) : undefined);

async function requestFs(el: FsElement): Promise<boolean> {
  try {
    if (el.requestFullscreen) await el.requestFullscreen({ navigationUI: 'hide' });
    else if (el.webkitRequestFullscreen) await el.webkitRequestFullscreen();
    else return false;
    return true;
  } catch {
    return false;
  }
}

async function exitFs(): Promise<void> {
  const d = document as FsDocument;
  try {
    if (d.exitFullscreen) await d.exitFullscreen();
    else await d.webkitExitFullscreen?.();
  } catch {
    /* already out */
  }
}

async function lockTo(o: 'landscape' | 'portrait'): Promise<boolean> {
  const so = orientation();
  if (!so?.lock) return false;
  try {
    await so.lock(o);
    return true;
  } catch {
    return false;
  }
}

/**
 * Mobile fullscreen of the player container (not the <video>: our subtitles, controls and the hidden
 * seamless-switch slot live next to it). Where element fullscreen is missing (iPhone) the fixed,
 * full-viewport container is the fullscreen. Landscape is locked where the browser allows it.
 */
export function useMobileFullscreen(ref: RefObject<HTMLElement | null>, touchUi: boolean) {
  const [supported, setSupported] = useState(false);
  const [canRotate, setCanRotate] = useState(false);

  useEffect(() => {
    const ok = fullscreenSupported();
    setSupported(ok);
    setCanRotate(ok && !!orientation()?.lock);
  }, []);

  const enter = useCallback(async () => {
    const el = ref.current as FsElement | null;
    if (!el) return false;
    if (fsElement() !== el && !(await requestFs(el))) return false;
    // Films are wide: sideways, as Netflix does
    lockTo('landscape');
    return true;
  }, [ref]);

  const exit = useCallback(async () => {
    orientation()?.unlock?.();
    if (fsElement()) await exitFs();
  }, []);

  const toggle = useCallback(() => {
    if (fsElement()) exit();
    else enter();
  }, [enter, exit]);

  // Portrait <-> landscape; enters fullscreen first (Chrome locks only in fullscreen)
  const rotate = useCallback(async () => {
    if (!fsElement() && !(await enter())) return;
    const landscapeNow = (orientation()?.type || '').startsWith('landscape') || window.innerWidth > window.innerHeight;
    if (!(await lockTo(landscapeNow ? 'portrait' : 'landscape'))) setCanRotate(false);
  }, [enter]);

  // Opening the player on a phone goes fullscreen (the tap that opened it still counts as a user
  // gesture); if the browser refused, the first tap inside the player tries once more
  const autoTried = useRef(false);
  useEffect(() => {
    if (!touchUi || !fullscreenSupported() || autoTried.current) return;
    autoTried.current = true;
    const el = ref.current;
    enter().then(ok => {
      if (ok || !el) return;
      const retry = () => { enter(); };
      el.addEventListener('touchend', retry, { once: true });
    });
  }, [touchUi, enter, ref]);

  // Closing the player leaves fullscreen and frees the orientation
  useEffect(() => {
    // Read now: React clears the ref before this cleanup runs
    const el = ref.current;
    return () => {
      orientation()?.unlock?.();
      if (el && fsElement() === el) exitFs();
    };
  }, [ref]);

  return { supported, canRotate, toggle, rotate };
}

import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import type { MediaType } from '../lib/catalog';
import type { Profile } from '../lib/profile';
import { focusById, focusDefault, onFocusChange } from './spatial';

export type Tab = 'home' | 'search' | 'movies' | 'tv' | 'settings';

export type Route =
  | { name: 'setup' }
  | { name: 'profiles' }
  | { name: 'addProfile'; count: number }
  | { name: 'editProfile'; profile: Profile }
  | { name: 'surprise' }
  | { name: Tab }
  | { name: 'detail'; id: number; type: MediaType; autoplay?: boolean };

export interface Entry {
  key: number;
  route: Route;
  /** Focused element when the screen was left; restored when it comes back */
  focusId?: string;
  /** Screen state that survives a remount (search text, chosen season...) */
  state: Record<string, unknown>;
}

interface RouterApi {
  stack: Entry[];
  push: (route: Route) => void;
  replace: (route: Route) => void;
  reset: (route: Route) => void;
  /** false when already at the root */
  back: () => boolean;
}

const RouterContext = createContext<RouterApi | null>(null);
const EntryContext = createContext<Entry | null>(null);

let nextKey = 1;
const entry = (route: Route): Entry => ({ key: nextKey++, route, state: {} });

export function RouterProvider({ initial, children }: { initial: Route; children: React.ReactNode }) {
  const [stack, setStack] = useState<Entry[]>(() => [entry(initial)]);
  const stackRef = useRef(stack);
  stackRef.current = stack;

  // The top screen remembers its focused element (overlays such as the player are skipped)
  useEffect(() => onFocusChange(el => {
    const id = el.getAttribute('data-nav-id');
    const top = stackRef.current[stackRef.current.length - 1];
    if (id && top && !el.closest('[data-nav-layer]')) top.focusId = id;
  }), []);

  const push = useCallback((route: Route) => setStack(s => [...s, entry(route)]), []);
  const replace = useCallback((route: Route) => setStack(s => [...s.slice(0, -1), entry(route)]), []);
  const reset = useCallback((route: Route) => setStack([entry(route)]), []);
  const back = useCallback(() => {
    if (stackRef.current.length <= 1) return false;
    setStack(s => s.slice(0, -1));
    return true;
  }, []);

  const api = useMemo(() => ({ stack, push, replace, reset, back }), [stack, push, replace, reset, back]);
  return <RouterContext.Provider value={api}>{children}</RouterContext.Provider>;
}

export function useRouter(): RouterApi {
  const r = useContext(RouterContext);
  if (!r) throw new Error('RouterProvider missing');
  return r;
}

export function EntryProvider({ value, children }: { value: Entry; children: React.ReactNode }) {
  return <EntryContext.Provider value={value}>{children}</EntryContext.Provider>;
}

export function useEntry(): Entry {
  const e = useContext(EntryContext);
  if (!e) throw new Error('EntryProvider missing');
  return e;
}

/** State kept on the history entry, so it is back after Back from a detail page. */
export function useEntryState<T>(key: string, initial: T): [T, (v: T) => void] {
  const e = useEntry();
  const [value, setValue] = useState<T>(() => (key in e.state ? (e.state[key] as T) : initial));
  const set = useCallback((v: T) => {
    e.state[key] = v;
    setValue(v);
  }, [e, key]);
  return [value, set];
}

/**
 * Focus when the screen is ready: the element focused when it was left (Back), else
 * `fallbackId`, else the screen's default. Runs once.
 */
export function useFocusRestore(ready: boolean, fallbackId?: string): void {
  const e = useEntry();
  const done = useRef(false);
  useEffect(() => {
    if (!ready || done.current) return;
    const raf = requestAnimationFrame(() => {
      done.current = true;
      if (focusById(e.focusId) || focusById(fallbackId)) return;
      focusDefault();
    });
    return () => cancelAnimationFrame(raf);
  }, [ready, e, fallbackId]);
}

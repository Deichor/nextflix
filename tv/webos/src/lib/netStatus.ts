import { useEffect, useState } from 'react';
import { base } from './http';

/** online; offline: the server has no internet; unreachable: the TV cannot reach the server */
export type NetState = 'online' | 'offline' | 'unreachable';

const CHECK_TIMEOUT_MS = 6000;
const POLL_ONLINE_MS = 60000;
const POLL_DOWN_MS = 5000;

let current: NetState = 'online';
const listeners = new Set<(s: NetState) => void>();

export const netState = () => current;

function publish(s: NetState) {
  if (s === current) return;
  current = s;
  listeners.forEach(fn => fn(s));
}

/** Asks the server's /api/health (services.internet) and publishes the answer. */
export async function checkNet(): Promise<NetState> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), CHECK_TIMEOUT_MS);
  let s: NetState;
  try {
    const res = await fetch(`${base()}/api/health`, { cache: 'no-store', signal: ctrl.signal });
    const data = await res.json().catch(() => null);
    s = data?.services?.internet === 'offline' ? 'offline' : 'online';
  } catch {
    s = 'unreachable';
  } finally {
    clearTimeout(timer);
  }
  publish(s);
  return s;
}

/** The connection state, checked now and then while mounted (often while it is down). */
export function useNetState(): NetState {
  const [s, setS] = useState(current);
  useEffect(() => {
    let timer = 0;
    let alive = true;
    const tick = () => {
      checkNet().then(next => {
        if (alive) timer = window.setTimeout(tick, next === 'online' ? POLL_ONLINE_MS : POLL_DOWN_MS);
      });
    };
    listeners.add(setS);
    tick();
    return () => {
      alive = false;
      window.clearTimeout(timer);
      listeners.delete(setS);
    };
  }, []);
  return s;
}

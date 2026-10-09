import { useEffect, useRef, useState } from 'react';
import { request } from '../lib/http';

// Instant-play hints, same rules as the web app (web/src/shared/lib/instant.ts): cards ask
// whether a title has a release TorBox has cached; asks of one render are batched into one
// POST, unknown answers (the server still checks) are asked again after RETRY_MS, twice.

export interface InstantItem {
  type: 'movie' | 'tv';
  tmdb_id: number;
  title?: string;
  season?: number;
  episode?: number;
}

export type InstantVerdict = boolean | null;

const FLUSH_MS = 150;
const RETRY_MS = 20_000;
const MAX_ROUNDS = 2;
const KNOWN_TTL = 10 * 60_000;
const UNKNOWN_TTL = 60_000;
const BATCH = 100;

const answers = new Map<string, { v: InstantVerdict; at: number }>();
const listeners = new Map<string, Set<(v: InstantVerdict) => void>>();
const want = new Map<string, InstantItem>();
const inflight = new Set<string>();
const rounds = new Map<string, number>();
const retryItems = new Map<string, InstantItem>();
let flushTimer: ReturnType<typeof setTimeout> | undefined;
let retryTimer: ReturnType<typeof setTimeout> | undefined;
let off = false; // TorBox is off on the server

const keyOf = (it: InstantItem) => `${it.type}:${it.tmdb_id}`;

function ask(item: InstantItem, retry = false) {
  const key = keyOf(item);
  if (off || want.has(key) || inflight.has(key)) return;
  const a = answers.get(key);
  if (a && !retry) {
    const ttl = a.v === null ? UNKNOWN_TTL : KNOWN_TTL;
    if (Date.now() - a.at < ttl || retryItems.has(key)) return;
  }
  want.set(key, item);
  if (!flushTimer) flushTimer = setTimeout(flush, FLUSH_MS);
}

function scheduleRetry(key: string, item: InstantItem) {
  const n = rounds.get(key) || 0;
  if (n >= MAX_ROUNDS) return;
  rounds.set(key, n + 1);
  retryItems.set(key, item);
  if (retryTimer) return;
  retryTimer = setTimeout(() => {
    retryTimer = undefined;
    const items = Array.from(retryItems.values());
    retryItems.clear();
    items.forEach(it => {
      const l = listeners.get(keyOf(it));
      if (l && l.size) ask(it, true);
    });
  }, RETRY_MS);
}

function flush() {
  flushTimer = undefined;
  const items = Array.from(want.values());
  want.clear();
  for (let i = 0; i < items.length; i += BATCH) void send(items.slice(i, i + BATCH));
}

async function send(items: InstantItem[]) {
  const keys = items.map(keyOf);
  keys.forEach(k => inflight.add(k));
  let results: Record<string, InstantVerdict> = {};
  try {
    const res = await request('/api/torrent-stream/instant', { method: 'POST', body: { items } });
    if (res.ok) {
      const data = await res.json();
      if (data && data.enabled === false) off = true;
      results = (data && data.results) || {};
    }
  } catch {
    /* unknown, asked again below */
  }
  const now = Date.now();
  items.forEach((it, i) => {
    const key = keys[i];
    inflight.delete(key);
    const v = typeof results[key] === 'boolean' ? results[key] : null;
    answers.set(key, { v, at: now });
    if (v === null) {
      if (!off) scheduleRetry(key, it);
    } else {
      rounds.delete(key);
    }
    const l = listeners.get(key);
    if (l) l.forEach(fn => fn(v));
  });
}

/** True when the title plays instantly from TorBox; asks only while `enabled`. */
export function useInstant(item: InstantItem, enabled: boolean): boolean {
  const key = keyOf(item);
  const [v, setV] = useState<InstantVerdict>(() => {
    const a = answers.get(key);
    return a ? a.v : null;
  });
  const itemRef = useRef(item);
  itemRef.current = item;

  useEffect(() => {
    if (!enabled || !(itemRef.current.tmdb_id > 0)) return;
    let set = listeners.get(key);
    if (!set) {
      set = new Set();
      listeners.set(key, set);
    }
    set.add(setV);
    const a = answers.get(key);
    setV(a ? a.v : null);
    ask(itemRef.current);
    return () => {
      set!.delete(setV);
      if (set!.size === 0) listeners.delete(key);
    };
  }, [key, enabled]);

  return v === true;
}

"use client";

import { useCallback, useEffect, useEffectEvent, useSyncExternalStore } from 'react';
import { getApiUrl } from '@/shared/lib/api';

// Instant-play hints: does a title have a release TorBox has cached? Cards ask through
// useInstant; the asks of one render (a row) are batched into one POST. The server answers
// unknown (null) for titles it still checks, so those are asked again after RETRY_MS, at most
// MAX_ROUNDS times.

export interface InstantItem {
  type: 'movie' | 'tv';
  tmdb_id: number;
  title?: string;
  year?: number;
  season?: number;
  episode?: number;
}

/** true: cached on TorBox, false: not, null: unknown (still checking, or TorBox is off) */
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
// The server said TorBox is off: nothing is asked again until a reload
let off = false;

export const instantKey = (type: string, id: number) => `${type}:${id}`;

function emit(key: string, v: InstantVerdict) {
  listeners.get(key)?.forEach(fn => fn(v));
}

function ask(item: InstantItem, retry = false) {
  const key = instantKey(item.type, item.tmdb_id);
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
  const n = rounds.get(key) ?? 0;
  if (n >= MAX_ROUNDS) return;
  rounds.set(key, n + 1);
  retryItems.set(key, item);
  if (!retryTimer) {
    retryTimer = setTimeout(() => {
      retryTimer = undefined;
      const items = [...retryItems.values()];
      retryItems.clear();
      // Only titles a card still shows
      items.forEach(it => { if (listeners.get(instantKey(it.type, it.tmdb_id))?.size) ask(it, true); });
    }, RETRY_MS);
  }
}

async function flush() {
  flushTimer = undefined;
  const items = [...want.values()];
  want.clear();
  for (let i = 0; i < items.length; i += BATCH) {
    void send(items.slice(i, i + BATCH));
  }
}

async function send(items: InstantItem[]) {
  const keys = items.map(it => instantKey(it.type, it.tmdb_id));
  keys.forEach(k => inflight.add(k));
  let results: Record<string, InstantVerdict> = {};
  try {
    const res = await fetch(`${getApiUrl()}/api/torrent-stream/instant`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ items }),
    });
    if (res.ok) {
      const data = await res.json();
      if (data?.enabled === false) off = true;
      results = data?.results || {};
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
    emit(key, v);
  });
}

const serverVerdict = () => null;

/** The instant-play verdict of a title; asks only while `enabled` (e.g. once the card is near the viewport). */
export function useInstant(item: InstantItem | null, enabled = true): InstantVerdict {
  const key = item ? instantKey(item.type, item.tmdb_id) : '';
  const active = !!key && enabled && !!item && item.tmdb_id > 0;
  // Latest item without re-asking when the caller passes a new object for the same title
  const currentItem = useEffectEvent(() => item);

  // A listener marks the title as still shown (see scheduleRetry)
  const subscribe = useCallback((onChange: () => void) => {
    if (!active) return () => {};
    let set = listeners.get(key);
    if (!set) listeners.set(key, (set = new Set()));
    set.add(onChange);
    return () => {
      set.delete(onChange);
      if (set.size === 0) listeners.delete(key);
    };
  }, [key, active]);
  const v = useSyncExternalStore(subscribe, () => (key ? answers.get(key)?.v ?? null : null), serverVerdict);

  useEffect(() => {
    const it = currentItem();
    if (active && it) ask(it);
  }, [key, active]);

  return v;
}

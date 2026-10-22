"use client";

import { useEffect, useSyncExternalStore } from 'react';
import { StreamMediaType, StreamProgress, getProgressList } from '@/features/watch/api';
import { getActiveProfileId, useActiveProfile } from '@/shared/lib/profile';

// One progress list per profile, shared by Home (cards, billboard) and Continue Watching.
// Entries that did not change keep their object, so only cards whose entry changed re-render.

interface Loaded {
  list: StreamProgress[];
  byKey: Map<string, StreamProgress>;
  at: number;
}

// A plain (non-forced) load within this window reuses the memory copy
const REUSE_MS = 5000;
const NO_ITEMS: StreamProgress[] = [];

const loaded = new Map<number, Loaded>();
const inflight = new Map<number, Promise<StreamProgress[]>>();
const seq = new Map<number, number>();
const listeners = new Set<() => void>();

const keyOf = (type: string, id: number) => `${type}-${id}`;

function emit() {
  listeners.forEach(l => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

function store(profileId: number, list: StreamProgress[]) {
  const prev = loaded.get(profileId)?.byKey;
  const byKey = new Map<string, StreamProgress>();
  const kept = list.map(p => {
    const key = keyOf(p.media_type, p.tmdb_id);
    const old = prev?.get(key);
    const entry = old && JSON.stringify(old) === JSON.stringify(p) ? old : p;
    // An episode that is not out yet has nothing to resume
    if (!byKey.has(key) && !p.next_air_date) byKey.set(key, entry);
    return entry;
  });
  loaded.set(profileId, { list: kept, byKey, at: Date.now() });
  emit();
}

/** Loads the profile's list; concurrent calls share one request. force skips the memory copy. */
export function loadProgress(profileId: number, opts: { force?: boolean } = {}): Promise<StreamProgress[]> {
  const mem = loaded.get(profileId);
  if (!opts.force) {
    const pending = inflight.get(profileId);
    if (pending) return pending;
    if (mem && Date.now() - mem.at < REUSE_MS) return Promise.resolve(mem.list);
  }
  const n = (seq.get(profileId) ?? 0) + 1;
  seq.set(profileId, n);
  const req = getProgressList(profileId).then(list => {
    // An older request finishing late must not overwrite a newer answer
    if (seq.get(profileId) === n) store(profileId, list);
    return loaded.get(profileId)?.list ?? list;
  }).finally(() => {
    if (inflight.get(profileId) === req) inflight.delete(profileId);
  });
  inflight.set(profileId, req);
  return req;
}

/** Saved entry of a title for non-React code (active profile by default). */
export function getProgressFor(type: StreamMediaType, id: number, profileId = getActiveProfileId()): StreamProgress | undefined {
  return profileId ? loaded.get(profileId)?.byKey.get(keyOf(type, id)) : undefined;
}

/** Drops a title from the memory list (e.g. removed from Continue Watching). */
export function removeProgressEntry(profileId: number, type: StreamMediaType, id: number) {
  const mem = loaded.get(profileId);
  if (!mem || !mem.list.some(p => p.media_type === type && p.tmdb_id === id)) return;
  store(profileId, mem.list.filter(p => !(p.media_type === type && p.tmdb_id === id)));
}

/** Replaces the memory list of a profile. */
export function replaceProgressList(profileId: number, list: StreamProgress[]) {
  seq.set(profileId, (seq.get(profileId) ?? 0) + 1);
  store(profileId, list);
}

/** The profile's list (empty until loaded); loads it on mount. */
export function useProgressList(profileId: number | null): StreamProgress[] {
  useEffect(() => { if (profileId) loadProgress(profileId); }, [profileId]);
  return useSyncExternalStore(
    subscribe,
    () => (profileId ? loaded.get(profileId)?.list ?? NO_ITEMS : NO_ITEMS),
    () => NO_ITEMS,
  );
}

/** Saved entry of one title for the active profile; re-renders only when that entry changes. */
export function useProgressFor(type: StreamMediaType | undefined, id: number | undefined): StreamProgress | undefined {
  const profileId = useActiveProfile()?.id ?? null;
  return useSyncExternalStore(
    subscribe,
    () => (profileId && type && id ? loaded.get(profileId)?.byKey.get(keyOf(type, id)) : undefined),
    () => undefined,
  );
}

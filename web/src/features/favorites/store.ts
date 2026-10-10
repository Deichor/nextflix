"use client";

import { useEffect, useMemo, useSyncExternalStore } from 'react';
import { getApiUrl } from '@/shared/lib/api';
import { getActiveProfileId, profileHeaders, PROFILE_EVENT } from '@/shared/lib/profile';
import { parseRatings } from '@/features/favorites/ratingsResponse';

// Per-profile ratings ("Not for me" / "I like this" / "Love this!") and Favorites, backed by
// /api/ratings. The whole list is loaded once per profile and shared by every card, so a
// row of cards never sends one request per title.

export type RatingValue = 'dislike' | 'like' | 'love';
export type RatingMediaType = 'movie' | 'tv';

export interface Rating {
  profile_id: number;
  media_type: RatingMediaType;
  tmdb_id: number;
  title: string;
  poster_path: string;
  backdrop_path: string;
  rating: RatingValue | null;
  favorite: boolean;
  updated_at?: string;
}

/** What a rating / favorite needs to know about a title (stored for the Favorites page). */
export interface RatableTitle {
  id: number;
  mediaType: RatingMediaType;
  title: string;
  /** Raw TMDB paths ("/abc.jpg") */
  posterPath?: string;
  backdropPath?: string;
}

const keyOf = (mediaType: string, id: number) => `${mediaType}-${id}`;

let profileId: number | null = null;
let entries = new Map<string, Rating>();
let loaded = false;
let loading: Promise<void> | null = null;
let version = 0;
// Requests in flight per title; a server answer only replaces local state when it is the last one
const pending = new Map<string, number>();

const listeners = new Set<() => void>();
const changeListeners = new Set<(r: Rating) => void>();

function emit() {
  version++;
  listeners.forEach(fn => fn());
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}

function syncProfile() {
  const id = getActiveProfileId();
  if (id === profileId) return;
  profileId = id;
  entries = new Map();
  loaded = false;
  loading = null;
  pending.clear();
  emit();
  if (id) loadRatings();
}

let profileListening = false;
function listenForProfile() {
  if (profileListening || typeof window === 'undefined') return;
  profileListening = true;
  window.addEventListener(PROFILE_EVENT, syncProfile);
  window.addEventListener('storage', syncProfile);
}

/** Loads the active profile's ratings once (force reloads). */
export function loadRatings(force = false): Promise<void> {
  if (typeof window === 'undefined') return Promise.resolve();
  listenForProfile();
  const pid = getActiveProfileId();
  if (pid !== profileId) {
    profileId = pid;
    entries = new Map();
    loaded = false;
    loading = null;
  }
  if (!pid) return Promise.resolve();
  if (loaded && !force) return Promise.resolve();
  if (loading && !force) return loading;
  const p: Promise<void> = (async () => {
    try {
      const res = await fetch(`${getApiUrl()}/api/ratings?profile_id=${pid}`, { headers: profileHeaders(), cache: 'no-store' });
      const data = await parseRatings<Rating>(res);
      if (profileId !== pid) return;
      const next = new Map<string, Rating>();
      data.forEach(r => next.set(keyOf(r.media_type, r.tmdb_id), r));
      // Keep optimistic values of titles that are still being saved
      pending.forEach((n, k) => {
        const local = entries.get(k);
        if (n > 0 && local) next.set(k, local);
      });
      entries = next;
      loaded = true;
    } catch {
      /* keep what we have; the next mount tries again */
    }
    emit();
  })();
  loading = p;
  p.then(() => { if (loading === p) loading = null; });
  return p;
}

function useStoreSubscription() {
  useEffect(() => { loadRatings(); }, []);
}

const getVersion = () => version;
const serverVersion = () => 0;

/** Re-renders on every ratings change; returns a counter. */
export function useRatingsVersion(): number {
  useStoreSubscription();
  return useSyncExternalStore(subscribe, getVersion, serverVersion);
}

/** Rating row of one title (undefined when it has neither a rating nor a favorite). */
export function useTitleRating(t: { id: number; mediaType: RatingMediaType } | null): Rating | undefined {
  useStoreSubscription();
  const key = t ? keyOf(t.mediaType, t.id) : '';
  return useSyncExternalStore(
    subscribe,
    () => (key ? entries.get(key) : undefined),
    () => undefined,
  );
}

/** Every rating of the active profile (newest change first, as the server sent them). */
export function useRatings(): { list: Rating[]; loaded: boolean } {
  const v = useRatingsVersion();
  // New array only when something changed, so memos depending on it stay stable
  const list = useMemo(() => Array.from(entries.values()), [v]); // eslint-disable-line react-hooks/exhaustive-deps
  return { list, loaded };
}

export function getTitleRating(mediaType: string, id: number): Rating | undefined {
  return entries.get(keyOf(mediaType, id));
}

/** Called after every saved change (used to refresh recommendations). */
export function onRatingsChanged(fn: (r: Rating) => void): () => void {
  changeListeners.add(fn);
  return () => { changeListeners.delete(fn); };
}

type Patch = { rating?: RatingValue | null; favorite?: boolean };

async function save(t: RatableTitle, patch: Patch): Promise<boolean> {
  const pid = getActiveProfileId();
  if (!pid) return false;
  if (profileId !== pid) await loadRatings();
  const key = keyOf(t.mediaType, t.id);
  const before = entries.get(key);
  const optimistic: Rating = {
    profile_id: pid,
    media_type: t.mediaType,
    tmdb_id: t.id,
    title: before?.title || t.title,
    poster_path: before?.poster_path || t.posterPath || '',
    backdrop_path: before?.backdrop_path || t.backdropPath || '',
    rating: before?.rating ?? null,
    favorite: before?.favorite ?? false,
    updated_at: new Date().toISOString(),
    ...patch,
  };
  const set = (r: Rating | undefined) => {
    entries = new Map(entries);
    if (!r || (!r.rating && !r.favorite)) entries.delete(key);
    else entries.set(key, r);
    emit();
  };
  set(optimistic);
  pending.set(key, (pending.get(key) || 0) + 1);

  let ok = false;
  let answer: Rating | null = null;
  try {
    const res = await fetch(`${getApiUrl()}/api/ratings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', ...profileHeaders() },
      body: JSON.stringify({
        profile_id: pid,
        media_type: t.mediaType,
        tmdb_id: t.id,
        title: t.title,
        poster_path: t.posterPath || '',
        backdrop_path: t.backdropPath || '',
        ...patch,
      }),
    });
    ok = res.ok;
    if (ok) answer = await res.json().catch(() => null);
  } catch {
    ok = false;
  }

  const left = (pending.get(key) || 1) - 1;
  if (left > 0) pending.set(key, left);
  else pending.delete(key);
  if (profileId !== pid) return ok;

  const current = entries.get(key);
  if (!ok) {
    // Roll back only the fields this request changed, if nothing newer touched them
    const rolled: Rating | undefined = current ? { ...current } : before ? { ...before } : undefined;
    if (rolled) {
      if ('rating' in patch && current?.rating === patch.rating) rolled.rating = before?.rating ?? null;
      if ('favorite' in patch && current?.favorite === patch.favorite) rolled.favorite = before?.favorite ?? false;
    }
    set(rolled);
    return false;
  }
  if (left <= 0 && answer && typeof answer.tmdb_id === 'number') {
    set({
      ...answer,
      title: answer.title || optimistic.title,
      poster_path: answer.poster_path || optimistic.poster_path,
      backdrop_path: answer.backdrop_path || optimistic.backdrop_path,
    });
  }
  const changed = entries.get(key) || { ...optimistic, rating: null, favorite: false };
  changeListeners.forEach(fn => fn(changed));
  return true;
}

/** Sets (or clears with null) the title's rating. Optimistic; rolls back when saving fails. */
export function setTitleRating(t: RatableTitle, rating: RatingValue | null): Promise<boolean> {
  return save(t, { rating });
}

/** Adds the title to Favorites or removes it. Optimistic; rolls back when saving fails. */
export function setTitleFavorite(t: RatableTitle, favorite: boolean): Promise<boolean> {
  return save(t, { favorite });
}

"use client";

import { useCallback, useEffect, useState } from 'react';
import { getApiUrl } from '@/shared/lib/api';
import { profileHeaders, PROFILE_EVENT } from '@/shared/lib/profile';
import type { NfMediaType, NfTitle } from '@/features/catalog/titles';

// Per-profile My List backed by /api/mylist (X-User-ID = active profile).
// TMDB titles are stored with the existing "9" + tmdbId media_id convention.

export interface MyListEntry {
  media_id: number;
  media_type: string;
  added_at?: string;
}

export function listIdFor(t: { id: number }): number {
  return parseInt(`9${t.id}`, 10);
}

export function tmdbIdFromListId(mediaId: number): number | null {
  const s = String(mediaId);
  return s.startsWith('9') && s.length > 1 ? parseInt(s.slice(1), 10) : null;
}

let entries: MyListEntry[] | null = null;
let loading: Promise<MyListEntry[]> | null = null;
const listeners = new Set<() => void>();

function emit() {
  listeners.forEach(fn => fn());
}

export async function loadMyList(force = false): Promise<MyListEntry[]> {
  if (entries && !force) return entries;
  if (loading && !force) return loading;
  loading = (async () => {
    try {
      const res = await fetch(`${getApiUrl()}/api/mylist`, { headers: profileHeaders() });
      const data = res.ok ? await res.json() : [];
      entries = Array.isArray(data) ? data : [];
    } catch {
      entries = entries || [];
    } finally {
      loading = null;
    }
    emit();
    return entries!;
  })();
  return loading;
}

export async function addToMyList(t: { id: number; mediaType: NfMediaType }): Promise<boolean> {
  const mediaId = listIdFor(t);
  try {
    const res = await fetch(`${getApiUrl()}/api/mylist/${mediaId}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...profileHeaders() },
      body: JSON.stringify({ media_type: t.mediaType }),
    });
    // "already exists" (500 from the old API, 409 possible) still means it is in the list
    if (!res.ok && res.status !== 500 && res.status !== 409) return false;
    entries = [{ media_id: mediaId, media_type: t.mediaType, added_at: new Date().toISOString() }, ...(entries || []).filter(e => e.media_id !== mediaId)];
    emit();
    return true;
  } catch {
    return false;
  }
}

export async function removeFromMyList(t: { id: number }): Promise<boolean> {
  const mediaId = listIdFor(t);
  try {
    const res = await fetch(`${getApiUrl()}/api/mylist/${mediaId}`, { method: 'DELETE', headers: profileHeaders() });
    if (!res.ok) return false;
    entries = (entries || []).filter(e => e.media_id !== mediaId);
    emit();
    return true;
  } catch {
    return false;
  }
}

/** Shared My List state for cards and detail pages. */
export function useMyList() {
  const [, setTick] = useState(0);

  useEffect(() => {
    const fn = () => setTick(n => n + 1);
    listeners.add(fn);
    loadMyList();
    const onProfile = () => loadMyList(true);
    window.addEventListener(PROFILE_EVENT, onProfile);
    return () => {
      listeners.delete(fn);
      window.removeEventListener(PROFILE_EVENT, onProfile);
    };
  }, []);

  const has = useCallback((t: { id: number }) => {
    const id = listIdFor(t);
    return !!entries?.some(e => e.media_id === id);
  }, []);

  const toggle = useCallback(async (t: NfTitle | { id: number; mediaType: NfMediaType }) => {
    return has(t) ? removeFromMyList(t) : addToMyList(t);
  }, [has]);

  return { entries: entries || [], loaded: entries !== null, has, toggle, reload: () => loadMyList(true) };
}

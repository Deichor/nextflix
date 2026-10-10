"use client";

import { useSyncExternalStore } from 'react';

export interface Profile {
  id: number;
  name: string;
  avatar: string;
  color: string;
  /** UI language of the profile (server default 'en') */
  language?: ProfileLanguage;
  created_at?: string;
}

export type ProfileLanguage = 'en' | 'tr';

const KEY = 'hfx-profile';
export const PROFILE_EVENT = 'hfx-profile-change';

export function getActiveProfile(): Profile | null {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.localStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as Profile) : null;
  } catch {
    return null;
  }
}

export function getActiveProfileId(): number | null {
  return getActiveProfile()?.id ?? null;
}

export function setActiveProfile(profile: Profile | null) {
  if (typeof window === 'undefined') return;
  try {
    if (profile) window.localStorage.setItem(KEY, JSON.stringify(profile));
    else window.localStorage.removeItem(KEY);
  } catch {
    /* storage unavailable */
  }
  window.dispatchEvent(new Event(PROFILE_EVENT));
}

// Headers that scope per-user backend data (My List, history, ...) to the active profile
export function profileHeaders(): Record<string, string> {
  const id = getActiveProfileId();
  return id ? { 'X-User-ID': String(id) } : {};
}

// X-User-ID value for per-user endpoints; falls back to the legacy user 1 (the seeded "Home" profile)
export function profileUserId(): string {
  return String(getActiveProfileId() ?? 1);
}

function subscribeProfile(onChange: () => void) {
  window.addEventListener(PROFILE_EVENT, onChange);
  window.addEventListener('storage', onChange);
  return () => {
    window.removeEventListener(PROFILE_EVENT, onChange);
    window.removeEventListener('storage', onChange);
  };
}

// useSyncExternalStore needs a stable snapshot: parse again only when the stored JSON changes
let snapshotRaw: string | null = null;
let snapshot: Profile | null = null;

function profileSnapshot(): Profile | null {
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(KEY);
  } catch {
    /* storage unavailable */
  }
  if (raw !== snapshotRaw) {
    snapshotRaw = raw;
    snapshot = getActiveProfile();
  }
  return snapshot;
}

// The server and the hydration pass render without a profile
const serverProfile = () => null;

/** The active profile; null on the server and during hydration. */
export function useActiveProfile(): Profile | null {
  return useSyncExternalStore(subscribeProfile, profileSnapshot, serverProfile);
}

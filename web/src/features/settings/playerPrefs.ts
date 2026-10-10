"use client";

import { useCallback, useMemo, useSyncExternalStore } from 'react';

// Same localStorage entry and shape as the player (features/watch/subtitles.ts: PlayerPrefs).
// audioLang and sub are already read by the player; 'original', subSize and autoplayNext are new.
export type SubtitleSize = 'small' | 'medium' | 'large';

export interface SettingsPlayerPrefs {
  /** ISO 639-1 code the player picked last, or 'original' for the title's original language */
  audioLang?: string;
  sub?: 'off' | { lang: string; id?: string };
  subSize?: SubtitleSize;
  /** Start the next episode after the countdown (default on) */
  autoplayNext?: boolean;
}

export const AUDIO_ORIGINAL = 'original';

/** Font size multiplier for the player's subtitle overlay */
export const SUBTITLE_SIZE_SCALE: Record<SubtitleSize, number> = { small: 0.8, medium: 1, large: 1.25 };

const prefsKey = (profileId: number | null) => `hfx-player:${profileId ?? 0}`;

function readRaw(profileId: number | null): string | null {
  try {
    return window.localStorage.getItem(prefsKey(profileId));
  } catch {
    return null;
  }
}

function parsePrefs(raw: string | null): SettingsPlayerPrefs {
  try {
    const parsed = raw ? JSON.parse(raw) : {};
    return parsed && typeof parsed === 'object' ? (parsed as SettingsPlayerPrefs) : {};
  } catch {
    return {};
  }
}

export function readPrefs(profileId: number | null): SettingsPlayerPrefs {
  return parsePrefs(readRaw(profileId));
}

// Same-tab writes do not fire 'storage'
const localListeners = new Set<() => void>();

// Merges into the stored object so keys written by the player (e.g. sub.id) are kept
export function writePrefs(profileId: number | null, patch: Partial<SettingsPlayerPrefs>): void {
  try {
    const next = { ...readPrefs(profileId), ...patch };
    window.localStorage.setItem(prefsKey(profileId), JSON.stringify(next));
  } catch {
    /* storage unavailable */
  }
  localListeners.forEach(fn => fn());
}

function subscribePrefs(onChange: () => void) {
  localListeners.add(onChange);
  window.addEventListener('storage', onChange);
  return () => {
    localListeners.delete(onChange);
    window.removeEventListener('storage', onChange);
  };
}

const serverRaw = () => null;

export function usePlayerPrefs(profileId: number | null) {
  // The raw JSON string is a stable snapshot; parsed only when it changes
  const raw = useSyncExternalStore(subscribePrefs, () => readRaw(profileId), serverRaw);
  const prefs = useMemo(() => parsePrefs(raw), [raw]);

  const update = useCallback(
    (patch: Partial<SettingsPlayerPrefs>) => writePrefs(profileId, patch),
    [profileId],
  );

  return [prefs, update] as const;
}

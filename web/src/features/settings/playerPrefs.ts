"use client";

import { useCallback, useEffect, useState } from 'react';

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

export function readPrefs(profileId: number | null): SettingsPlayerPrefs {
  try {
    const raw = window.localStorage.getItem(prefsKey(profileId));
    const parsed = raw ? JSON.parse(raw) : {};
    return parsed && typeof parsed === 'object' ? (parsed as SettingsPlayerPrefs) : {};
  } catch {
    return {};
  }
}

// Merges into the stored object so keys written by the player (e.g. sub.id) are kept
export function writePrefs(profileId: number | null, patch: Partial<SettingsPlayerPrefs>): void {
  try {
    const next = { ...readPrefs(profileId), ...patch };
    window.localStorage.setItem(prefsKey(profileId), JSON.stringify(next));
  } catch {
    /* storage unavailable */
  }
}

export function usePlayerPrefs(profileId: number | null) {
  const [prefs, setPrefs] = useState<SettingsPlayerPrefs>({});

  useEffect(() => {
    setPrefs(readPrefs(profileId));
    const key = prefsKey(profileId);
    const onStorage = (e: StorageEvent) => {
      if (e.key === key) setPrefs(readPrefs(profileId));
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, [profileId]);

  const update = useCallback(
    (patch: Partial<SettingsPlayerPrefs>) => {
      writePrefs(profileId, patch);
      setPrefs(readPrefs(profileId));
    },
    [profileId],
  );

  return [prefs, update] as const;
}

import { readJson, writeJson } from './storage';
import { isLocale, type Locale } from './i18n';

export interface Profile {
  id: number;
  name: string;
  avatar: string;
  color: string;
  language?: string;
  created_at?: string;
}

// Same key as the web app (a different origin, so they never collide)
const KEY = 'hfx-profile';

// Same limits as the server and web/src/features/profiles/api.ts
export const MAX_PROFILES = 5;
export const MAX_PROFILE_NAME = 20;

let active: Profile | null = readJson<Profile>(KEY);

export const getProfile = () => active;
export const getProfileId = () => active?.id ?? null;

export function setProfile(p: Profile | null): void {
  active = p;
  writeJson(KEY, p);
}

/** UI and content language of the active profile (server default 'en'). */
export function profileLocale(p: Profile | null = active): Locale {
  return p && isLocale(p.language) ? p.language : 'en';
}

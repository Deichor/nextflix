import { HomeData, RecommendationRow, fetchHome, fetchRecommendations, mergeHome } from '@/features/home/feeds';
import type { Locale } from '@/shared/i18n';

// Last home rows in localStorage, so the next launch paints them before the network answers.
// Best effort: every storage call may throw (quota, private mode) and is ignored.

interface Snapshot<T> {
  savedAt: number;
  data: T;
}

const MAX_BYTES = 400 * 1024;
const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;
// The picker skips its prefetch when the snapshot is younger than this
const PREFETCH_FRESH_MS = 10 * 60 * 1000;

const homeKey = (lang: string) => `hfx-home:v1:${lang}`;
const recsKey = (profileId: number, lang: string) => `hfx-home-recs:v1:${profileId}:${lang}`;

function read<T>(key: string, valid: (data: any) => boolean): Snapshot<T> | null {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    const snap = JSON.parse(raw);
    if (typeof snap?.savedAt !== 'number' || Date.now() - snap.savedAt > MAX_AGE_MS || !valid(snap.data)) return null;
    return snap as Snapshot<T>;
  } catch {
    return null;
  }
}

function write(key: string, data: unknown) {
  if (typeof window === 'undefined') return;
  try {
    const raw = JSON.stringify({ savedAt: Date.now(), data });
    if (raw.length > MAX_BYTES) return;
    window.localStorage.setItem(key, raw);
  } catch {
    /* storage full or unavailable */
  }
}

const isHomeData = (d: any) => !!d && typeof d === 'object' && !!d.genres && typeof d.genres === 'object';

export function readHomeSnapshot(lang: string): Snapshot<HomeData> | null {
  return read<HomeData>(homeKey(lang), isHomeData);
}

export function writeHomeSnapshot(lang: string, data: HomeData) {
  write(homeKey(lang), data);
}

export function readRecsSnapshot(profileId: number, lang: string): RecommendationRow[] | null {
  return read<RecommendationRow[]>(recsKey(profileId, lang), Array.isArray)?.data ?? null;
}

export function writeRecsSnapshot(profileId: number, lang: string, rows: RecommendationRow[]) {
  write(recsKey(profileId, lang), rows);
}

const homePrefetches = new Map<string, Promise<void>>();

/** Loads the home rows into the snapshot ahead of Home (profile picker). */
export function prefetchHome(lang: string): Promise<void> {
  const snap = readHomeSnapshot(lang);
  if (snap && Date.now() - snap.savedAt < PREFETCH_FRESH_MS) return Promise.resolve();
  let p = homePrefetches.get(lang);
  if (!p) {
    p = fetchHome(lang)
      .then(fresh => writeHomeSnapshot(lang, mergeHome(readHomeSnapshot(lang)?.data ?? null, fresh)))
      .catch(() => { /* Home loads it again */ })
      .finally(() => homePrefetches.delete(lang));
    homePrefetches.set(lang, p);
  }
  return p;
}

const recsPrefetches = new Map<string, number>();
const RECS_PREFETCH_GAP_MS = 60 * 1000;

/** Warms the server's recommendations cache for a profile and stores the rows if they land in time. */
export function prefetchRecommendations(profileId: number, lang: Locale) {
  const key = `${profileId}:${lang}`;
  const last = recsPrefetches.get(key);
  if (last && Date.now() - last < RECS_PREFETCH_GAP_MS) return;
  recsPrefetches.set(key, Date.now());
  fetchRecommendations(profileId, undefined, lang, true)
    .then(rows => writeRecsSnapshot(profileId, lang, rows))
    .catch(() => recsPrefetches.delete(key));
}

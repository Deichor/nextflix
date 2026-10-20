import { getApiUrl } from '@/shared/lib/api';
import { intlLocale, type Locale } from '@/shared/i18n/core';
import { type SubtitleSize } from '@/features/settings/playerPrefs';
import { normLang } from '@/features/watch/lang';
import { SYNC_NONE, syncValue } from '@/features/watch/subsLog';
import { finalCues } from '@/features/watch/cuePoll';

export { normLang };
export { pickAudioTrack, type AudioTrackInfo } from '@/features/watch/audioPick';

// ---- Languages ----

const displayNames: Partial<Record<Locale, Intl.DisplayNames | null>> = {};

/** Full language name in the UI language ("İngilizce", "Spanish"), the code when Intl does not know it. */
export function languageName(locale: Locale, code: string): string {
  if (!code) return '';
  if (displayNames[locale] === undefined) {
    try {
      displayNames[locale] = new Intl.DisplayNames([intlLocale(locale)], { type: 'language' });
    } catch {
      displayNames[locale] = null;
    }
  }
  let name = code;
  try {
    name = displayNames[locale]?.of(code) || code;
  } catch {
    /* invalid code */
  }
  return name.charAt(0).toLocaleUpperCase(intlLocale(locale)) + name.slice(1);
}

// ---- Per-profile player preferences (localStorage) ----

export interface PlayerPrefs {
  /** Last audio language the viewer picked (ISO 639-1), or 'original' (Settings) for the title's original language */
  audioLang?: string;
  /** Last subtitle choice: off, or a language (plus the exact file when it is offered again) */
  sub?: 'off' | { lang: string; id?: string };
  /** Subtitle text size (Settings) */
  subSize?: SubtitleSize;
  /** Start the next episode after the countdown (Settings, default on) */
  autoplayNext?: boolean;
  /** Anime4K upscaling for anime (player panel, default off) */
  upscale?: boolean;
  /** Anime4K A+A instead of Mode A while upscaling is on (default off) */
  upscaleExtra?: boolean;
}

const prefsKey = (profileId: number | null) => `hfx-player:${profileId ?? 0}`;

export function readPlayerPrefs(profileId: number | null): PlayerPrefs {
  try {
    const raw = window.localStorage.getItem(prefsKey(profileId));
    return raw ? (JSON.parse(raw) as PlayerPrefs) : {};
  } catch {
    return {};
  }
}

export function writePlayerPrefs(profileId: number | null, patch: Partial<PlayerPrefs>): void {
  try {
    const next = { ...readPlayerPrefs(profileId), ...patch };
    window.localStorage.setItem(prefsKey(profileId), JSON.stringify(next));
  } catch {
    /* storage unavailable */
  }
}

// ---- Subtitles API ----

export type SubtitleSource = 'torrent' | 'embedded' | 'opensubtitles' | 'subdl' | 'subsource' | 'turkcealtyazi' | string;

export interface SubtitleItem {
  id: string;
  source: SubtitleSource;
  language: string;
  label: string;
  release: string;
  hi: boolean;
  forced: boolean;
  /** Hash match or from the same torrent: timing fits this file */
  synced: boolean;
  score: number;
  /** Embedded track of a big file: read while it plays, its cues grow (fetch with the position) */
  live?: boolean;
  /** OpenSubtitles' daily download limit is used up: listed last, not picked by itself */
  quota?: boolean;
  /** Source "ai": Claude translates the subtitle of this language (listed only when no source has the language) */
  ai_from?: string;
}

export interface SubtitleQuery {
  tmdb_id: number;
  media_type: 'movie' | 'tv';
  season?: number;
  episode?: number;
  hash: string;
  index: number;
  langs: string[];
  /** Torrent / release name, helps matching */
  title?: string;
  year?: number;
  /** The profile's language: the AI translation target "profile" */
  ui_lang?: string;
}

// Map that keeps only the `max` most recently used entries (Map iterates in insertion order)
class LruCache<K, V> {
  private readonly map = new Map<K, V>();
  private readonly max: number;
  constructor(max: number) {
    this.max = max;
  }
  get(key: K): V | undefined {
    const v = this.map.get(key);
    if (v !== undefined) {
      this.map.delete(key);
      this.map.set(key, v);
    }
    return v;
  }
  set(key: K, value: V) {
    this.map.delete(key);
    this.map.set(key, value);
    if (this.map.size > this.max) this.map.delete(this.map.keys().next().value as K);
  }
  delete(key: K) {
    this.map.delete(key);
  }
}

const CACHE_MAX = 20;

const listCache = new LruCache<string, Promise<SubtitleItem[]>>(CACHE_MAX);

/**
 * Subtitles for the playing file, best first per language. Cached per file for the session;
 * fresh skips the cache (slow providers finish after the first answer) and replaces it.
 */
export function fetchSubtitles(q: SubtitleQuery, opts: { fresh?: boolean; refresh?: boolean } = {}): Promise<SubtitleItem[]> {
  const p = new URLSearchParams({
    tmdb_id: String(q.tmdb_id),
    media_type: q.media_type,
    hash: q.hash,
    index: String(q.index),
    langs: q.langs.join(','),
  });
  if (q.media_type === 'tv') {
    p.set('season', String(q.season ?? 1));
    p.set('episode', String(q.episode ?? 1));
  }
  if (q.title) p.set('title', q.title);
  if (q.year) p.set('year', String(q.year));
  if (q.ui_lang) p.set('ui_lang', q.ui_lang);
  const key = p.toString();
  const hit = listCache.get(key);
  if (opts.refresh) opts = { ...opts, fresh: true };
  if (hit && !opts.fresh) return hit;
  // refresh=1: the server asks the providers again (late uploads; it limits how often)
  const req = fetch(`${getApiUrl()}/api/subtitles?${key}${opts.refresh ? '&refresh=1' : ''}`)
    .then(res => (res.ok ? res.json() : { items: [] }))
    .then(data => (Array.isArray(data?.items) ? (data.items as SubtitleItem[]).filter(s => s && s.id) : []))
    .catch(() => [] as SubtitleItem[]);
  if (opts.fresh) {
    // A fuller answer joins what the cache holds; an empty one changes nothing
    req.then(items => {
      if (items.length === 0) return;
      const merged = (hit || Promise.resolve([] as SubtitleItem[])).then(prev => mergeSubtitles(prev, items));
      listCache.set(key, merged);
    });
    return req;
  }
  listCache.set(key, req);
  // Empty answers (slow providers, server busy) may be filled in on the next play
  req.then(items => { if (items.length === 0 && listCache.get(key) === req) listCache.delete(key); });
  return req;
}

/** Both lists by id, the newer answer's order first (its entries win) */
export function mergeSubtitles(prev: SubtitleItem[], next: SubtitleItem[]): SubtitleItem[] {
  const ids = new Set(next.map(s => s.id));
  return [...next, ...prev.filter(s => !ids.has(s.id))];
}

// The server answers within a few seconds with what is ready and keeps asking the slower
// providers for its next call: refetch then, unless the preferred language is already there
export const SUBTITLE_RETRY_MS = [8000, 20000];

// An AI translation does not count: a real subtitle that turns up later still replaces it
export const hasSubtitleLang = (items: SubtitleItem[], lang: string) =>
  items.some(s => s.source !== 'ai' && normLang(s.language) === normLang(lang));

// Late uploads: while the profile's language is missing the list is asked again this often, bypassing caches
export const LATE_SUBTITLE_POLL_MS = 10 * 60 * 1000;
// After the retries: how often the player asks for a release that carries the profile's subtitle
export const SUBALT_RETRY_MAX = 4;

/**
 * A later list brought subtitles in lang that the shown list lacked: the merged list, whether lang is new,
 * and the subtitle to show by itself (its best one) when the viewer has not chosen one.
 */
export function lateSubtitles(
  shown: SubtitleItem[],
  fresh: SubtitleItem[],
  lang: string,
  viewerChose: boolean,
): { items: SubtitleItem[]; arrived: boolean; pick: string | null } {
  const items = mergeSubtitles(shown, fresh);
  const arrived = !hasSubtitleLang(shown, lang) && hasSubtitleLang(items, lang);
  if (!arrived || viewerChose) return { items, arrived, pick: null };
  const l = normLang(lang);
  const pool = downloadable(items.filter(s => normLang(s.language) === l && !s.forced));
  return { items, arrived, pick: pool[0]?.id ?? null };
}

/** Items that can be downloaded now: without the ones whose quota is used up or that failed (all of them when none is left). */
function downloadable(pool: SubtitleItem[], failed?: ReadonlySet<string>): SubtitleItem[] {
  const now = Date.now();
  const ok = pool.filter(s => !s.quota && !failed?.has(s.id) && !((failedFiles.get(s.id)?.until ?? 0) > now));
  return ok.length > 0 ? ok : pool;
}

/**
 * Best subtitle of lang in items (not forced, downloadable), for a pick the viewer asked for. The server's
 * order is best first (synced ones first, but complete ones ahead of ones known to have holes).
 */
export function bestSubtitleOf(items: SubtitleItem[], lang: string, failed?: ReadonlySet<string>): string | null {
  const l = normLang(lang);
  const of = items.filter(s => normLang(s.language) === l);
  const pool = downloadable(of.filter(s => !s.forced).length > 0 ? of.filter(s => !s.forced) : of, failed);
  return pool[0]?.id ?? null;
}

// Why a subtitle file could not be loaded: quota = OpenSubtitles' daily download limit,
// limited = another 429, error = a server or provider error (5xx, network), missing = 404 and the like,
// ai = the AI translation cannot be used now (no credit, bad key, turned off)
export type SubtitleFailure = 'quota' | 'limited' | 'error' | 'missing' | 'ai';

/**
 * The subtitle to try after `failedId` could not be loaded: the next one of its language that
 * was not tried yet, from another provider first. After a 429 (quota or rate limit) that provider
 * is left out altogether, so no other download of it is wasted. null: nothing left.
 */
export function nextSubtitleAfterFailure(
  items: SubtitleItem[],
  failedId: string,
  why: SubtitleFailure,
  failed: ReadonlySet<string>,
): string | null {
  const bad = items.find(s => s.id === failedId);
  if (!bad) return null;
  const l = normLang(bad.language);
  const blocked = why === 'quota' || why === 'limited' || why === 'ai';
  const pool = items.filter(s =>
    s.id !== failedId && !failed.has(s.id) && normLang(s.language) === l && s.forced === bad.forced && !s.quota &&
    !(blocked && s.source === bad.source));
  const rank = (s: SubtitleItem) => (s.source === bad.source ? 1 : 0);
  // Stable: the list's own order (best first) within each group
  const ordered = pool.map((s, i) => ({ s, i })).sort((a, b) => rank(a.s) - rank(b.s) || a.i - b.i).map(x => x.s);
  return (ordered.find(s => s.synced && s.source !== bad.source) || ordered[0])?.id ?? null;
}

export interface Cue {
  start: number;
  end: number;
  text: string;
}

// Final answers, asked again after CUE_CACHE_MS (the server may have timed the file better since)
const cueCache = new LruCache<string, { at: number; file: Promise<{ cues: Cue[]; sync: string }> }>(CACHE_MAX);
const CUE_CACHE_MS = 10 * 60 * 1000;
// Subtitle files that could not be loaded, by id: not asked again before `until`
const failedFiles = new Map<string, { why: SubtitleFailure; until: number; errors: number }>();
const FAILED_FILE_MS = 5 * 60 * 1000;

/**
 * How long a failed file is not asked again: a server or network error (often over in seconds, e.g. an AI
 * translation's first answer) 5 s after the first one in a row, doubling up to FAILED_FILE_MS; anything
 * else FAILED_FILE_MS; a used up quota until it resets.
 */
export function failedFileWait(why: SubtitleFailure, errors: number): number {
  return why === 'error' ? Math.min(FAILED_FILE_MS, 5000 * 2 ** Math.max(0, errors - 1)) : FAILED_FILE_MS;
}

function rememberFailure(id: string, why: SubtitleFailure, reset = NaN) {
  const errors = why === 'error' ? (failedFiles.get(id)?.errors ?? 0) + 1 : 0;
  const until = why === 'quota' && reset > Date.now() ? reset : Date.now() + failedFileWait(why, errors);
  failedFiles.set(id, { why, until, errors });
}

/** When a failed subtitle file may be asked again (the quota reset for OpenSubtitles), null if it did not fail. */
export const subtitleRetryAt = (id: string): number | null => failedFiles.get(id)?.until ?? null;

// The server marks a partial live answer with this note (more cues follow while the file plays)
export const PARTIAL_NOTE = 'NOTE nextflix-partial';
// ...and an AI translation with this one while the lines at the position are being translated
export const AI_NOTE = 'NOTE nextflix-ai-translating';

export function fetchSubtitleCues(id: string): Promise<Cue[]> {
  return fetchSubtitleFile(id).then(f => f.cues);
}

/**
 * Cues of a subtitle. `at` (seconds of the file) is where the viewer is: a live embedded track is read from
 * there on. Only a final answer is cached (finalCues); ask again (cuePollMs) for the cues read or timed since.
 */
export interface SubtitleFile {
  cues: Cue[];
  partial: boolean;
  /** X-Subtitle-Sync for the playback log ("hidden" when the browser hides it, see subsLog.ts) */
  sync: string;
  failed?: SubtitleFailure;
  /** AI translation: the lines at the position are still being translated */
  translating?: boolean;
}

/** poll: the file answered before (a partial one asked again); a failure now is temporary and not remembered */
export function fetchSubtitleFile(id: string, at?: number, poll = false): Promise<SubtitleFile> {
  const hit = cueCache.get(id);
  if (hit && Date.now() - hit.at < CUE_CACHE_MS) return hit.file.then(c => ({ cues: c.cues, partial: false, sync: c.sync }));
  if (hit) cueCache.delete(id);
  // A file that failed is not asked again for a while (every seek reloads the cues)
  const bad = poll ? undefined : failedFiles.get(id);
  if (bad && Date.now() < bad.until) return Promise.resolve({ cues: [], partial: false, sync: SYNC_NONE, failed: bad.why });
  const t = at !== undefined && at > 0 ? `&t=${Math.floor(at)}` : '';
  const req = fetch(`${getApiUrl()}/api/subtitles/file?id=${encodeURIComponent(id)}${t}`)
    .then(async (res): Promise<SubtitleFile> => {
      const sync = syncValue(res.headers.get('X-Subtitle-Sync'), res.type === 'cors');
      if (res.ok) {
        const text = await res.text();
        failedFiles.delete(id);
        return { cues: parseVtt(text), partial: text.includes(PARTIAL_NOTE), sync, translating: text.includes(AI_NOTE) };
      }
      const body = await res.json().catch(() => null) as { quota?: boolean; reset_at?: string; ai?: boolean } | null;
      const why: SubtitleFailure = body?.ai ? 'ai'
        : res.status === 429 ? (body?.quota ? 'quota' : 'limited') : res.status >= 500 ? 'error' : 'missing';
      // An unusable AI translation is not asked again for FAILED_FILE_MS, even while polling
      if (!poll || why === 'ai') rememberFailure(id, why, body?.reset_at ? Date.parse(body.reset_at) : NaN);
      return { cues: [], partial: false, sync, failed: why };
    })
    .catch((): SubtitleFile => {
      if (!poll) rememberFailure(id, 'error');
      return { cues: [], partial: false, sync: SYNC_NONE, failed: 'error' };
    });
  req.then(f => {
    if (f.cues.length > 0 && finalCues(f)) cueCache.set(id, { at: Date.now(), file: Promise.resolve({ cues: f.cues, sync: f.sync }) });
  });
  return req;
}


/** The server times a subtitle of a file that does not play yet (the next episode, see pretime.ts); nothing is shown. */
export function pretimeSubtitle(id: string): Promise<void> {
  return fetch(`${getApiUrl()}/api/subtitles/file?id=${encodeURIComponent(id)}&pre=1`).then(() => undefined, () => undefined);
}

const TIME = /(?:(\d+):)?(\d{1,2}):(\d{2})[.,](\d{1,3})/;

function parseTime(s: string): number | null {
  const m = s.match(TIME);
  if (!m) return null;
  const ms = m[4].padEnd(3, '0');
  return (Number(m[1] || 0) * 3600) + Number(m[2]) * 60 + Number(m[3]) + Number(ms) / 1000;
}

/** WebVTT (and SRT-like) text -> cues sorted by start. */
export function parseVtt(text: string): Cue[] {
  const cues: Cue[] = [];
  const blocks = text.replace(/^﻿/, '').replace(/\r\n?/g, '\n').split(/\n{2,}/);
  for (const block of blocks) {
    const lines = block.split('\n');
    const at = lines.findIndex(l => l.includes('-->'));
    if (at < 0) continue;
    const [a, b] = lines[at].split('-->');
    const start = parseTime(a);
    const end = parseTime(b || '');
    if (start === null || end === null || end <= start) continue;
    const body = lines.slice(at + 1).join('\n').trim();
    if (body) cues.push({ start, end, text: body });
  }
  return cues.sort((x, y) => x.start - y.start);
}

export { activeCues } from '@/features/watch/cueLookup';

/**
 * Default subtitle: the viewer's last choice (same file if offered, else the best of that
 * language, off stays off). Without one a Turkish profile gets the best Turkish subtitle unless
 * the audio is Turkish already; other profiles start with subtitles off.
 */
export function pickSubtitle(
  items: SubtitleItem[],
  opts: { profileLang: Locale; pref?: PlayerPrefs['sub']; audioLang?: string },
): string | null {
  // A subtitle whose download quota is used up is only taken when nothing else is left (the server's order is best first)
  const best = (lang: string) => {
    const l = normLang(lang);
    const of = items.filter(s => normLang(s.language) === l && !s.forced);
    const pool = downloadable(of.length > 0 ? of : items.filter(s => normLang(s.language) === l));
    return pool[0]?.id ?? null;
  };
  const pref = opts.pref;
  if (pref === 'off') return null;
  if (pref) {
    if (pref.id && items.some(s => s.id === pref.id && !s.quota)) return pref.id;
    return best(pref.lang);
  }
  if (opts.profileLang === 'tr' && normLang(opts.audioLang) !== 'tr') return best('tr');
  return null;
}

/** List order: the profile's language first, then the other requested ones, then the rest by name. */
export function groupSubtitles(items: SubtitleItem[], order: string[], locale: Locale): SubtitleItem[] {
  const rank = (s: SubtitleItem) => {
    const i = order.indexOf(normLang(s.language));
    return i >= 0 ? i : order.length;
  };
  return items
    .map((s, i) => ({ s, i }))
    .sort((a, b) =>
      rank(a.s) - rank(b.s) ||
      languageName(locale, normLang(a.s.language)).localeCompare(languageName(locale, normLang(b.s.language)), intlLocale(locale)) ||
      a.i - b.i)
    .map(x => x.s);
}

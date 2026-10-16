import { getJson, request } from './http';
import { intlLocale, type Locale } from './i18n';
import type { PlayerPrefs } from './stream';
import { SYNC_NONE, syncValue } from '../player/subsLog';
import { finalCues } from '../player/playRules';

// Ported from web/src/features/watch/subtitles.ts

const ISO639_2: Record<string, string> = {
  eng: 'en', tur: 'tr', spa: 'es', fre: 'fr', fra: 'fr', ger: 'de', deu: 'de', ita: 'it', por: 'pt', rus: 'ru',
  jpn: 'ja', kor: 'ko', chi: 'zh', zho: 'zh', cmn: 'zh', yue: 'zh', ara: 'ar', hin: 'hi', dut: 'nl', nld: 'nl',
  swe: 'sv', nor: 'no', nob: 'nb', nno: 'nn', dan: 'da', fin: 'fi', pol: 'pl', cze: 'cs', ces: 'cs', slo: 'sk',
  slk: 'sk', hun: 'hu', rum: 'ro', ron: 'ro', gre: 'el', ell: 'el', heb: 'he', tha: 'th', vie: 'vi', ind: 'id',
  may: 'ms', msa: 'ms', ukr: 'uk', bul: 'bg', hrv: 'hr', srp: 'sr', slv: 'sl', est: 'et', lav: 'lv', lit: 'lt',
  per: 'fa', fas: 'fa', ice: 'is', isl: 'is', tam: 'ta', tel: 'te', ben: 'bn', urd: 'ur', cat: 'ca', baq: 'eu',
  eus: 'eu', glg: 'gl', fil: 'fil', tgl: 'tl', aze: 'az', kaz: 'kk', geo: 'ka', kat: 'ka', arm: 'hy', hye: 'hy',
  alb: 'sq', sqi: 'sq', mac: 'mk', mkd: 'mk', bos: 'bs', lat: 'la', wel: 'cy', cym: 'cy', gle: 'ga', kur: 'ku',
};

/** ISO 639-1 base code from 639-1/639-2/BCP 47 input; '' for unknown / undetermined. */
export function normLang(code?: string | null): string {
  const c = (code || '').trim().toLowerCase().split(/[-_]/)[0];
  if (!c || c === 'und' || c === 'zxx' || c === 'mul' || c === 'mis') return '';
  return c.length === 3 ? ISO639_2[c] || c : c;
}

// Intl.DisplayNames needs Chromium 81 (webOS 6); older engines use this table
const NAMES: Record<string, [string, string]> = {
  en: ['English', 'İngilizce'], tr: ['Turkish', 'Türkçe'], es: ['Spanish', 'İspanyolca'], fr: ['French', 'Fransızca'],
  de: ['German', 'Almanca'], it: ['Italian', 'İtalyanca'], pt: ['Portuguese', 'Portekizce'], ru: ['Russian', 'Rusça'],
  ja: ['Japanese', 'Japonca'], ko: ['Korean', 'Korece'], zh: ['Chinese', 'Çince'], ar: ['Arabic', 'Arapça'],
  hi: ['Hindi', 'Hintçe'], nl: ['Dutch', 'Felemenkçe'], sv: ['Swedish', 'İsveççe'], no: ['Norwegian', 'Norveççe'],
  da: ['Danish', 'Danca'], fi: ['Finnish', 'Fince'], pl: ['Polish', 'Lehçe'], cs: ['Czech', 'Çekçe'],
  hu: ['Hungarian', 'Macarca'], ro: ['Romanian', 'Rumence'], el: ['Greek', 'Yunanca'], he: ['Hebrew', 'İbranice'],
  th: ['Thai', 'Tayca'], vi: ['Vietnamese', 'Vietnamca'], id: ['Indonesian', 'Endonezce'], uk: ['Ukrainian', 'Ukraynaca'],
  bg: ['Bulgarian', 'Bulgarca'], hr: ['Croatian', 'Hırvatça'], sr: ['Serbian', 'Sırpça'], fa: ['Persian', 'Farsça'],
};

const displayNames: Partial<Record<Locale, { of(code: string): string | undefined } | null>> = {};

export function languageName(locale: Locale, code: string): string {
  if (!code) return '';
  if (displayNames[locale] === undefined) {
    const DN = (Intl as any).DisplayNames;
    try {
      displayNames[locale] = DN ? new DN([intlLocale(locale)], { type: 'language' }) : null;
    } catch {
      displayNames[locale] = null;
    }
  }
  let name = '';
  try {
    name = displayNames[locale]?.of(code) || '';
  } catch {
    /* invalid code */
  }
  if (!name || name === code) name = NAMES[code]?.[locale === 'tr' ? 1 : 0] || code;
  return name.charAt(0).toLocaleUpperCase(intlLocale(locale)) + name.slice(1);
}

// ---- Audio ----
// Same rules as web/src/features/watch/audioPick.ts (keep both in sync)

export interface AudioTrackInfo {
  index: number;
  language: string;
  title: string;
  codec: string;
  channels: number;
  /** sent untouched to this client (probe answer) */
  direct?: boolean;
  /** default disposition in the file (probe answer) */
  default?: boolean;
}

// Words a track title uses for a language ("English 5.1", "Rus MVO")
const LANG_WORDS: Record<string, string[]> = {
  en: ['english', 'eng'], tr: ['turkish', 'türkçe', 'turkce', 'tur'], ru: ['russian', 'rus', 'русский'],
  es: ['spanish', 'español', 'spa'], fr: ['french', 'français', 'fre'], de: ['german', 'deutsch', 'ger'],
  it: ['italian', 'italiano', 'ita'], ja: ['japanese', 'jpn'], ko: ['korean', 'kor'], zh: ['chinese', 'mandarin'],
  hi: ['hindi'], pt: ['portuguese', 'português', 'por'], uk: ['ukrainian', 'ukr'],
};
const ORIGINAL_WORD = /(^|[^a-z])(orig|orij)|оригинал/i;
const DUB_WORD = /(^|[^a-z])(dub|dubbed|mvo|dvo|avo|voice.?over|dublaj)([^a-z]|$)|дубл|многоголос|двухголос|закадр/i;
const COMMENTARY = /comment|yorum|коммент/i;

const words = (title: string) => title.toLowerCase().split(/[\s.,:;()[\]/|+_-]+/).filter(Boolean);

/** A track whose title says it is the original, or names the original language */
function byTitle(tracks: AudioTrackInfo[], orig: string): AudioTrackInfo | undefined {
  const names = LANG_WORDS[orig] || [];
  return tracks.find(t => {
    const title = t.title || '';
    if (!title || COMMENTARY.test(title)) return false;
    return ORIGINAL_WORD.test(title) || words(title).some(w => names.includes(w));
  });
}

/** An untagged ("und") track, usually the original: one without dub words first, the default one first */
function untagged(tracks: AudioTrackInfo[]): AudioTrackInfo | undefined {
  const plain = tracks.filter(t => !normLang(t.language) && !COMMENTARY.test(t.title || ''));
  const clean = plain.filter(t => !DUB_WORD.test(t.title || ''));
  const pool = clean.length ? clean : plain;
  return pool.find(t => t.default) || pool[0];
}

/**
 * Default track, first match wins: the last picked language (not with the 'original'
 * preference), Turkish for a Turkish profile (idem), a track tagged with the original language,
 * one whose title says "Original" or names the original language, an untagged track (usually
 * the original) over ones tagged with another language, the file's default track, the first.
 */
export function pickAudioTrack(
  tracks: AudioTrackInfo[],
  opts: { profileLang: Locale; originalLang?: string; prefLang?: string },
): number {
  if (tracks.length === 0) return 0;
  const find = (lang?: string) => {
    const l = normLang(lang);
    return l ? tracks.find(t => normLang(t.language) === l) : undefined;
  };
  const keepOriginal = opts.prefLang === 'original';
  const orig = normLang(opts.originalLang);
  const pick = (keepOriginal ? undefined : find(opts.prefLang)) ||
    (!keepOriginal && opts.profileLang === 'tr' ? find('tr') : undefined) ||
    find(orig) ||
    byTitle(tracks, orig) ||
    (orig ? untagged(tracks) : undefined) ||
    tracks.find(t => t.default) ||
    tracks[0];
  return pick.index;
}

// ---- Subtitles ----

export interface SubtitleItem {
  id: string;
  source: string;
  language: string;
  label: string;
  release: string;
  hi: boolean;
  forced: boolean;
  synced: boolean;
  score: number;
  /** Embedded track of a big file: read while it plays, its cues grow (fetch with the position) */
  live?: boolean;
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
  title?: string;
  year?: number;
  /** The profile's language: the AI translation target "profile" */
  ui_lang?: string;
}

/** refresh: the server asks the providers again, skipping its caches (late uploads; it limits how often) */
export async function fetchSubtitles(q: SubtitleQuery, opts: { refresh?: boolean } = {}): Promise<SubtitleItem[]> {
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
  try {
    const data = await getJson(`/api/subtitles?${p.toString()}${opts.refresh ? '&refresh=1' : ''}`);
    return Array.isArray(data?.items) ? (data.items as SubtitleItem[]).filter(s => s && s.id) : [];
  } catch {
    return [];
  }
}

export interface Cue {
  start: number;
  end: number;
  text: string;
}

// Parsed subtitle files with final timing, least recently used first (a few files only: they can be large)
const cueCache = new Map<string, Promise<{ cues: Cue[]; sync: string }>>();
const CUE_CACHE_MAX = 5;

function rememberCues(id: string, cues: Cue[], sync: string) {
  cueCache.delete(id);
  cueCache.set(id, Promise.resolve({ cues, sync }));
  while (cueCache.size > CUE_CACHE_MAX) cueCache.delete(cueCache.keys().next().value as string);
}

// The server marks a partial live answer with this note (more cues follow while the file plays)
const PARTIAL_NOTE = 'NOTE nextflix-partial';
// ...and an AI translation with this one while the lines at the position are being translated
const AI_NOTE = 'NOTE nextflix-ai-translating';
// Late uploads: while the profile's language is missing the list is asked again this often, bypassing caches
export const LATE_SUBTITLE_POLL_MS = 10 * 60 * 1000;
// How often the player asks again for a release with the profile's subtitle while the server scans
export const SUBALT_RETRY_MAX = 4;

export function fetchCues(id: string): Promise<Cue[]> {
  return fetchCueFile(id).then(f => f.cues);
}

/**
 * Cues of a subtitle. `at` (seconds of the file) is where the viewer is: a live embedded track is read from
 * there on. Only a final answer is cached (finalCues); ask again (cuePollMs) for the cues read or timed since.
 */
export interface CueFile {
  cues: Cue[];
  partial: boolean;
  /** X-Subtitle-Sync for the playback log ("hidden" when the browser hides it, see subsLog.ts) */
  sync: string;
  /** AI translation: the lines at the position are still being translated */
  translating?: boolean;
  /** No cues because of a server or network error (often over in seconds), not a missing file */
  failed?: 'error';
}

export function fetchCueFile(id: string, at?: number): Promise<CueFile> {
  const hit = cueCache.get(id);
  if (hit) {
    // Most recently used goes last
    cueCache.delete(id);
    cueCache.set(id, hit);
    return hit.then(c => ({ cues: c.cues, partial: false, sync: c.sync }));
  }
  const t = at !== undefined && at > 0 ? `&t=${Math.floor(at)}` : '';
  const req = request(`/api/subtitles/file?id=${encodeURIComponent(id)}${t}`)
    .then(async res => {
      const sync = syncValue(res.headers.get('X-Subtitle-Sync'), res.type === 'cors');
      const text = res.ok ? await res.text() : '';
      const f: CueFile = { cues: parseVtt(text), partial: text.indexOf(PARTIAL_NOTE) >= 0, sync, translating: text.indexOf(AI_NOTE) >= 0 };
      if (res.status >= 500) f.failed = 'error';
      return f;
    })
    .catch((): CueFile => ({ cues: [], partial: false, sync: SYNC_NONE, failed: 'error' }));
  req.then(f => {
    if (f.cues.length > 0 && finalCues(f)) rememberCues(id, f.cues, f.sync);
  });
  return req;
}

// An AI translation does not count: a real subtitle that turns up later still replaces it
export const hasSubtitleLang = (items: SubtitleItem[], lang: string) =>
  items.some(s => s.source !== 'ai' && normLang(s.language) === normLang(lang));

/** Both lists by id, the newer answer's entries first */
export function mergeSubtitles(prev: SubtitleItem[], next: SubtitleItem[]): SubtitleItem[] {
  const ids = new Set(next.map(s => s.id));
  return [...next, ...prev.filter(s => !ids.has(s.id))];
}

/** Best subtitle of lang (not forced, synced first). */
export function bestSubtitleOf(items: SubtitleItem[], lang: string): string | null {
  const l = normLang(lang);
  const of = items.filter(s => normLang(s.language) === l);
  const regular = of.filter(s => !s.forced);
  const pool = regular.length > 0 ? regular : of;
  return (pool.find(s => s.synced) || pool[0])?.id ?? null;
}

/**
 * A later list brought subtitles in lang that the shown list lacked: the merged list, whether lang is new,
 * and the subtitle to show by itself when the viewer has not chosen one. Same rules as the web player.
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
  const pool = items.filter(s => normLang(s.language) === l && !s.forced);
  const best = pool.find(s => s.synced) || pool[0];
  return { items, arrived, pick: best ? best.id : null };
}

const TIME = /(?:(\d+):)?(\d{1,2}):(\d{2})[.,](\d{1,3})/;

function parseTime(s: string): number | null {
  const m = s.match(TIME);
  if (!m) return null;
  const ms = m[4].padEnd(3, '0');
  return Number(m[1] || 0) * 3600 + Number(m[2]) * 60 + Number(m[3]) + Number(ms) / 1000;
}

export function parseVtt(text: string): Cue[] {
  const cues: Cue[] = [];
  const blocks = text.replace(/^﻿/, '').replace(/\r\n?/g, '\n').split(/\n{2,}/);
  for (const block of blocks) {
    const lines = block.split('\n');
    const at = lines.findIndex(l => l.indexOf('-->') >= 0);
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

/** Cues showing at time t (cues sorted by start). */
export function activeCues(cues: Cue[], t: number): Cue[] {
  let lo = 0;
  let hi = cues.length - 1;
  let last = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (cues[mid].start <= t) { last = mid; lo = mid + 1; } else hi = mid - 1;
  }
  const out: Cue[] = [];
  for (let i = last; i >= 0 && i > last - 12; i--) {
    if (cues[i].end > t) out.unshift(cues[i]);
  }
  return out;
}

/** Last choice of the viewer; without one a Turkish profile gets Turkish unless the audio is Turkish. */
export function pickSubtitle(
  items: SubtitleItem[],
  opts: { profileLang: Locale; pref?: PlayerPrefs['sub']; audioLang?: string },
): string | null {
  const best = (lang: string) => {
    const l = normLang(lang);
    const of = items.filter(s => normLang(s.language) === l && !s.forced);
    const pool = of.length > 0 ? of : items.filter(s => normLang(s.language) === l);
    return (pool.find(s => s.synced) || pool[0])?.id ?? null;
  };
  const pref = opts.pref;
  if (pref === 'off') return null;
  if (pref) {
    if (pref.id && items.some(s => s.id === pref.id)) return pref.id;
    return best(pref.lang);
  }
  if (opts.profileLang === 'tr' && normLang(opts.audioLang) !== 'tr') return best('tr');
  return null;
}

/** One row per language (its best subtitle), the requested languages first. */
export function bestPerLanguage(items: SubtitleItem[], order: string[], locale: Locale): SubtitleItem[] {
  const rank = (s: SubtitleItem) => {
    const i = order.indexOf(normLang(s.language));
    return i >= 0 ? i : order.length;
  };
  const sorted = items
    .map((s, i) => ({ s, i }))
    .sort((a, b) =>
      rank(a.s) - rank(b.s) ||
      languageName(locale, normLang(a.s.language)).localeCompare(languageName(locale, normLang(b.s.language))) ||
      a.i - b.i)
    .map(x => x.s);
  const seen = new Set<string>();
  return sorted.filter(s => {
    const code = normLang(s.language) || s.language;
    if (seen.has(code)) return false;
    seen.add(code);
    return true;
  });
}

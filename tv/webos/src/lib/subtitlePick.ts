// Which subtitle shows by default (tested in subtitlePick.test.mjs). The server sends each language's
// subtitles best first (server/internal/modules/subtitles/ranking.go: complete ones ahead of synced ones
// with known holes), so a default pick keeps that order.

import type { Locale } from './i18n';
import type { PlayerPrefs } from './stream';

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

// An AI translation does not count: a real subtitle that turns up later still replaces it
export const hasSubtitleLang = (items: SubtitleItem[], lang: string) =>
  items.some(s => s.source !== 'ai' && normLang(s.language) === normLang(lang));

/** Both lists by id, the newer answer's entries first */
export function mergeSubtitles(prev: SubtitleItem[], next: SubtitleItem[]): SubtitleItem[] {
  const ids = new Set(next.map(s => s.id));
  return [...next, ...prev.filter(s => !ids.has(s.id))];
}

/** Best subtitle of lang: the first one not forced (a forced one only when the language has nothing else). */
export function bestSubtitleOf(items: SubtitleItem[], lang: string): string | null {
  const l = normLang(lang);
  const of = items.filter(s => normLang(s.language) === l);
  const regular = of.filter(s => !s.forced);
  const pool = regular.length > 0 ? regular : of;
  return pool[0]?.id ?? null;
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
  const best = items.find(s => normLang(s.language) === l && !s.forced);
  return { items, arrived, pick: best ? best.id : null };
}

/** Last choice of the viewer; without one a Turkish profile gets Turkish unless the audio is Turkish. */
export function pickSubtitle(
  items: SubtitleItem[],
  opts: { profileLang: Locale; pref?: PlayerPrefs['sub']; audioLang?: string },
): string | null {
  const pref = opts.pref;
  if (pref === 'off') return null;
  if (pref) {
    if (pref.id && items.some(s => s.id === pref.id)) return pref.id;
    return bestSubtitleOf(items, pref.lang);
  }
  if (opts.profileLang === 'tr' && normLang(opts.audioLang) !== 'tr') return bestSubtitleOf(items, 'tr');
  return null;
}

// The last home answers in localStorage: the next launch paints them at once while loading.
import { base } from './http';
import { readJson, writeJson } from './storage';
import type { HomeData, RecommendationRow, Title } from './catalog';
import type { Locale } from './i18n';
import type { StreamProgress } from './stream';

const MAX_AGE = 7 * 24 * 60 * 60 * 1000;

export interface PersonalSnapshot {
  cw?: StreamProgress[];
  recs?: RecommendationRow[];
  favs?: Title[];
}

const homeKey = (locale: Locale) => `nfx-tv:home:v1:${base()}:${locale}`;
const personalKey = (pid: number, locale: Locale) => `nfx-tv:home-p:v1:${base()}:${pid}:${locale}`;

function readFresh<T>(key: string): (T & { at: number }) | undefined {
  const s = readJson<T & { at: number }>(key);
  if (!s || typeof s !== 'object' || typeof s.at !== 'number') return undefined;
  const age = Date.now() - s.at;
  return age >= 0 && age < MAX_AGE ? s : undefined;
}

export function readHomeSnapshot(locale: Locale): HomeData | undefined {
  const s = readFresh<{ data: HomeData }>(homeKey(locale));
  const d = s && s.data;
  return d && typeof d === 'object' && d.genres && typeof d.genres === 'object' ? d : undefined;
}

export function writeHomeSnapshot(locale: Locale, data: HomeData): void {
  writeJson(homeKey(locale), { at: Date.now(), data });
}

export function readPersonal(pid: number, locale: Locale): PersonalSnapshot {
  return (pid && readFresh<PersonalSnapshot>(personalKey(pid, locale))) || {};
}

export function writePersonal(pid: number, locale: Locale, part: PersonalSnapshot): void {
  if (!pid) return;
  writeJson(personalKey(pid, locale), { ...readPersonal(pid, locale), ...part, at: Date.now() });
}

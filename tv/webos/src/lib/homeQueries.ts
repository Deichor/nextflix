// The per-profile home queries, shared by Home and the profile picker's prefetch (same keys and loaders).
import { cached, onInvalidate, scopedFor } from './http';
import { favorites, homeCached, recommendations, type RecommendationRow, type Title } from './catalog';
import { readPersonal, writePersonal, type PersonalSnapshot } from './homeSnapshot';
import type { Locale } from './i18n';
import { getProfileId, profileLocale, type Profile } from './profile';
import { progressList, type StreamProgress } from './stream';

const SEC = 1000;
const MIN = 60 * SEC;
const PARTS = ['cw', 'recs', 'favs'] as const;

// A list invalidated after a rating, favourite or playback is not painted from the old snapshot
onInvalidate(match => {
  const pid = getProfileId();
  if (!pid) return;
  const locale = profileLocale();
  const drop: PersonalSnapshot = {};
  PARTS.forEach(part => { if (scopedFor(pid, locale, part).includes(match)) drop[part] = undefined; });
  if (Object.keys(drop).length > 0) writePersonal(pid, locale, drop);
});

export interface PersonalQuery<T> {
  key: string;
  ttl: number;
  load: () => Promise<T>;
  initial: () => T | undefined;
}

function query<K extends keyof PersonalSnapshot>(
  pid: number,
  locale: Locale,
  part: K,
  ttl: number,
  get: () => Promise<NonNullable<PersonalSnapshot[K]>>,
): PersonalQuery<NonNullable<PersonalSnapshot[K]>> {
  return {
    key: scopedFor(pid, locale, part),
    ttl,
    load: () => get().then(data => {
      writePersonal(pid, locale, { [part]: data } as PersonalSnapshot);
      return data;
    }),
    initial: () => {
      const v = readPersonal(pid, locale)[part];
      return Array.isArray(v) ? (v as NonNullable<PersonalSnapshot[K]>) : undefined;
    },
  };
}

export function personalQueries(pid: number, locale: Locale): {
  cw: PersonalQuery<StreamProgress[]>;
  recs: PersonalQuery<RecommendationRow[]>;
  favs: PersonalQuery<Title[]>;
} {
  return {
    cw: query(pid, locale, 'cw', 15 * SEC, () => progressList(pid, locale)),
    recs: query(pid, locale, 'recs', 5 * MIN, () => recommendations(pid, locale)),
    favs: query(pid, locale, 'favs', MIN, () => favorites(pid, locale)),
  };
}

const ignore = () => {};

/** Starts the profile-independent home lists (the first answer can take a while on a cold server). */
export function prefetchHome(locale: Locale): void {
  homeCached(locale).catch(ignore);
}

/** Starts everything Home loads for `p`; Home then shares the requests in flight and the answers. */
export function prefetchProfile(p: Profile): void {
  const locale = profileLocale(p);
  prefetchHome(locale);
  const q = personalQueries(p.id, locale);
  cached(q.cw.key, q.cw.ttl, q.cw.load).catch(ignore);
  cached(q.recs.key, q.recs.ttl, q.recs.load).catch(ignore);
  cached(q.favs.key, q.favs.ttl, q.favs.load).catch(ignore);
}

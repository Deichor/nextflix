"use client";

import { getApiUrl } from '@/shared/lib/api';
import { getActiveProfileId, profileHeaders } from '@/shared/lib/profile';
import type { Locale, MessageKey, TFunction } from '@/shared/i18n';
import { localGenre, type NfMediaType } from '@/features/catalog/titles';

export type SurpriseReasonKind =
  | 'because_loved'
  | 'because_liked'
  | 'because_watched'
  | 'favorite_like'
  | 'genre'
  | 'adjacent'
  | 'wild_card'
  | 'trending_pick';

export interface SurprisePick {
  id: number;
  mediaType: NfMediaType;
  title: string;
  overview: string;
  posterPath: string;
  backdropPath: string;
  logoPath: string;
  date: string;
  rating: number;
  genreIds: number[];
  originalLanguage?: string;
  reason: {
    kind: SurpriseReasonKind;
    bucket?: string;
    title?: string;
    mediaType?: NfMediaType;
    genreId?: number;
    genreIds?: number[];
    language?: string;
  };
}

// The last picks of this tab, sent as exclude= so "Something else" never repeats them
// (the server also remembers the last 30 per profile)
const MEMORY = 30;
const memoryKey = (profileId: number) => `nfx-surprise-recent:${profileId}`;

function recent(profileId: number): string[] {
  try {
    const raw = sessionStorage.getItem(memoryKey(profileId));
    const list = raw ? JSON.parse(raw) : [];
    return Array.isArray(list) ? list.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

function remember(profileId: number, key: string) {
  try {
    const list = [...recent(profileId).filter(k => k !== key), key].slice(-MEMORY);
    sessionStorage.setItem(memoryKey(profileId), JSON.stringify(list));
  } catch {
    /* storage unavailable */
  }
}

/** One title the active profile is likely to enjoy; null when the server has nothing. */
export async function fetchSurprise(signal?: AbortSignal): Promise<SurprisePick | null> {
  const profileId = getActiveProfileId();
  if (!profileId) return null;
  const params = new URLSearchParams({ profile_id: String(profileId) });
  const exclude = recent(profileId);
  if (exclude.length > 0) params.set('exclude', exclude.join(','));
  const res = await fetch(`${getApiUrl()}/api/recommendations/surprise?${params}`, {
    headers: profileHeaders(),
    cache: 'no-store',
    signal,
  });
  if (!res.ok) return null;
  const data = await res.json();
  const t = data?.title;
  if (!t || typeof t.id !== 'number') return null;
  const mediaType: NfMediaType = t.media_type === 'tv' ? 'tv' : 'movie';
  remember(profileId, `${mediaType}-${t.id}`);
  const r = data.reason || {};
  return {
    id: t.id,
    mediaType,
    title: t.title || '',
    overview: t.overview || '',
    posterPath: t.poster_path || '',
    backdropPath: t.backdrop_path || '',
    logoPath: t.logo_path || '',
    date: t.release_date || t.first_air_date || '',
    rating: Number(t.vote_average) || 0,
    genreIds: Array.isArray(t.genre_ids) ? t.genre_ids : [],
    originalLanguage: typeof t.original_language === 'string' ? t.original_language : undefined,
    reason: {
      kind: r.kind || 'trending_pick',
      title: r.title || undefined,
      mediaType: r.media_type === 'tv' ? 'tv' : r.media_type === 'movie' ? 'movie' : undefined,
      genreId: typeof r.genre_id === 'number' ? r.genre_id : undefined,
      genreIds: Array.isArray(r.genre_ids) ? r.genre_ids.filter((x: unknown): x is number => typeof x === 'number') : undefined,
      language: typeof r.language === 'string' ? r.language : undefined,
      bucket: typeof r.bucket === 'string' ? r.bucket : undefined,
    },
  };
}

const SEED_KEYS: Partial<Record<SurpriseReasonKind, [MessageKey, MessageKey]>> = {
  because_loved: ['surprise.reason.lovedMovie', 'surprise.reason.lovedTv'],
  because_liked: ['surprise.reason.likedMovie', 'surprise.reason.likedTv'],
  because_watched: ['surprise.reason.watchedMovie', 'surprise.reason.watchedTv'],
  favorite_like: ['surprise.reason.favoriteMovie', 'surprise.reason.favoriteTv'],
};

const LANGUAGES = ['ko', 'ja', 'es', 'fr', 'it', 'tr', 'hi', 'de', 'zh', 'da', 'sv', 'pt', 'ru', 'fa'];

const BACK_VOWELS = 'aıouâû';
const FRONT_VOWELS = 'eiöüî';

/** Turkish locative suffix with vowel harmony: "Bilim kurgu" -> "Bilim kurguda", "Suç" -> "Suçta". */
export function trLocative(word: string): string {
  const lower = word.toLocaleLowerCase('tr-TR');
  let back = true;
  for (let i = lower.length - 1; i >= 0; i--) {
    if (BACK_VOWELS.includes(lower[i])) break;
    if (FRONT_VOWELS.includes(lower[i])) { back = false; break; }
  }
  const hard = 'çfhkpsşt'.includes(lower[lower.length - 1] || '');
  return `${word}${hard ? 't' : 'd'}${back ? 'a' : 'e'}`;
}

/** "Because you loved Dune" / "Bayıldığın Dune filmine benzediği için", "Something completely different – ...". */
export function reasonText(t: TFunction, locale: Locale, pick: Pick<SurprisePick, 'reason' | 'mediaType' | 'genreIds'>): string {
  const { reason } = pick;
  const lower = (s: string) => s.toLocaleLowerCase(locale === 'tr' ? 'tr-TR' : 'en-US');
  // English names genres in lower case; Turkish starts the sentence with the first one
  const lead = (s: string) => (locale === 'tr' ? s.charAt(0).toLocaleUpperCase('tr-TR') + lower(s.slice(1)) : lower(s));
  const seed = SEED_KEYS[reason.kind];
  if (seed && reason.title) return t(seed[reason.mediaType === 'tv' ? 1 : 0], { title: reason.title });
  if (reason.kind === 'genre') {
    const names = (reason.genreIds?.length ? reason.genreIds : reason.genreId ? [reason.genreId] : [])
      .map(id => localGenre(t, id))
      .filter((x): x is string => !!x);
    if (names.length >= 2) return t('surprise.reason.genres', { first: lead(names[0]), second: lower(names[1]) });
    if (names.length === 1) return t('surprise.reason.genre', { genre: lead(names[0]) });
  }
  if (reason.kind === 'adjacent' && reason.genreId) {
    const genre = localGenre(t, reason.genreId);
    if (genre) return t('surprise.reason.adjacent', { genre: lower(genre), genreIn: trLocative(lead(genre)) });
  }
  if (reason.kind === 'wild_card') {
    if (reason.language === 'ja' && pick.genreIds.includes(16)) return t('surprise.reason.wildAnime');
    if (reason.language && LANGUAGES.includes(reason.language)) {
      const language = t(`surprise.lang.${reason.language}` as MessageKey);
      return t(pick.mediaType === 'tv' ? 'surprise.reason.wildTv' : 'surprise.reason.wildMovie', { language });
    }
    return t('surprise.reason.wild');
  }
  return t('surprise.reason.trending');
}

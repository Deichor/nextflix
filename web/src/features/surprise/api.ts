"use client";

import { getApiUrl } from '@/shared/lib/api';
import { getActiveProfileId, profileHeaders } from '@/shared/lib/profile';
import type { MessageKey, TFunction } from '@/shared/i18n';
import { localGenre, type NfMediaType } from '@/features/catalog/titles';

export type SurpriseReasonKind =
  | 'because_loved'
  | 'because_liked'
  | 'because_watched'
  | 'favorite_like'
  | 'genre'
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
  reason: { kind: SurpriseReasonKind; title?: string; mediaType?: NfMediaType; genreId?: number };
}

// The last picks of this tab, sent as exclude= so "Something else" never repeats them
// (the server also remembers the last 20 per profile)
const MEMORY = 20;
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
    },
  };
}

const SEED_KEYS: Partial<Record<SurpriseReasonKind, [MessageKey, MessageKey]>> = {
  because_loved: ['surprise.reason.lovedMovie', 'surprise.reason.lovedTv'],
  because_liked: ['surprise.reason.likedMovie', 'surprise.reason.likedTv'],
  because_watched: ['surprise.reason.watchedMovie', 'surprise.reason.watchedTv'],
  favorite_like: ['surprise.reason.favoriteMovie', 'surprise.reason.favoriteTv'],
};

/** "Because you loved Dune" / "Bayıldığın Dune filmine benzediği için". */
export function reasonText(t: TFunction, reason: SurprisePick['reason']): string {
  const seed = SEED_KEYS[reason.kind];
  if (seed && reason.title) return t(seed[reason.mediaType === 'tv' ? 1 : 0], { title: reason.title });
  if (reason.kind === 'genre' && reason.genreId) {
    const genre = localGenre(t, reason.genreId);
    if (genre) return t('surprise.reason.genre', { genre });
  }
  return t('surprise.reason.trending');
}

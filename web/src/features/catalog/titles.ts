"use client";

import { contentLang, getApiUrl } from '@/shared/lib/api';
import { pickLogo, tmdbImage } from '@/shared/lib/image';
import { en, type MessageKey, type TFunction } from '@/shared/i18n';

export type NfMediaType = 'movie' | 'tv';

// One card-able title, normalized from TMDB results
export interface NfTitle {
  key: string;
  id: number;
  mediaType: NfMediaType;
  title: string;
  overview?: string;
  poster?: string;
  backdrop?: string;
  posterPath?: string;
  backdropPath?: string;
  year?: number;
  rating?: number;
  genreIds?: number[];
  genreNames?: string[];
  href: string;
  /** Opens playback directly via the detail page's ?play=1 */
  playHref?: string;
}

export interface NfGenre {
  id: number;
  name: string;
}

export const MOVIE_GENRES: NfGenre[] = [
  { id: 28, name: 'Action' },
  { id: 12, name: 'Adventure' },
  { id: 16, name: 'Animation' },
  { id: 35, name: 'Comedy' },
  { id: 80, name: 'Crime' },
  { id: 99, name: 'Documentary' },
  { id: 18, name: 'Drama' },
  { id: 10751, name: 'Family' },
  { id: 14, name: 'Fantasy' },
  { id: 36, name: 'History' },
  { id: 27, name: 'Horror' },
  { id: 10402, name: 'Musical' },
  { id: 9648, name: 'Mystery' },
  { id: 10749, name: 'Romance' },
  { id: 878, name: 'Sci-Fi' },
  { id: 53, name: 'Thriller' },
  { id: 10752, name: 'War' },
  { id: 37, name: 'Western' },
];

export const TV_GENRES: NfGenre[] = [
  { id: 10759, name: 'Action & Adventure' },
  { id: 16, name: 'Animation' },
  { id: 35, name: 'Comedy' },
  { id: 80, name: 'Crime' },
  { id: 99, name: 'Documentary' },
  { id: 18, name: 'Drama' },
  { id: 10751, name: 'Family' },
  { id: 10762, name: 'Kids' },
  { id: 9648, name: 'Mystery' },
  { id: 10764, name: 'Reality' },
  { id: 10765, name: 'Sci-Fi & Fantasy' },
  { id: 10768, name: 'War & Politics' },
  { id: 37, name: 'Western' },
];

const GENRE_NAMES: Record<number, string> = Object.fromEntries(
  [...MOVIE_GENRES, ...TV_GENRES].map(g => [g.id, g.name])
);

export function genreName(id: number): string | undefined {
  return GENRE_NAMES[id];
}

/** Genre name in the UI language (keys 'genre.<tmdb id>'), English table as fallback. */
export function localGenre(t: TFunction, id: number): string | undefined {
  const key = `genre.${id}`;
  return key in en ? t(key as MessageKey) : GENRE_NAMES[id];
}

export function localGenres(t: TFunction, ids: number[] | undefined, max = 3): string[] {
  return (ids || []).map(id => localGenre(t, id)).filter((x): x is string => !!x).slice(0, max);
}

/** Image URL that always goes through the /tmdb-img proxy. */
export const imageUrl = tmdbImage;

/** Raw TMDB path ("/abc.jpg") from either a path or a full TMDB URL. */
function rawTmdbPath(path: string | null | undefined): string | undefined {
  if (!path) return undefined;
  const m = path.match(/\/t\/p\/[^/]+(\/.+)$/);
  if (m) return m[1];
  return path.startsWith('/') ? path : undefined;
}

function yearOf(date?: string | null): number | undefined {
  if (!date) return undefined;
  const y = parseInt(date.slice(0, 4), 10);
  return Number.isFinite(y) && y > 1800 ? y : undefined;
}

export function tmdbDetailHref(id: number, type: NfMediaType, play = false): string {
  return `/tmdb-movie/${id}?type=${type}${play ? '&play=1' : ''}`;
}

/** Normalizes TMDB list / search / discover items (movie or tv). */
export function fromTmdb(item: any, fallbackType: NfMediaType = 'movie'): NfTitle | null {
  if (!item || !item.id) return null;
  const mediaType: NfMediaType =
    item.media_type === 'tv' || item.media_type === 'movie'
      ? item.media_type
      : item.name && !item.title
        ? 'tv'
        : fallbackType;
  const title = item.title || item.name || item.original_title || item.original_name;
  if (!title) return null;
  const posterPath = rawTmdbPath(item.poster_path);
  const backdropPath = rawTmdbPath(item.backdrop_path);
  const genreIds: number[] = Array.isArray(item.genre_ids)
    ? item.genre_ids
    : Array.isArray(item.genres)
      ? item.genres.map((g: any) => g?.id).filter((x: any) => typeof x === 'number')
      : [];
  return {
    key: `${mediaType}-${item.id}`,
    id: item.id,
    mediaType,
    title,
    overview: item.overview || '',
    posterPath,
    backdropPath,
    poster: imageUrl(item.poster_path, 'w342'),
    backdrop: imageUrl(item.backdrop_path, 'w780'),
    year: yearOf(item.release_date || item.first_air_date),
    rating: typeof item.vote_average === 'number' ? item.vote_average : undefined,
    genreIds,
    genreNames: genreIds.map(genreName).filter((x): x is string => !!x),
    href: tmdbDetailHref(item.id, mediaType),
    playHref: tmdbDetailHref(item.id, mediaType, true),
  };
}

export function fromTmdbList(items: any[] | null | undefined, fallbackType: NfMediaType = 'movie'): NfTitle[] {
  const seen = new Set<string>();
  const out: NfTitle[] = [];
  for (const it of items || []) {
    const t = fromTmdb(it, fallbackType);
    if (!t || seen.has(t.key)) continue;
    seen.add(t.key);
    out.push(t);
  }
  return out;
}

/** Netflix-like "Match" from the TMDB vote average (0-10). */
export function matchPercent(rating?: number): number | null {
  if (!rating || rating <= 0) return null;
  return Math.max(1, Math.min(99, Math.round(rating * 10)));
}

// ---- Fetching (small in-memory cache so moving between pages stays fast) ----

const cache = new Map<string, { at: number; data: any }>();
const inflight = new Map<string, Promise<any>>();
const TTL = 10 * 60 * 1000;

export async function getJson<T = any>(path: string, opts: { cache?: boolean } = {}): Promise<T | null> {
  const useCache = opts.cache !== false;
  // Answers are in the content language (X-Lang), so cache them per language
  const key = `${contentLang()}|${path}`;
  const hit = cache.get(key);
  if (useCache && hit && Date.now() - hit.at < TTL) return hit.data as T;
  const pending = inflight.get(key);
  if (useCache && pending) return pending;
  const p = (async () => {
    try {
      const res = await fetch(`${getApiUrl()}${path}`);
      if (!res.ok) return null;
      const data = await res.json();
      if (useCache) cache.set(key, { at: Date.now(), data });
      return data as T;
    } catch {
      return null;
    } finally {
      inflight.delete(key);
    }
  })();
  if (useCache) inflight.set(key, p);
  return p;
}

export interface MovieLists {
  trendingDaily: NfTitle[];
  trendingWeekly: NfTitle[];
  nowPlaying: NfTitle[];
  upcoming: NfTitle[];
}

export async function fetchMovieLists(): Promise<MovieLists> {
  const d = await getJson<any>('/api/upcoming-movies');
  return {
    trendingDaily: fromTmdbList(d?.trending_daily, 'movie'),
    trendingWeekly: fromTmdbList(d?.trending_weekly, 'movie'),
    nowPlaying: fromTmdbList(d?.now_playing, 'movie'),
    upcoming: fromTmdbList(d?.upcoming, 'movie'),
  };
}

export interface TvLists {
  airingToday: NfTitle[];
  onTheAir: NfTitle[];
  trendingDaily: NfTitle[];
  trendingWeekly: NfTitle[];
}

export async function fetchTvLists(): Promise<TvLists> {
  const d = await getJson<any>('/api/upcoming-tv-series');
  return {
    airingToday: fromTmdbList(d?.airing_today, 'tv'),
    onTheAir: fromTmdbList(d?.on_the_air, 'tv'),
    trendingDaily: fromTmdbList(d?.trending_daily, 'tv'),
    trendingWeekly: fromTmdbList(d?.trending_weekly, 'tv'),
  };
}

export async function fetchMovieList(endpoint: 'popular' | 'now-playing' | 'upcoming', page = 1): Promise<NfTitle[]> {
  const d = await getJson<any>(`/api/tmdb/movie/${endpoint}?page=${page}`);
  return fromTmdbList(d?.results, 'movie');
}

export async function discoverMovies(params: Record<string, string | number>): Promise<NfTitle[]> {
  const qs = new URLSearchParams(Object.entries(params).map(([k, v]) => [k, String(v)])).toString();
  const d = await getJson<any>(`/api/tmdb/discover/movie?${qs}`);
  return fromTmdbList(d?.results, 'movie');
}

/** TV shows available on a streaming provider (8 = Netflix, 337 = Disney+, 9 = Prime Video, 1899 = Max). */
export async function fetchProviderTv(providerId: number, sortBy = 'popularity.desc', page = 1): Promise<NfTitle[]> {
  const d = await getJson<any>(`/api/providers/${providerId}/tv?sort_by=${encodeURIComponent(sortBy)}&page=${page}`);
  return fromTmdbList(d?.results, 'tv');
}

export async function fetchRelated(id: number, type: NfMediaType, limit = 12, year?: number): Promise<NfTitle[]> {
  const y = year ? `&year=${year}` : '';
  const d = await getJson<any>(`/api/tmdb/${id}/related?type=${type}&limit=${limit}${y}`);
  return fromTmdbList(d?.results, type);
}

/** Logo for a TMDB title in the content language (else English, language-less, first), proxied. */
export async function fetchLogo(id: number, type: NfMediaType): Promise<string | null> {
  // The backend only exposes images for movies
  if (type !== 'movie') return null;
  const d = await getJson<any>(`/api/tmdb/movie/${id}/images`);
  const logos: any[] = d?.logos || [];
  const best = pickLogo(logos, contentLang()) || logos[0];
  return best?.file_path ? imageUrl(best.file_path, 'w500') || null : null;
}

/** Merges lists, keeping the first occurrence of each title. */
export function uniqueTitles(...lists: NfTitle[][]): NfTitle[] {
  const seen = new Set<string>();
  const out: NfTitle[] = [];
  for (const list of lists) {
    for (const t of list) {
      if (seen.has(t.key)) continue;
      seen.add(t.key);
      out.push(t);
    }
  }
  return out;
}

/** Best billboard candidate: has a backdrop and an overview. */
export function pickBillboard(list: NfTitle[], rotate = true): NfTitle | null {
  const good = list.filter(t => t.backdropPath && (t.overview || '').length > 40).slice(0, 8);
  if (good.length === 0) return list.find(t => t.backdrop) || null;
  return rotate ? good[Math.floor(Math.random() * good.length)] : good[0];
}

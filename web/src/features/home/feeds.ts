import { contentLang, getApiUrl } from '@/shared/lib/api';
import { pickLogo, tmdbImage } from '@/shared/lib/image';

export type TitleType = 'movie' | 'tv';

// A TMDB movie or TV show, normalized for rows and the billboard
export interface TitleItem {
  id: number;
  media_type: TitleType;
  title: string;
  overview: string;
  poster_path: string;
  backdrop_path: string;
  date: string;
  vote_average: number;
  genre_ids: number[];
  /** TMDB original_language (ISO 639-1), picks the default audio track */
  original_language?: string;
  /** Netflix Top 10 rank (ranked rows only) */
  rank?: number;
}

export function toTitle(raw: any, fallbackType: TitleType): TitleItem | null {
  if (!raw || typeof raw.id !== 'number') return null;
  const type: TitleType = raw.media_type === 'tv' || raw.media_type === 'movie' ? raw.media_type : fallbackType;
  const title = raw.title || raw.name || raw.original_title || raw.original_name || '';
  if (!title) return null;
  return {
    id: raw.id,
    media_type: type,
    title,
    overview: raw.overview || '',
    poster_path: raw.poster_path || '',
    backdrop_path: raw.backdrop_path || '',
    date: raw.release_date || raw.first_air_date || '',
    vote_average: Number(raw.vote_average) || 0,
    genre_ids: Array.isArray(raw.genre_ids) ? raw.genre_ids : [],
    original_language: typeof raw.original_language === 'string' ? raw.original_language : undefined,
  };
}

export function toTitles(list: any, type: TitleType): TitleItem[] {
  if (!Array.isArray(list)) return [];
  const seen = new Set<string>();
  const out: TitleItem[] = [];
  for (const raw of list) {
    const t = toTitle(raw, type);
    if (!t || (!t.backdrop_path && !t.poster_path)) continue;
    const key = `${t.media_type}-${t.id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(t);
  }
  return out;
}

// Alternates two lists (e.g. trending movies and shows) without duplicates
export function interleave(a: TitleItem[], b: TitleItem[], max = 20): TitleItem[] {
  const out: TitleItem[] = [];
  for (let i = 0; out.length < max && (i < a.length || i < b.length); i++) {
    if (i < a.length) out.push(a[i]);
    if (i < b.length && out.length < max) out.push(b[i]);
  }
  return out;
}

export function yearOf(t: TitleItem): number | undefined {
  const y = parseInt(t.date.slice(0, 4), 10);
  return Number.isFinite(y) && y > 0 ? y : undefined;
}

export function detailHref(t: { id: number; media_type: TitleType }): string {
  return `/tmdb-movie/${t.id}?type=${t.media_type}`;
}

export function matchPercent(t: TitleItem): number {
  return Math.round(Math.min(10, Math.max(0, t.vote_average)) * 10);
}


async function getJson(path: string, signal?: AbortSignal): Promise<any> {
  const res = await fetch(`${getApiUrl()}${path}`, { signal });
  if (!res.ok) throw new Error(`${path} -> ${res.status}`);
  return res.json();
}

// Combined TMDB lists the backend caches (one request each)
export interface HomeFeeds {
  trendingMovies: TitleItem[];
  trendingWeekMovies: TitleItem[];
  upcomingMovies: TitleItem[];
  trendingTv: TitleItem[];
  popularTv: TitleItem[];
  onTheAirTv: TitleItem[];
}

export async function fetchHomeFeeds(signal?: AbortSignal): Promise<HomeFeeds> {
  const [movies, tv] = await Promise.all([
    getJson('/api/upcoming-movies', signal).catch(() => null),
    getJson('/api/upcoming-tv-series', signal).catch(() => null),
  ]);
  return {
    trendingMovies: toTitles(movies?.trending_daily, 'movie'),
    trendingWeekMovies: toTitles(movies?.trending_weekly, 'movie'),
    upcomingMovies: toTitles(movies?.upcoming, 'movie'),
    trendingTv: toTitles(tv?.trending_daily, 'tv'),
    popularTv: toTitles(tv?.trending_weekly, 'tv'),
    onTheAirTv: toTitles(tv?.on_the_air, 'tv'),
  };
}

export async function fetchPopularMovies(signal?: AbortSignal): Promise<TitleItem[]> {
  const data = await getJson('/api/tmdb/movie/popular', signal);
  return toTitles(data?.results, 'movie');
}

export async function discoverMovies(params: Record<string, string>, signal?: AbortSignal): Promise<TitleItem[]> {
  const q = new URLSearchParams({ sort_by: 'popularity.desc', include_adult: 'false', ...params });
  const data = await getJson(`/api/tmdb/discover/movie?${q.toString()}`, signal);
  return toTitles(data?.results, 'movie');
}

// Movie title logo in the content language (else English or language-less), or '' when there is none
export async function fetchMovieLogo(id: number, signal?: AbortSignal): Promise<string> {
  try {
    const data = await getJson(`/api/tmdb/movie/${id}/images`, signal);
    const logos: any[] = Array.isArray(data?.logos) ? data.logos : [];
    const pick = pickLogo(logos, contentLang());
    return pick?.file_path || '';
  } catch {
    return '';
  }
}

export function img(size: string, path: string): string {
  return tmdbImage(path, size) ?? '';
}

// ---- Personal rows (recommendations) and Netflix Top 10 ----

export type RecommendationReason = 'top_picks' | 'because_loved' | 'because_liked' | 'because_favorite' | 'popular' | string;

export interface RecommendationRow {
  id: string;
  /** English title from the server, localised by the screen */
  title: string;
  reason: RecommendationReason;
  items: TitleItem[];
}

// Rows from the profile's ratings and favorites ([] when the server has none or TMDB is down)
export async function fetchRecommendations(profileId: number, signal?: AbortSignal, lang = contentLang()): Promise<RecommendationRow[]> {
  const res = await fetch(`${getApiUrl()}/api/recommendations?profile_id=${profileId}`, {
    signal,
    cache: 'no-store',
    headers: { 'X-User-ID': String(profileId), 'X-Lang': lang },
  });
  if (!res.ok) throw new Error(`/api/recommendations -> ${res.status}`);
  const data = await res.json();
  const rows: any[] = Array.isArray(data?.rows) ? data.rows : [];
  return rows
    .map(r => ({
      id: String(r?.id ?? ''),
      title: String(r?.title ?? ''),
      reason: String(r?.reason ?? ''),
      items: toTitles(r?.items, 'movie'),
    }))
    .filter(r => r.id && r.items.length > 0);
}

export type Top10Region = 'TR' | 'global';

// Netflix's weekly Top 10 in Netflix's rank order (ranks can have gaps); throws while it is not available (503)
export async function fetchTop10(region: Top10Region, type: TitleType, signal?: AbortSignal): Promise<TitleItem[]> {
  const data = await getJson(`/api/top10?region=${region}&type=${type}`, signal);
  const items: any[] = Array.isArray(data?.items) ? data.items : [];
  const out: TitleItem[] = [];
  for (const it of items) {
    const t = toTitle({ ...it, id: it?.tmdb_id }, type);
    if (!t || (!t.poster_path && !t.backdrop_path)) continue;
    out.push({ ...t, rank: typeof it.rank === 'number' ? it.rank : out.length + 1 });
  }
  return out;
}

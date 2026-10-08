import { getApiUrl } from '@/shared/lib/api';
import { tmdbImage } from '@/shared/lib/image';

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
}

export const GENRE_NAMES: Record<number, string> = {
  28: 'Action', 12: 'Adventure', 16: 'Animation', 35: 'Comedy', 80: 'Crime', 99: 'Documentary',
  18: 'Drama', 10751: 'Family', 14: 'Fantasy', 36: 'History', 27: 'Horror', 10402: 'Music',
  9648: 'Mystery', 10749: 'Romance', 878: 'Sci-Fi', 10770: 'TV Movie', 53: 'Thriller',
  10752: 'War', 37: 'Western', 10759: 'Action & Adventure', 10762: 'Kids', 10763: 'News',
  10764: 'Reality', 10765: 'Sci-Fi & Fantasy', 10766: 'Soap', 10767: 'Talk', 10768: 'War & Politics',
};

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

export function genreLabels(t: TitleItem, max = 3): string[] {
  return t.genre_ids.map(id => GENRE_NAMES[id]).filter(Boolean).slice(0, max);
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

// English (or language-less) title logo for a movie, or '' when there is none
export async function fetchMovieLogo(id: number, signal?: AbortSignal): Promise<string> {
  try {
    const data = await getJson(`/api/tmdb/movie/${id}/images`, signal);
    const logos: any[] = Array.isArray(data?.logos) ? data.logos : [];
    const pick = logos.find(l => l.iso_639_1 === 'en') || logos.find(l => !l.iso_639_1);
    return pick?.file_path || '';
  } catch {
    return '';
  }
}

export function img(size: string, path: string): string {
  return tmdbImage(path, size) ?? '';
}

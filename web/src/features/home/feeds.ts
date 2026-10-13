import { contentLang, getApiUrl, isOldServer, markOldServer } from '@/shared/lib/api';
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


async function getJson(path: string, signal?: AbortSignal, headers?: HeadersInit): Promise<any> {
  const res = await fetch(`${getApiUrl()}${path}`, { signal, headers });
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

// Discover params of the home genre rows, by the ids of GET /api/home.
// Keep in sync with tmdb.HomeGenreRows (server/internal/platform/tmdb/home.go).
export const HOME_GENRE_ROWS: { id: string; params: Record<string, string> }[] = [
  { id: 'action_adventure', params: { with_genres: '28|12', 'vote_count.gte': '300' } },
  { id: 'comedies', params: { with_genres: '35', 'vote_count.gte': '300' } },
  { id: 'scifi_fantasy', params: { with_genres: '878|14', 'vote_count.gte': '300' } },
  { id: 'horror', params: { with_genres: '27', 'vote_count.gte': '200' } },
  { id: 'acclaimed', params: { sort_by: 'vote_average.desc', 'vote_count.gte': '5000' } },
];

/** Everything the home rows need; a null section failed or was not ready yet. */
export interface HomeData {
  feeds: HomeFeeds | null;
  /** Set when only one half of feeds arrived; the other half's lists are empty */
  feedsMissing?: 'movies' | 'tv';
  popular: TitleItem[] | null;
  top10Movies: TitleItem[] | null;
  top10Tv: TitleItem[] | null;
  /** By genre row id (HOME_GENRE_ROWS); a missing id counts as null */
  genres: Record<string, TitleItem[] | null>;
}

const ROW_MAX = 20;

/** Normalizes a GET /api/home body (also used for the legacy calls mapped into that shape). */
export function parseHome(body: any): HomeData {
  const movies = body?.upcoming_movies ?? null;
  const tv = body?.upcoming_tv_series ?? null;
  const feeds: HomeFeeds | null = movies || tv
    ? {
        trendingMovies: toTitles(movies?.trending_daily, 'movie'),
        trendingWeekMovies: toTitles(movies?.trending_weekly, 'movie'),
        upcomingMovies: toTitles(movies?.upcoming, 'movie'),
        trendingTv: toTitles(tv?.trending_daily, 'tv'),
        popularTv: toTitles(tv?.trending_weekly, 'tv'),
        onTheAirTv: toTitles(tv?.on_the_air, 'tv'),
      }
    : null;
  const genres: Record<string, TitleItem[] | null> = {};
  for (const g of Array.isArray(body?.genres) ? body.genres : []) {
    if (typeof g?.id !== 'string') continue;
    genres[g.id] = g.data ? toTitles(g.data.results, 'movie').slice(0, ROW_MAX) : null;
  }
  const pop = body?.popular_movies;
  return {
    feeds,
    feedsMissing: feeds && !movies ? 'movies' : feeds && !tv ? 'tv' : undefined,
    popular: pop ? toTitles(pop.results, 'movie').slice(0, ROW_MAX) : null,
    top10Movies: body?.top10_tr_movie ? top10FromBody(body.top10_tr_movie, 'movie').slice(0, ROW_MAX) : null,
    top10Tv: body?.top10_tr_tv ? top10FromBody(body.top10_tr_tv, 'tv').slice(0, ROW_MAX) : null,
    genres,
  };
}

/**
 * Home rows in one request (GET /api/home), else the old per-row calls on an older server.
 * Throws on network errors and 5xx: callers keep what they show.
 */
export async function fetchHome(lang: string, signal?: AbortSignal): Promise<HomeData> {
  const base = getApiUrl();
  // A 404/405 marks an older server for this page load; Home then uses the old calls there
  if (!isOldServer(base)) {
    const res = await fetch(`${base}/api/home?x_lang=${lang}`, { signal });
    if (res.ok) return parseHome(await res.json());
    if (res.status !== 404 && res.status !== 405) throw new Error(`/api/home -> ${res.status}`);
    markOldServer(base);
  }
  return fetchHomeLegacy(lang, signal);
}

/** The old endpoints in parallel, mapped into the /api/home shape. */
export async function fetchHomeLegacy(lang: string, signal?: AbortSignal): Promise<HomeData> {
  // Older servers read the language only from the X-Lang header
  const get = (path: string) => getJson(path, signal, { 'X-Lang': lang }).catch(() => null);
  const discover = (params: Record<string, string>) =>
    get(`/api/tmdb/discover/movie?${new URLSearchParams({ sort_by: 'popularity.desc', include_adult: 'false', ...params })}`);
  const [movies, tv, popular, top10Movie, top10Tv, ...genres] = await Promise.all([
    get('/api/upcoming-movies'),
    get('/api/upcoming-tv-series'),
    get('/api/tmdb/movie/popular'),
    get('/api/top10?region=TR&type=movie'),
    get('/api/top10?region=TR&type=tv'),
    ...HOME_GENRE_ROWS.map(g => discover(g.params)),
  ]);
  // Not throwIfAborted(): Safari before 15.4 lacks it
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError');
  return parseHome({
    upcoming_movies: movies,
    upcoming_tv_series: tv,
    popular_movies: popular,
    top10_tr_movie: top10Movie,
    top10_tr_tv: top10Tv,
    genres: HOME_GENRE_ROWS.map((g, i) => ({ id: g.id, data: genres[i] })),
  });
}

// Keeps unchanged items (and whole lists) as the same objects, so memoized cards skip re-rendering
function keepSame(prev: TitleItem[] | null | undefined, next: TitleItem[]): TitleItem[] {
  if (!prev || prev.length === 0) return next;
  const byKey = new Map(prev.map(t => [`${t.media_type}-${t.id}`, t]));
  let same = prev.length === next.length;
  const out = next.map((t, i) => {
    const old = byKey.get(`${t.media_type}-${t.id}`);
    const kept = old && JSON.stringify(old) === JSON.stringify(t) ? old : t;
    if (kept !== prev[i]) same = false;
    return kept;
  });
  return same ? prev : out;
}

function mergeList(prev: TitleItem[] | null | undefined, next: TitleItem[] | null): TitleItem[] | null {
  return next ? keepSame(prev, next) : prev ?? null;
}

function mergeFeeds(prev: HomeFeeds | null, next: HomeData): HomeFeeds | null {
  if (!next.feeds) return prev;
  const f = next.feeds;
  const fromPrev = (k: keyof HomeFeeds, missing: boolean) => (missing && prev ? prev[k] : keepSame(prev?.[k], f[k]));
  const noMovies = next.feedsMissing === 'movies';
  const noTv = next.feedsMissing === 'tv';
  return {
    trendingMovies: fromPrev('trendingMovies', noMovies),
    trendingWeekMovies: fromPrev('trendingWeekMovies', noMovies),
    upcomingMovies: fromPrev('upcomingMovies', noMovies),
    trendingTv: fromPrev('trendingTv', noTv),
    popularTv: fromPrev('popularTv', noTv),
    onTheAirTv: fromPrev('onTheAirTv', noTv),
  };
}

/** True when a section failed or was not ready (the server keeps filling its cache). */
export function hasNullSection(d: HomeData): boolean {
  return !d.feeds || !!d.feedsMissing || !d.popular || !d.top10Movies || !d.top10Tv
    || HOME_GENRE_ROWS.some(g => !d.genres[g.id]);
}

/** A fresh answer over the previous one: null sections keep the previous data. */
export function mergeHome(prev: HomeData | null, next: HomeData): HomeData {
  if (!prev) return next;
  const genres: Record<string, TitleItem[] | null> = {};
  for (const g of HOME_GENRE_ROWS) genres[g.id] = mergeList(prev.genres[g.id], next.genres[g.id] ?? null);
  const feedsMissing = !next.feeds ? prev.feedsMissing
    : !next.feedsMissing ? undefined
      : !prev.feeds || prev.feedsMissing === next.feedsMissing ? next.feedsMissing : undefined;
  return {
    feeds: mergeFeeds(prev.feeds, next),
    feedsMissing,
    popular: mergeList(prev.popular, next.popular),
    top10Movies: mergeList(prev.top10Movies, next.top10Movies),
    top10Tv: mergeList(prev.top10Tv, next.top10Tv),
    genres,
  };
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
export async function fetchRecommendations(
  profileId: number,
  signal?: AbortSignal,
  lang = contentLang(),
  keepalive = false,
): Promise<RecommendationRow[]> {
  // Query parameters instead of X-User-ID / X-Lang headers: no CORS preflight
  const res = await fetch(`${getApiUrl()}/api/recommendations?profile_id=${profileId}&x_lang=${lang}`, {
    signal,
    cache: 'no-store',
    // keepalive: a prefetch from the picker finishes even though the page navigates away
    keepalive,
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
  return top10FromBody(await getJson(`/api/top10?region=${region}&type=${type}`, signal), type);
}

/** Titles of a GET /api/top10 body in rank order. */
export function top10FromBody(data: any, type: TitleType): TitleItem[] {
  const items: any[] = Array.isArray(data?.items) ? data.items : [];
  const out: TitleItem[] = [];
  for (const it of items) {
    const t = toTitle({ ...it, id: it?.tmdb_id }, type);
    if (!t || (!t.poster_path && !t.backdrop_path)) continue;
    out.push({ ...t, rank: typeof it.rank === 'number' ? it.rank : out.length + 1 });
  }
  return out;
}

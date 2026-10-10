import { getJson, scoped, cached } from './http';
import type { Profile } from './profile';

export type MediaType = 'movie' | 'tv';

// A TMDB movie or show, normalized for cards, rows and the hero
export interface Title {
  key: string;
  id: number;
  type: MediaType;
  title: string;
  overview: string;
  poster: string;
  backdrop: string;
  date: string;
  vote: number;
  genreIds: number[];
  originalLanguage?: string;
  /** Netflix Top 10 rank (ranked rows only) */
  rank?: number;
}

export function toTitle(raw: any, fallback: MediaType): Title | null {
  if (!raw || typeof raw.id !== 'number') return null;
  if (raw.media_type === 'person') return null;
  const type: MediaType = raw.media_type === 'tv' || raw.media_type === 'movie'
    ? raw.media_type
    : raw.name && !raw.title ? 'tv' : fallback;
  const title = raw.title || raw.name || raw.original_title || raw.original_name || '';
  if (!title) return null;
  return {
    key: `${type}-${raw.id}`,
    id: raw.id,
    type,
    title,
    overview: raw.overview || '',
    poster: raw.poster_path || '',
    backdrop: raw.backdrop_path || '',
    date: raw.release_date || raw.first_air_date || '',
    vote: Number(raw.vote_average) || 0,
    genreIds: Array.isArray(raw.genre_ids) ? raw.genre_ids : [],
    originalLanguage: typeof raw.original_language === 'string' ? raw.original_language : undefined,
  };
}

export function toTitles(list: any, fallback: MediaType): Title[] {
  if (!Array.isArray(list)) return [];
  const seen = new Set<string>();
  const out: Title[] = [];
  for (const raw of list) {
    const t = toTitle(raw, fallback);
    if (!t || (!t.backdrop && !t.poster) || seen.has(t.key)) continue;
    seen.add(t.key);
    out.push(t);
  }
  return out;
}

export function interleave(a: Title[], b: Title[], max = 20): Title[] {
  const out: Title[] = [];
  for (let i = 0; out.length < max && (i < a.length || i < b.length); i++) {
    if (i < a.length) out.push(a[i]);
    if (i < b.length && out.length < max) out.push(b[i]);
  }
  return out;
}

export function unique(...lists: Title[][]): Title[] {
  const seen = new Set<string>();
  const out: Title[] = [];
  lists.forEach(list => list.forEach(t => {
    if (!seen.has(t.key)) { seen.add(t.key); out.push(t); }
  }));
  return out;
}

export function yearOf(date?: string): number | undefined {
  const y = parseInt((date || '').slice(0, 4), 10);
  return Number.isFinite(y) && y > 1800 ? y : undefined;
}

export const matchPercent = (vote: number) => (vote > 0 ? Math.max(1, Math.min(99, Math.round(vote * 10))) : 0);

// ---- Lists ----

export async function profiles(): Promise<Profile[]> {
  const data = await getJson<Profile[] | null>('/api/profiles');
  return Array.isArray(data) ? data : [];
}

export type ProfileInput = Pick<Profile, 'name' | 'avatar' | 'color' | 'language'>;

export function createProfile(input: ProfileInput): Promise<Profile> {
  return getJson<Profile>('/api/profiles', { method: 'POST', body: input });
}

export interface HomeFeeds {
  trendingMovies: Title[];
  trendingWeekMovies: Title[];
  nowPlaying: Title[];
  upcomingMovies: Title[];
  trendingTv: Title[];
  popularTv: Title[];
  onTheAirTv: Title[];
  airingToday: Title[];
}

export async function homeFeeds(): Promise<HomeFeeds> {
  const [movies, tv] = await Promise.all([
    getJson('/api/upcoming-movies').catch(() => null),
    getJson('/api/upcoming-tv-series').catch(() => null),
  ]);
  if (!movies && !tv) throw new Error('feeds unavailable');
  return {
    trendingMovies: toTitles(movies?.trending_daily, 'movie'),
    trendingWeekMovies: toTitles(movies?.trending_weekly, 'movie'),
    nowPlaying: toTitles(movies?.now_playing, 'movie'),
    upcomingMovies: toTitles(movies?.upcoming, 'movie'),
    trendingTv: toTitles(tv?.trending_daily, 'tv'),
    popularTv: toTitles(tv?.trending_weekly, 'tv'),
    onTheAirTv: toTitles(tv?.on_the_air, 'tv'),
    airingToday: toTitles(tv?.airing_today, 'tv'),
  };
}

export const homeFeedsCached = () => cached(scoped('feeds'), 10 * 60 * 1000, homeFeeds);

export async function top10(region: 'TR' | 'global', type: MediaType): Promise<Title[]> {
  const data = await getJson(`/api/top10?region=${region}&type=${type}`);
  const items: any[] = Array.isArray(data?.items) ? data.items : [];
  const out: Title[] = [];
  items.forEach(it => {
    const t = toTitle({ ...it, id: it?.tmdb_id }, type);
    if (t && (t.poster || t.backdrop)) out.push({ ...t, rank: typeof it.rank === 'number' ? it.rank : out.length + 1 });
  });
  return out;
}

export interface RecommendationRow {
  id: string;
  title: string;
  reason: string;
  items: Title[];
}

export async function recommendations(profileId: number): Promise<RecommendationRow[]> {
  const data = await getJson(`/api/recommendations?profile_id=${profileId}`);
  const rows: any[] = Array.isArray(data?.rows) ? data.rows : [];
  return rows
    .map(r => ({ id: String(r?.id ?? ''), title: String(r?.title ?? ''), reason: String(r?.reason ?? ''), items: toTitles(r?.items, 'movie') }))
    .filter(r => r.id && r.items.length > 0);
}

export async function favorites(profileId: number): Promise<Title[]> {
  const data = await getJson<any[]>(`/api/favorites?profile_id=${profileId}`);
  return (Array.isArray(data) ? data : [])
    .map(r => toTitle({ id: r.tmdb_id, media_type: r.media_type, title: r.title, poster_path: r.poster_path, backdrop_path: r.backdrop_path }, 'movie'))
    .filter((t): t is Title => !!t && !!(t.poster || t.backdrop));
}

export async function popularMovies(): Promise<Title[]> {
  const data = await getJson('/api/tmdb/movie/popular');
  return toTitles(data?.results, 'movie');
}

export async function discoverMovies(params: Record<string, string>): Promise<Title[]> {
  const q = new URLSearchParams({ sort_by: 'popularity.desc', include_adult: 'false', ...params });
  const data = await getJson(`/api/tmdb/discover/movie?${q.toString()}`);
  return toTitles(data?.results, 'movie');
}

export async function providerTv(providerId: number, sortBy = 'popularity.desc'): Promise<Title[]> {
  const data = await getJson(`/api/providers/${providerId}/tv?sort_by=${encodeURIComponent(sortBy)}&page=1`);
  return toTitles(data?.results, 'tv');
}

export async function search(query: string, signal?: AbortSignal): Promise<Title[]> {
  const data = await getJson(`/api/tmdb/search?q=${encodeURIComponent(query)}&page=1`, { signal });
  return toTitles(data?.results, 'movie');
}

// ---- Details ----

export interface Details {
  id: number;
  type: MediaType;
  title: string;
  originalTitle: string;
  overview: string;
  tagline: string;
  date: string;
  poster: string;
  backdrop: string;
  vote: number;
  runtime: number;
  /** TV: typical episode length in minutes (0 when unknown) */
  episodeRunTime: number;
  genres: string[];
  cast: string[];
  directors: string[];
  creators: string[];
  originalLanguage?: string;
  numberOfSeasons: number;
  seasons: SeasonInfo[];
}

export interface SeasonInfo {
  season_number: number;
  episode_count: number;
  name?: string;
}

export function normalizeSeasons(seasons: any[] | undefined | null): SeasonInfo[] {
  return (seasons || [])
    .filter(s => s && s.season_number > 0 && s.episode_count > 0)
    .map(s => ({ season_number: s.season_number, episode_count: s.episode_count, name: s.name }))
    .sort((a, b) => a.season_number - b.season_number);
}

export async function details(id: number, type: MediaType): Promise<Details> {
  const json = await getJson(`/api/tmdb-movie/${id}?type=${type}`);
  const isTv = json?.media_type === 'tv';
  const d = json?.data ?? json;
  if (!d || typeof d.id !== 'number') throw new Error('not found');
  const crew: any[] = d.credits?.crew || [];
  return {
    id: d.id,
    type: isTv ? 'tv' : 'movie',
    title: (isTv ? d.name : d.title) || d.original_title || d.original_name || '',
    originalTitle: (isTv ? d.original_name : d.original_title) || '',
    overview: d.overview || '',
    tagline: d.tagline || '',
    date: (isTv ? d.first_air_date : d.release_date) || '',
    poster: d.poster_path || '',
    backdrop: d.backdrop_path || '',
    vote: Number(d.vote_average) || 0,
    runtime: isTv ? 0 : Number(d.runtime) || 0,
    episodeRunTime: isTv && Array.isArray(d.episode_run_time) ? Number(d.episode_run_time[0]) || 0 : 0,
    genres: (d.genres || []).map((g: any) => g?.name).filter(Boolean),
    cast: (d.credits?.cast || []).slice(0, 6).map((p: any) => p?.name).filter(Boolean),
    directors: crew.filter(p => p?.job === 'Director').map(p => p.name),
    creators: (d.created_by || []).map((c: any) => c?.name).filter(Boolean),
    originalLanguage: d.original_language || undefined,
    numberOfSeasons: Number(d.number_of_seasons) || 0,
    seasons: isTv ? normalizeSeasons(d.seasons) : [],
  };
}

/** Movie title logo in the content language, else English, else language-less ('' when none). */
export async function movieLogo(id: number, lang: string): Promise<string> {
  try {
    const data = await getJson(`/api/tmdb/movie/${id}/images`);
    const logos: any[] = Array.isArray(data?.logos) ? data.logos : [];
    const pick = logos.find(l => l.iso_639_1 === lang) || logos.find(l => l.iso_639_1 === 'en') || logos.find(l => !l.iso_639_1);
    return pick?.file_path || '';
  } catch {
    return '';
  }
}

/** "More Like This" from the server's matcher (collection first; profile via X-User-ID). */
export async function related(id: number, type: MediaType, _year?: number): Promise<Title[]> {
  const data = await getJson(`/api/catalog/${type}/${id}/similar?limit=24`);
  return toTitles(data?.results, type).filter(t => !(t.id === id && t.type === type));
}

export interface EpisodeInfo {
  episode_number: number;
  season_number: number;
  name: string;
  overview: string;
  still_path: string;
  runtime: number;
  air_date: string;
}

/** Numbered entries without names (shown as "Episode N" in the UI language). */
export const placeholderEpisodes = (season: SeasonInfo): EpisodeInfo[] =>
  Array.from({ length: season.episode_count }, (_, i) => ({
    episode_number: i + 1,
    season_number: season.season_number,
    name: '',
    overview: '',
    still_path: '',
    runtime: 0,
    air_date: '',
  }));

/** Episodes of a season; throws when the request fails (callers must not cache a failure). */
export async function loadSeasonEpisodes(tvId: number, season: SeasonInfo): Promise<EpisodeInfo[]> {
  const data = await getJson(`/api/tmdb/tv/${tvId}/season/${season.season_number}`);
  const eps: any[] = Array.isArray(data?.episodes) ? data.episodes : [];
  if (eps.length === 0) return placeholderEpisodes(season);
  return eps.map(e => ({
    episode_number: e.episode_number,
    season_number: e.season_number ?? season.season_number,
    name: e.name || '',
    overview: e.overview || '',
    still_path: e.still_path || '',
    runtime: e.runtime || 0,
    air_date: e.air_date || '',
  }));
}

/** Like loadSeasonEpisodes, but numbered placeholders on failure. */
export function seasonEpisodes(tvId: number, season: SeasonInfo): Promise<EpisodeInfo[]> {
  return loadSeasonEpisodes(tvId, season).catch(() => placeholderEpisodes(season));
}

/** No name, or TMDB's own for untitled episodes: "Episode 3" (en-US) or "3. Bölüm" (tr-TR). */
export const isPlaceholderEpisodeName = (ep: Pick<EpisodeInfo, 'name' | 'episode_number'>) =>
  !ep.name || ep.name === `Episode ${ep.episode_number}` || ep.name === `${ep.episode_number}. Bölüm`;

export async function tvSeasons(tvId: number): Promise<SeasonInfo[]> {
  try {
    const json = await getJson(`/api/tmdb-movie/${tvId}?type=tv`);
    return normalizeSeasons((json?.data ?? json)?.seasons);
  } catch {
    return [];
  }
}

export interface PauseInfo {
  overview: string;
  /** Movies: release year and runtime in minutes */
  year?: number;
  runtime: number;
  /** TV: the episode's name ('' for TMDB placeholders) */
  episodeTitle: string;
}

/**
 * Text of the player's pause screen: the episode's (else the show's) or the film's overview.
 * null when nothing could be loaded; only complete answers are cached, so the next pause asks again.
 */
export function pauseInfo(m: { tmdb_id: number; media_type: MediaType; season?: number; episode?: number }): Promise<PauseInfo | null> {
  const key = scoped(`pause|${m.media_type}|${m.tmdb_id}|${m.season || 0}|${m.episode || 0}`);
  const load = async (): Promise<{ info: PauseInfo | null; complete: boolean }> => {
    if (m.media_type === 'tv' && m.season && m.episode) {
      let seasonOk = true;
      const [eps, d] = await Promise.all([
        loadSeasonEpisodes(m.tmdb_id, { season_number: m.season, episode_count: 0 })
          .catch(() => { seasonOk = false; return [] as EpisodeInfo[]; }),
        details(m.tmdb_id, 'tv').catch(() => null),
      ]);
      const ep = eps.find(e => e.episode_number === m.episode);
      if (!ep?.overview && !d) return { info: null, complete: false };
      return {
        info: {
          overview: ep?.overview || d?.overview || '',
          year: d ? yearOf(d.date) : undefined,
          runtime: ep?.runtime || d?.episodeRunTime || 0,
          episodeTitle: ep && !isPlaceholderEpisodeName(ep) ? ep.name : '',
        },
        complete: seasonOk && !!d,
      };
    }
    const d = await details(m.tmdb_id, m.media_type).catch(() => null);
    if (!d) return { info: null, complete: false };
    return { info: { overview: d.overview, year: yearOf(d.date), runtime: d.runtime, episodeTitle: '' }, complete: true };
  };
  return cached(key, 6 * 60 * 60 * 1000, load, r => r.complete).then(r => r.info, () => null);
}

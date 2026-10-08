import { getApiUrl } from '@/shared/lib/api';

export type StreamMediaType = 'movie' | 'tv';

// Best torrent picked by the backend for a title / episode
export interface ResolvedSource {
  magnet: string;
  hash: string;
  index: number;
  file_name?: string;
  torrent_title?: string;
  quality?: string;
  seeders?: number;
  size?: number;
}

// What the player needs to start streaming without resolving again
export interface StreamSource {
  hash: string;
  index: number;
  magnet?: string;
  file_name?: string;
}

// Title info stored with progress (Continue Watching cards, resume)
export interface StreamMeta {
  tmdb_id: number;
  media_type: StreamMediaType;
  title: string;
  poster_path?: string;
  backdrop_path?: string;
  season?: number;
  episode?: number;
  episode_title?: string;
  year?: number;
}

export interface StreamProgress {
  id?: number;
  profile_id: number;
  media_type: StreamMediaType;
  tmdb_id: number;
  title: string;
  poster_path: string;
  backdrop_path: string;
  season: number;
  episode: number;
  episode_title: string;
  magnet: string;
  hash: string;
  file_index: number;
  position: number;
  duration: number;
  updated_at?: string;
}

// Everything needed to open the player
export interface PlayRequest {
  meta: StreamMeta;
  source?: StreamSource;
  startAt?: number;
}

export interface SeasonInfo {
  season_number: number;
  episode_count: number;
  name?: string;
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

// Same threshold the backend uses to drop finished titles from Continue Watching
export const FINISHED_RATIO = 0.92;

export class StreamApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function readError(res: Response, fallback: string): Promise<string> {
  try {
    const data = await res.json();
    return data?.error || fallback;
  } catch {
    return fallback;
  }
}

export async function resolveSource(
  params: { type: StreamMediaType; title: string; year?: number; season?: number; episode?: number },
  signal?: AbortSignal,
): Promise<ResolvedSource> {
  const q = new URLSearchParams({ type: params.type, title: params.title });
  if (params.type === 'movie' && params.year) q.set('year', String(params.year));
  if (params.type === 'tv') {
    q.set('season', String(params.season ?? 1));
    q.set('episode', String(params.episode ?? 1));
  }
  const res = await fetch(`${getApiUrl()}/api/torrent-stream/resolve?${q.toString()}`, { signal });
  if (!res.ok) throw new StreamApiError(await readError(res, 'No playable source found'), res.status);
  return res.json();
}

export async function saveProgress(p: StreamProgress, opts: { keepalive?: boolean } = {}): Promise<void> {
  const { id, updated_at, ...body } = p;
  try {
    await fetch(`${getApiUrl()}/api/stream-progress`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      keepalive: opts.keepalive,
    });
  } catch {
    /* progress is best effort */
  }
}

export async function getProgressList(profileId: number): Promise<StreamProgress[]> {
  try {
    const res = await fetch(`${getApiUrl()}/api/stream-progress?profile_id=${profileId}`);
    if (!res.ok) return [];
    const data = await res.json();
    return Array.isArray(data) ? data : [];
  } catch {
    return [];
  }
}

export async function getProgressItem(
  profileId: number,
  mediaType: StreamMediaType,
  tmdbId: number,
  season = 0,
  episode = 0,
): Promise<StreamProgress | null> {
  const q = new URLSearchParams({
    profile_id: String(profileId),
    media_type: mediaType,
    tmdb_id: String(tmdbId),
    season: String(season),
    episode: String(episode),
  });
  try {
    const res = await fetch(`${getApiUrl()}/api/stream-progress/item?${q.toString()}`);
    if (!res.ok) return null;
    return await res.json();
  } catch {
    return null;
  }
}

// Every saved entry of one title (all episodes, finished ones included), newest first
export async function getTitleProgress(
  profileId: number,
  mediaType: StreamMediaType,
  tmdbId: number,
): Promise<StreamProgress[]> {
  const q = new URLSearchParams({ profile_id: String(profileId), media_type: mediaType, tmdb_id: String(tmdbId) });
  try {
    const res = await fetch(`${getApiUrl()}/api/stream-progress/title?${q.toString()}`, { cache: 'no-store' });
    if (!res.ok) return [];
    const data = await res.json();
    return Array.isArray(data) ? sortNewestFirst(data) : [];
  } catch {
    return [];
  }
}

function sortNewestFirst(list: StreamProgress[]): StreamProgress[] {
  const time = (p: StreamProgress) => (p.updated_at ? Date.parse(p.updated_at) || 0 : 0);
  return [...list].sort((a, b) => time(b) - time(a));
}

// Saved entry of one episode (or of the movie with season = episode = 0) from a getTitleProgress list
export function findProgress(entries: StreamProgress[], season = 0, episode = 0): StreamProgress | null {
  return entries.find(p => (p.season || 0) === season && (p.episode || 0) === episode) || null;
}

export async function deleteProgress(profileId: number, mediaType: StreamMediaType, tmdbId: number): Promise<boolean> {
  const q = new URLSearchParams({ profile_id: String(profileId), media_type: mediaType, tmdb_id: String(tmdbId) });
  try {
    const res = await fetch(`${getApiUrl()}/api/stream-progress?${q.toString()}`, { method: 'DELETE' });
    return res.ok;
  } catch {
    return false;
  }
}

// Regular seasons of a TMDB show (specials / season 0 skipped)
export async function getTvSeasons(tmdbId: number): Promise<SeasonInfo[]> {
  try {
    const res = await fetch(`${getApiUrl()}/api/tmdb-movie/${tmdbId}?type=tv`);
    if (!res.ok) return [];
    const json = await res.json();
    const data = json?.data ?? json;
    return normalizeSeasons(data?.seasons);
  } catch {
    return [];
  }
}

export function normalizeSeasons(seasons: any[] | undefined | null): SeasonInfo[] {
  return (seasons || [])
    .filter(s => s && s.season_number > 0 && s.episode_count > 0)
    .map(s => ({ season_number: s.season_number, episode_count: s.episode_count, name: s.name }))
    .sort((a, b) => a.season_number - b.season_number);
}

// Episode details of one season. Falls back to numbered placeholders when the
// backend has no TMDB season endpoint.
export async function getSeasonEpisodes(tmdbId: number, season: SeasonInfo): Promise<EpisodeInfo[]> {
  try {
    const res = await fetch(`${getApiUrl()}/api/tmdb/tv/${tmdbId}/season/${season.season_number}`);
    if (res.ok) {
      const data = await res.json();
      const eps = Array.isArray(data?.episodes) ? data.episodes : [];
      if (eps.length > 0) {
        return eps.map((e: any) => ({
          episode_number: e.episode_number,
          season_number: e.season_number ?? season.season_number,
          name: e.name || `Episode ${e.episode_number}`,
          overview: e.overview || '',
          still_path: e.still_path || '',
          runtime: e.runtime || 0,
          air_date: e.air_date || '',
        }));
      }
    }
  } catch {
    /* fall through to placeholders */
  }
  return Array.from({ length: season.episode_count }, (_, i) => ({
    episode_number: i + 1,
    season_number: season.season_number,
    name: `Episode ${i + 1}`,
    overview: '',
    still_path: '',
    runtime: 0,
    air_date: '',
  }));
}

export function nextEpisodeOf(seasons: SeasonInfo[], season: number, episode: number): { season: number; episode: number } | null {
  const cur = seasons.find(s => s.season_number === season);
  if (cur && episode < cur.episode_count) return { season, episode: episode + 1 };
  const next = seasons.find(s => s.season_number > season && s.episode_count > 0);
  return next ? { season: next.season_number, episode: 1 } : null;
}

// What "Play" on a show should open, from getTitleProgress (newest first):
// resume the newest episode, or the episode after it when that one is finished.
// Both null means start from the beginning.
export function planTvPlay(
  entries: StreamProgress[],
  seasons: SeasonInfo[],
): { resume: StreamProgress | null; next: { season: number; episode: number } | null } {
  const latest = entries[0];
  if (!latest) return { resume: null, next: null };
  if (!isFinished(latest)) return { resume: latest, next: null };
  const next = nextEpisodeOf(seasons, latest.season, latest.episode);
  if (!next) return { resume: null, next: null };
  const saved = findProgress(entries, next.season, next.episode);
  return { resume: saved && !isFinished(saved) ? saved : null, next };
}

export function isFinished(p: { position: number; duration: number }): boolean {
  return p.duration > 0 && p.position >= p.duration * FINISHED_RATIO;
}

export function progressPercent(p: { position: number; duration: number }): number {
  return p.duration > 0 ? Math.min(100, Math.max(0, (p.position / p.duration) * 100)) : 0;
}

export function episodeLabel(season?: number, episode?: number): string {
  return season && episode ? `S${season}:E${episode}` : '';
}

// Saved progress -> player request that reuses the stored torrent (no resolve)
export function progressToRequest(p: StreamProgress): PlayRequest {
  return {
    meta: {
      tmdb_id: p.tmdb_id,
      media_type: p.media_type,
      title: p.title,
      poster_path: p.poster_path,
      backdrop_path: p.backdrop_path,
      season: p.season || undefined,
      episode: p.episode || undefined,
      episode_title: p.episode_title || undefined,
    },
    source: p.hash ? { hash: p.hash, index: p.file_index, magnet: p.magnet || undefined } : undefined,
    startAt: isFinished(p) ? 0 : Math.max(0, p.position - 5),
  };
}

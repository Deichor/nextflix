import { contentLang, getApiUrl } from '@/shared/lib/api';
import { translate, type Locale } from '@/shared/i18n/core';

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
  /** release is tagged HDR10 / HLG / Dolby Vision */
  hdr?: boolean;
  /** Swarm speed measured when the server picked it (bytes/s) */
  health?: SourceHealth;
  /** the file's duration was checked against the TMDB runtime and fits */
  duration_verified?: boolean;
  /** true when the server answered from its source cache */
  cached?: boolean;
  /** Format of the file; subs: languages of the text subtitles inside (embedded tracks, files next to it) */
  format?: { subs?: string[] };
  /** Plays from the TorBox cache (source "torbox"), not the swarm */
  debrid?: boolean;
  source?: string;
  /** A cinema recording (asked for with cinema: true) */
  cinema?: boolean;
}

export interface SourceHealth {
  rate: number;
  /** average bitrate the file needs (size / runtime) */
  need: number;
  active_peers: number;
  seeders: number;
  keeps_up: boolean;
  dead?: boolean;
}

// What the player needs to start streaming without resolving again
export interface StreamSource {
  hash: string;
  index: number;
  magnet?: string;
  file_name?: string;
  /** From the resolve result, shown until the probe knows the real video */
  quality?: string;
  hdr?: boolean;
  /** The resolve measured its swarm below the file's bitrate */
  starving?: boolean;
  /** Swarm rate / needed bitrate measured by the resolve (undefined: not measured), see startBuffer.ts */
  ratio?: number;
  /** Release name from the resolve (helps the subtitle search match the release) */
  torrent_title?: string;
  /** Plays from the TorBox cache */
  debrid?: boolean;
  /** A cinema recording of a film still in cinemas */
  cinema?: boolean;
}

export interface PlaybackCaps {
  /** HEVC Main and Main10 decode, so the server can send 4K / 10-bit HEVC untouched */
  hevc: boolean;
  /** HDR display (only reported together with hevc) */
  hdr: boolean;
  /** Audio codecs this browser plays in MP4 (server names: aac, opus, flac, mp3, ac3, eac3) */
  audio: string[];
}

const HEVC_TYPES = ['video/mp4; codecs="hvc1.1.6.L153.B0"', 'video/mp4; codecs="hvc1.2.4.L153.B0"'];
const AUDIO_TYPES: [string, string][] = [
  ['aac', 'audio/mp4; codecs="mp4a.40.2"'],
  ['opus', 'audio/mp4; codecs="opus"'],
  ['flac', 'audio/mp4; codecs="flac"'],
  ['mp3', 'audio/mp4; codecs="mp4a.69"'],
  ['ac3', 'audio/mp4; codecs="ac-3"'],
  ['eac3', 'audio/mp4; codecs="ec-3"'],
];
let caps: PlaybackCaps | null = null;

// What this browser can play, detected once
export function playbackCaps(): PlaybackCaps {
  if (caps) return caps;
  if (typeof window === 'undefined') return { hevc: false, hdr: false, audio: ['aac'] };
  const video = document.createElement('video');
  const supported = (type: string) => {
    try {
      if (typeof MediaSource !== 'undefined' && MediaSource.isTypeSupported(type)) return true;
    } catch {
      /* fall back to canPlayType */
    }
    return video.canPlayType(type) === 'probably';
  };
  const hevc = HEVC_TYPES.every(supported);
  const hdr = hevc && !!window.matchMedia?.('(dynamic-range: high)').matches;
  const audio = AUDIO_TYPES.filter(([, type]) => supported(type)).map(([name]) => name);
  caps = { hevc, hdr, audio };
  if (hevc) detect4kDecode();
  return caps;
}

// Whether this device decodes 4K HEVC smoothly; undefined until the async check answers
let decodes4k: boolean | undefined;

function detect4kDecode(): void {
  const mc = typeof navigator !== 'undefined' ? navigator.mediaCapabilities : undefined;
  if (!mc?.decodingInfo) return;
  mc.decodingInfo({
    type: 'media-source',
    video: { contentType: HEVC_TYPES[1], width: 3840, height: 2160, bitrate: 25_000_000, framerate: 24 },
  })
    .then(r => { decodes4k = r.supported && r.smooth; })
    .catch(() => {});
}

// Capability query for probe, seekpoint and play ("&acodecs=..." plus "&hevc=1[&hdr=1]").
// The audio codecs listed there are copied untouched instead of being re-encoded.
export function capsQuery(): string {
  const c = playbackCaps();
  return (c.hevc ? '&hevc=1' : '') + (c.hdr ? '&hdr=1' : '') + `&acodecs=${c.audio.join(',')}`;
}

function addCaps(q: URLSearchParams): URLSearchParams {
  const c = playbackCaps();
  if (c.hevc) q.set('hevc', '1');
  if (c.hdr) q.set('hdr', '1');
  return q;
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
  /** TMDB runtime in minutes; lets the server check that a swarm keeps up with the bitrate */
  runtime?: number;
  /** TMDB original_language (ISO 639-1); picks the original audio track by default */
  original_language?: string;
  /** Play a cinema recording of a film that is only in cinemas (resolve with cinema=1) */
  cinema?: boolean;
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
  /** YYYY-MM-DD: the next episode is not out yet (Continue Watching only) */
  next_air_date?: string;
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
  /** 425 answers: the title / episode is not out yet */
  availability?: Availability;
  /** "offline": the server has no internet connection */
  code?: string;
  constructor(message: string, status: number, availability?: Availability, code?: string) {
    super(message);
    this.status = status;
    this.availability = availability;
    this.code = code;
  }
}

async function readError(res: Response, fallback: string): Promise<StreamApiError> {
  let data: any = null;
  try {
    data = await res.json();
  } catch {
    /* no JSON body */
  }
  const availability = res.status === 425 && data
    ? {
        status: (data.status as AvailabilityStatus) || 'not_released',
        release_date: data.release_date || null,
        theatrical_date: data.theatrical_date || null,
        digital_date: data.digital_date || null,
      }
    : undefined;
  return new StreamApiError(data?.error || fallback, res.status, availability, typeof data?.code === 'string' ? data.code : undefined);
}

// ---- Availability (is the title / episode out yet?) ----

export type AvailabilityStatus = 'available' | 'not_released' | 'in_theaters' | 'unknown';

export interface Availability {
  status: AvailabilityStatus;
  release_date: string | null;
  theatrical_date?: string | null;
  digital_date?: string | null;
}

export interface AvailabilityTarget {
  media_type: StreamMediaType;
  tmdb_id: number;
  season?: number;
  episode?: number;
}

const AVAILABILITY_TTL_MS = 10 * 60 * 1000;
const availabilityCache = new Map<string, { at: number; p: Promise<Availability | null> }>();

// Release state of a movie or episode; cached for 10 minutes, null when the server cannot tell
export function getAvailability(target: AvailabilityTarget): Promise<Availability | null> {
  const q = new URLSearchParams({ media_type: target.media_type, tmdb_id: String(target.tmdb_id) });
  if (target.media_type === 'tv' && target.season && target.episode) {
    q.set('season', String(target.season));
    q.set('episode', String(target.episode));
  }
  const key = q.toString();
  const hit = availabilityCache.get(key);
  if (hit && Date.now() - hit.at < AVAILABILITY_TTL_MS) return hit.p;
  const p = fetch(`${getApiUrl()}/api/availability?${key}`)
    .then(res => (res.ok ? res.json() : null))
    .then(data => (data && typeof data.status === 'string' ? (data as Availability) : null))
    .catch(() => null);
  availabilityCache.set(key, { at: Date.now(), p });
  p.then(a => { if (!a) availabilityCache.delete(key); });
  return p;
}

/** Raw TMDB details of a title; fields are checked where they are read. */
type TitleDetails = Record<string, unknown>;

const DETAILS_TTL_MS = 10 * 60 * 1000;
const detailsCache = new Map<string, { at: number; p: Promise<TitleDetails | null> }>();

// TMDB details in the content language (X-Lang), shared by the helpers below
function getTitleDetails(tmdbId: number, type: StreamMediaType): Promise<TitleDetails | null> {
  const key = `${contentLang()}|${type}:${tmdbId}`;
  const hit = detailsCache.get(key);
  if (hit && Date.now() - hit.at < DETAILS_TTL_MS) return hit.p;
  const p = fetch(`${getApiUrl()}/api/tmdb-movie/${tmdbId}?type=${type}`)
    .then(res => (res.ok ? res.json() : null))
    .then(json => {
      const data = json?.data ?? json;
      return data && typeof data === 'object' && !Array.isArray(data) ? (data as TitleDetails) : null;
    })
    .catch(() => null);
  detailsCache.set(key, { at: Date.now(), p });
  p.then(d => { if (!d) detailsCache.delete(key); });
  return p;
}

interface TitleTraits {
  originalLanguage?: string;
  genreIds: number[];
}

const traitsCache = new Map<string, Promise<TitleTraits | null>>();

// Original language and genres of a title from TMDB details (cached for the session)
function getTitleTraits(tmdbId: number, type: StreamMediaType): Promise<TitleTraits | null> {
  const key = `${type}:${tmdbId}`;
  let p = traitsCache.get(key);
  if (!p) {
    p = getTitleDetails(tmdbId, type)
      .then(data => {
        if (!data) return null;
        return {
          originalLanguage: (typeof data.original_language === 'string' && data.original_language) || undefined,
          genreIds: Array.isArray(data.genres) ? data.genres.map((g: { id?: unknown }) => Number(g?.id)).filter(Number.isFinite) : [],
        };
      })
      .catch(() => null);
    traitsCache.set(key, p);
    p.then(t => { if (!t) traitsCache.delete(key); });
  }
  return p;
}

/**
 * TMDB original_language of a title (cached for the session), for resumes whose meta lacks it
 * (Continue Watching). Undefined when unknown or slower than waitMs.
 */
export function getOriginalLanguage(tmdbId: number, type: StreamMediaType, waitMs = 0): Promise<string | undefined> {
  const p = getTitleTraits(tmdbId, type).then(t => t?.originalLanguage);
  if (waitMs <= 0) return p;
  return Promise.race([p, new Promise<undefined>(resolve => setTimeout(() => resolve(undefined), waitMs))]);
}

const ANIMATION_GENRE = 16;
// Japanese anime and Chinese donghua (TMDB: zh Mandarin, cn Cantonese)
const ANIME_LANGUAGES = ['ja', 'zh', 'cn'];

/**
 * Anime for the player's upscaling: Animation from Japan or China. Wider than the catalog's Anime
 * filter (catalog/discover.go), which is Japanese only.
 */
export function isAnimeTitle(tmdbId: number, type: StreamMediaType): Promise<boolean> {
  return getTitleTraits(tmdbId, type).then(t =>
    !!t && ANIME_LANGUAGES.includes(t.originalLanguage || '') && t.genreIds.includes(ANIMATION_GENRE));
}

/** Episode air date (YYYY-MM-DD) still ahead of today. */
export function isFutureDate(date?: string | null): boolean {
  if (!date) return false;
  const d = new Date(/^\d{4}-\d{2}-\d{2}$/.test(date) ? `${date}T00:00:00` : date);
  return !Number.isNaN(d.getTime()) && d.getTime() > Date.now();
}

/** Highest resolution the player accepts after it had to step down */
export type QualityCap = 1080 | 720;

// Largest resolution this device can use: 4K only on a 4K-class screen (physical pixels) that also
// decodes 4K HEVC smoothly, so a 4K phone gets 4K and a 1080p phone gets 1080p
export function screenQualityCap(): QualityCap | undefined {
  if (typeof window === 'undefined' || !window.screen) return undefined;
  playbackCaps();
  const long = Math.max(window.screen.width, window.screen.height) * (window.devicePixelRatio || 1);
  if (!long) return undefined;
  if (long < 1600) return 720;
  if (long < 3000 || decodes4k === false) return 1080;
  return undefined;
}

export type ResolveParams = {
  type: StreamMediaType;
  title: string;
  year?: number;
  season?: number;
  episode?: number;
  /** TMDB id: the server looks up the runtime itself and rejects files of the wrong length */
  tmdb_id?: number;
  /** TMDB runtime in minutes, used when the server has no TMDB runtime */
  runtime?: number;
  maxq?: QualityCap;
  /** A cinema recording of a film only in cinemas (the viewer asked for it) */
  cinema?: boolean;
};

function resolveQuery(params: ResolveParams): URLSearchParams {
  const q = new URLSearchParams({ type: params.type, title: params.title });
  if (params.type === 'movie' && params.year) q.set('year', String(params.year));
  if (params.type === 'tv') {
    q.set('season', String(params.season ?? 1));
    q.set('episode', String(params.episode ?? 1));
  }
  if (params.tmdb_id && params.tmdb_id > 0) q.set('tmdb_id', String(params.tmdb_id));
  if (params.runtime && params.runtime > 0) q.set('runtime', String(Math.round(params.runtime)));
  const screen = screenQualityCap();
  const maxq = params.maxq && screen ? Math.min(params.maxq, screen) : params.maxq || screen;
  if (maxq) q.set('maxq', String(maxq));
  if (params.cinema) q.set('cinema', '1');
  return q;
}

// `refresh` skips the server's source cache (use it when a cached source failed to play).
// `avoid` lists torrent hashes that must not be picked again (broken or of the wrong length).
// The answer has `cached: true` when it came from that cache.
export async function resolveSource(
  params: ResolveParams,
  signal?: AbortSignal,
  opts: { refresh?: boolean; avoid?: string[] } = {},
): Promise<ResolvedSource> {
  const q = addCaps(resolveQuery(params));
  if (opts.refresh) q.set('refresh', '1');
  if (opts.avoid?.length) q.set('avoid', opts.avoid.join(','));
  const res = await fetch(`${getApiUrl()}/api/torrent-stream/resolve?${q.toString()}`, { signal });
  if (!res.ok) throw await readError(res, 'No playable source found');
  return res.json();
}

// How a saved source (Continue Watching, a resumed episode) plays: from TorBox when it has the
// release cached, from its swarm when that keeps up at `t`, else 'resolve' (search again without
// it). null when the server could not tell (old server, network): play the saved source as is.
export interface ReuseAnswer {
  action: 'play' | 'resolve';
  reason?: string;
  source?: ResolvedSource;
}

export async function reuseSource(
  params: ResolveParams,
  saved: { hash: string; index: number; magnet?: string },
  t: number,
  signal?: AbortSignal,
): Promise<ReuseAnswer | null> {
  const q = addCaps(resolveQuery(params));
  q.set('hash', saved.hash.toLowerCase());
  q.set('index', String(saved.index));
  if (saved.magnet) q.set('magnet', saved.magnet);
  if (t > 0) q.set('t', String(Math.round(t)));
  try {
    const res = await fetch(`${getApiUrl()}/api/torrent-stream/reuse?${q.toString()}`, { signal });
    if (res.status === 503) {
      // Without internet the saved source cannot be checked either: the player shows it
      const err = await readError(res, 'offline');
      if (err.code === 'offline') throw err;
    }
    if (!res.ok) return null;
    const a = (await res.json()) as ReuseAnswer;
    return a?.action === 'resolve' || (a?.action === 'play' && a.source?.hash) ? a : null;
  } catch (e: any) {
    if (e?.name === 'AbortError' || (e instanceof StreamApiError && e.code === 'offline')) throw e;
    return null;
  }
}

// A better source than the playing one (`current` hash of `currentQuality` lines) once the
// server has warmed one up that keeps up, else null. The server may start a background search.
export async function checkUpgrade(
  params: ResolveParams,
  current: string,
  currentQuality: number,
  signal?: AbortSignal,
  failed?: string,
): Promise<ResolvedSource | null> {
  const q = addCaps(resolveQuery(params));
  q.set('current', current);
  if (currentQuality > 0) q.set('current_q', String(currentQuality));
  // Why the last switch to an offer failed (logged by the server)
  if (failed) q.set('failed', failed.slice(0, 160));
  try {
    const res = await fetch(`${getApiUrl()}/api/torrent-stream/upgrade?${q.toString()}`, { signal });
    return res.status === 200 ? await res.json() : null;
  } catch {
    return null;
  }
}

// Probe query that lets the server compare the file's duration with the TMDB runtime
export function runtimeCheckQuery(meta?: StreamMeta): string {
  if (!meta?.tmdb_id) return '';
  const q = new URLSearchParams({ type: meta.media_type, tmdb_id: String(meta.tmdb_id) });
  if (meta.media_type === 'tv') {
    q.set('season', String(meta.season ?? 1));
    q.set('episode', String(meta.episode ?? 1));
  }
  if (meta.runtime && meta.runtime > 0) q.set('runtime', String(Math.round(meta.runtime)));
  return `&${q.toString()}`;
}

// The source Play would use, if the server already found it. Never starts a search.
export async function getCachedSource(params: ResolveParams): Promise<ResolvedSource | null> {
  if (typeof window === 'undefined' || !params.title?.trim()) return null;
  const q = addCaps(resolveQuery({ ...params, title: params.title.trim() }));
  q.set('cached', '1');
  try {
    const res = await fetch(`${getApiUrl()}/api/torrent-stream/resolve?${q.toString()}`);
    return res.ok ? await res.json() : null;
  } catch {
    return null;
  }
}

// "4K" / "HD" / "SD", plus "HDR" when this screen can show it
export function qualityBadges(s: Pick<ResolvedSource, 'quality' | 'hdr'>): string[] {
  const res = s.quality === '2160p' ? '4K' : s.quality === '1080p' || s.quality === '720p' ? 'HD' : s.quality ? 'SD' : '';
  return [res, s.hdr && playbackCaps().hdr ? 'HDR' : ''].filter(Boolean);
}

// A sidegrade: a different source of at least the playing resolution that keeps up now
// (the server keeps 1-2 alternatives warm during the first minutes of playback), else null.
// state: the player's buffer verdict (draining = playback outruns the download, critical = about to stall),
// so the server treats the current source as starving and looks for a faster one at any point of playback
export async function checkSidegrade(
  params: ResolveParams,
  current: string,
  currentQuality: number,
  signal?: AbortSignal,
  opts: { state?: 'draining' | 'critical'; avoid?: string[] } = {},
): Promise<ResolvedSource | null> {
  const q = addCaps(resolveQuery(params));
  q.set('current', current);
  if (currentQuality > 0) q.set('current_q', String(currentQuality));
  if (opts.state) q.set(opts.state, '1');
  if (opts.avoid?.length) q.set('avoid', opts.avoid.slice(-5).join(','));
  try {
    const res = await fetch(`${getApiUrl()}/api/torrent-stream/sidegrade?${q.toString()}`, { signal });
    return res.status === 200 ? await res.json() : null;
  } catch {
    return null;
  }
}


// A release of the title whose subtitles (embedded tracks, files in the torrent) include `lang` when the
// playing one lacks it. retryAfter (seconds) is set while the server scans other releases' headers.
export async function checkSubAlt(
  params: ResolveParams,
  current: string,
  currentQuality: number,
  lang: string,
  signal?: AbortSignal,
): Promise<{ source: ResolvedSource | null; retryAfter?: number }> {
  const q = addCaps(resolveQuery(params));
  q.set('current', current);
  q.set('lang', lang);
  if (currentQuality > 0) q.set('current_q', String(currentQuality));
  try {
    const res = await fetch(`${getApiUrl()}/api/torrent-stream/subalt?${q.toString()}`, { signal });
    if (res.status === 200) return { source: await res.json() };
    const retry = Number(res.headers.get('Retry-After'));
    return { source: null, retryAfter: retry > 0 ? retry : undefined };
  } catch {
    return { source: null };
  }
}

const PREFETCH_TTL_MS = 10 * 60 * 1000;
const prefetched = new Map<string, number>();

// Title / episode part of a prefetch key (release clears every variant of it)
const titleKey = (params: ResolveParams) => resolveQuery({ ...params, title: params.title.trim(), maxq: undefined }).toString().toLowerCase();

// The next episode is warmed once 10 minutes are left, or at 85 % of episodes under 25 minutes
// (the server applies the same rule)
export function nextWarmDue(position: number, duration: number): boolean {
  if (duration <= 0 || position <= 0) return false;
  if (duration < 25 * 60) return position >= duration * 0.85;
  return duration - position <= 10 * 60;
}

// Warms a source on intent (detail page opened, Play, next episode near the end) so Play
// starts instantly. Fire-and-forget, deduplicated until the title is released.
// `next` passes the current episode's position for the server's next-episode window.
// True when it asked the server (false: deduplicated, or nothing to ask).
export function prefetchResolve(params: ResolveParams, opts: { next?: { position: number; duration: number } } = {}): boolean {
  if (typeof window === 'undefined' || !params.title?.trim()) return false;
  const q = addCaps(resolveQuery({ ...params, title: params.title.trim() }));
  if (opts.next) {
    q.set('next_pos', String(Math.round(opts.next.position)));
    q.set('next_dur', String(Math.round(opts.next.duration)));
  }
  const key = `${titleKey(params)}|${params.maxq ?? ''}`;
  const now = Date.now();
  const last = prefetched.get(key);
  if (last && now - last < PREFETCH_TTL_MS) return false;
  prefetched.set(key, now);
  fetch(`${getApiUrl()}/api/torrent-stream/prefetch?${q.toString()}`, { method: 'POST' })
    .then(res => (res.ok ? res.json() : null))
    .then(data => {
      // Skipped while the server is busy, another title plays (deferred) or too early for the next episode: may try again
      if (data?.status === 'busy' || data?.status === 'early' || data?.status === 'deferred') prefetched.delete(key);
    })
    .catch(() => {});
  return true;
}

function beacon(url: string): void {
  try {
    if (navigator.sendBeacon?.(url)) return;
  } catch {
    /* fall back to fetch */
  }
  fetch(url, { method: 'POST', keepalive: true }).catch(() => {});
}

// Titles (movie / episode) an open player plays, counted per player
const playingTitles = new Map<string, number>();
const playKey = (p: ResolveParams) =>
  `${p.type}|${p.tmdb_id ?? p.title.trim().toLowerCase()}|${p.season ?? 0}|${p.episode ?? 0}`;

// Marks a title as playing until the returned function is called (idempotent). While it
// plays, releaseTitle ignores it, so no detail page or row releases the playing title.
export function holdPlaying(params: ResolveParams): () => void {
  const key = playKey(params);
  playingTitles.set(key, (playingTitles.get(key) ?? 0) + 1);
  let held = true;
  return () => {
    if (!held) return;
    held = false;
    const n = (playingTitles.get(key) ?? 1) - 1;
    if (n > 0) playingTitles.set(key, n);
    else playingTitles.delete(key);
  };
}

// Lets the server drop a title's warmed torrents and stop its background work, after
// `delaySec` (a grace in case the viewer comes back; any new prefetch or Play cancels it).
// Works during page unload.
export function releaseTitle(params: ResolveParams, delaySec = 0): void {
  if (typeof window === 'undefined' || !params.title?.trim()) return;
  // An open player plays it: only that player releases it, when it closes
  if (playingTitles.has(playKey(params))) return;
  const base = titleKey(params);
  Array.from(prefetched.keys()).forEach(k => { if (k.startsWith(`${base}|`)) prefetched.delete(k); });
  const q = resolveQuery({ ...params, title: params.title.trim(), maxq: undefined });
  if (delaySec > 0) q.set('delay', String(Math.round(delaySec)));
  beacon(`${getApiUrl()}/api/torrent-stream/release?${q.toString()}`);
}

// Drops torrents a closing player used (never one that is playing again by then); why: the
// reason a switch to them failed, for the server log
/** A paused player keeps its file ready on the server (stream guard, TorBox link and connection) */
export function keepSourceAlive(hash: string, index: number): void {
  if (typeof window === 'undefined' || !/^[0-9a-f]{40}$/i.test(hash)) return;
  beacon(`${getApiUrl()}/api/torrent-stream/keepalive?hash=${hash.toLowerCase()}&index=${index}`);
}

export function releaseTorrents(hashes: string[], why?: string): void {
  const list = Array.from(new Set(hashes.map(h => h.toLowerCase()).filter(h => /^[0-9a-f]{40}$/.test(h)))).slice(0, 10);
  if (typeof window === 'undefined' || list.length === 0) return;
  const reason = why ? `&why=${encodeURIComponent(why.slice(0, 160))}` : '';
  beacon(`${getApiUrl()}/api/torrent-stream/release?hash=${list.join(',')}${reason}`);
}

export async function saveProgress(p: StreamProgress, opts: { keepalive?: boolean } = {}): Promise<void> {
  // Server-owned fields; JSON.stringify drops undefined ones
  const body = { ...p, id: undefined, updated_at: undefined };
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
    const res = await fetch(`${getApiUrl()}/api/stream-progress?profile_id=${profileId}&upcoming=1`);
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
  const data = await getTitleDetails(tmdbId, 'tv');
  return normalizeSeasons(Array.isArray(data?.seasons) ? data.seasons : null);
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

export function episodeLabel(season?: number, episode?: number, locale: Locale = 'en'): string {
  return season && episode ? translate(locale, 'player.episodeShort', { s: String(season), e: String(episode) }) : '';
}

/** getSeasonEpisodes names episodes without TMDB data "Episode N". */
export function isPlaceholderEpisodeName(ep: Pick<EpisodeInfo, 'name' | 'episode_number'>): boolean {
  // TMDB names untitled episodes "Episode 3" (en-US) or "3. Bölüm" (tr-TR)
  return ep.name === `Episode ${ep.episode_number}` || ep.name === `${ep.episode_number}. Bölüm`;
}

export interface PauseInfo {
  overview: string;
  /** Release (TV: first air) year and runtime in minutes */
  year?: number;
  runtime: number;
  /** TV: the episode's name ('' for TMDB placeholders) */
  episodeTitle: string;
}

const pauseInfoCache = new Map<string, Promise<PauseInfo | null>>();
const pauseSeasonCache = new Map<string, { at: number; p: Promise<EpisodeInfo[]> }>();

// getSeasonEpisodes once per season for all of its episodes; failures (no episodes) are not kept
function pauseSeasonEpisodes(tmdbId: number, season: number): Promise<EpisodeInfo[]> {
  const key = `${contentLang()}|${tmdbId}|${season}`;
  const hit = pauseSeasonCache.get(key);
  if (hit && Date.now() - hit.at < DETAILS_TTL_MS) return hit.p;
  const p = getSeasonEpisodes(tmdbId, { season_number: season, episode_count: 0 });
  pauseSeasonCache.set(key, { at: Date.now(), p });
  p.then(eps => { if (eps.length === 0) pauseSeasonCache.delete(key); });
  return p;
}

const asText = (v: unknown) => (typeof v === 'string' ? v : '');

function yearOf(date: unknown): number | undefined {
  const year = parseInt(asText(date).slice(0, 4), 10);
  return year > 1800 ? year : undefined;
}

/**
 * Text of the player's pause screen in the content language: the episode's (else the show's) or
 * the film's overview (cached for the session).
 */
export function getPauseInfo(m: StreamMeta): Promise<PauseInfo | null> {
  const key = `${contentLang()}|${m.media_type}|${m.tmdb_id}|${m.season || 0}|${m.episode || 0}`;
  let p = pauseInfoCache.get(key);
  if (p) return p;
  p = (async (): Promise<PauseInfo | null> => {
    if (m.media_type === 'tv' && m.season && m.episode) {
      const [eps, d] = await Promise.all([pauseSeasonEpisodes(m.tmdb_id, m.season), getTitleDetails(m.tmdb_id, 'tv')]);
      const ep = eps.find(e => e.episode_number === m.episode);
      // Nothing to show yet: let the next pause retry
      if (!ep?.overview && !d) return null;
      const runTimes = Array.isArray(d?.episode_run_time) ? d.episode_run_time : [];
      return {
        overview: ep?.overview || asText(d?.overview),
        year: yearOf(d?.first_air_date),
        runtime: ep?.runtime || Number(runTimes[0]) || 0,
        episodeTitle: ep && !isPlaceholderEpisodeName(ep) ? ep.name : '',
      };
    }
    const d = await getTitleDetails(m.tmdb_id, m.media_type);
    if (!d) return null;
    return { overview: asText(d.overview), year: yearOf(d.release_date || d.first_air_date), runtime: Number(d.runtime) || 0, episodeTitle: '' };
  })().catch(() => null);
  pauseInfoCache.set(key, p);
  p.then(i => { if (!i) pauseInfoCache.delete(key); });
  return p;
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

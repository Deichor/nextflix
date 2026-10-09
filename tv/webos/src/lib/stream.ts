import { HttpError, beacon, getJson, request } from './http';
import { readJson, writeJson } from './storage';
import { deviceInfo, isWebOS, type DeviceInfo } from './webos';
import type { MediaType, SeasonInfo } from './catalog';
import { translate, type Locale } from './i18n';
import { healthRatio } from '../player/startBuffer';

// Same request shapes as web/src/features/watch/api.ts

// ---- Playback capabilities ----
// Declared with the same query on every streaming call of a title (resolve, prefetch, release,
// sidegrade, upgrade, probe, seekpoint, play): the server ranks for them and they are part of
// its source cache key (docs/API.md "Playback capabilities").

export interface PlaybackCaps {
  /** HEVC Main / Main10 in MP4: the server copies 4K / 10-bit HEVC instead of converting it */
  hevc: boolean;
  /** HDR display (sent only together with hevc) */
  hdr: boolean;
  /** Dolby Vision decoder and display (sent only together with hevc) */
  dv: boolean;
  /** Atmos (E-AC-3 JOC) to the speakers; sent only together with eac3 */
  atmos: boolean;
  /** Audio codecs the TV plays muxed with video in MP4 (server / ffprobe names); /play copies these */
  audio: string[];
  /** Audio codecs the TV plays only in an audio-only SourceBuffer (split mode: /play?track=audio) */
  splitAudio: string[];
}

export type HdrMode = 'auto' | 'on' | 'off';
const HDR_KEY = 'nfx-tv:hdr';

export const getHdrMode = (): HdrMode => readJson<HdrMode>(HDR_KEY) || 'auto';
export function setHdrMode(mode: HdrMode): void {
  writeJson(HDR_KEY, mode);
  caps = null;
}

const HEVC_TYPES = ['video/mp4; codecs="hvc1.1.6.L153.B0"', 'video/mp4; codecs="hvc1.2.4.L153.B0"'];
const DV_TYPES = ['video/mp4; codecs="dvh1.05.06"', 'video/mp4; codecs="dvhe.05.06"', 'video/mp4; codecs="dvh1.08.06"'];
const AUDIO_TYPES: [string, string][] = [
  ['aac', 'audio/mp4; codecs="mp4a.40.2"'],
  ['opus', 'audio/mp4; codecs="opus"'],
  // No FLAC: an LG TV (Chrome 120) reports it but its MSE pipeline stalls on silent FLAC frames
  // (frames decode, the clock never starts); the server converts it to Opus instead
  ['mp3', 'audio/mp4; codecs="mp4a.69"'],
  ['ac3', 'audio/mp4; codecs="ac-3"'],
  ['eac3', 'audio/mp4; codecs="ec-3"'],
  ['truehd', 'audio/mp4; codecs="mlpa"'],
  ['dts', 'audio/mp4; codecs="dtsc"'],
];

let caps: PlaybackCaps | null = null;

const SAFE_AUDIO = ['aac', 'opus'];
let safeAudio = false;

function withVideo(audioType: string): string {
  const m = audioType.match(/codecs="([^"]+)"/);
  return m ? `video/mp4; codecs="avc1.640028,${m[1]}"` : audioType;
}

/** After a stream the TV could not take: only AAC / Opus from now on (the server converts the rest). */
export function restrictToSafeAudio(): boolean {
  if (safeAudio) return false;
  safeAudio = true;
  caps = null;
  return true;
}
let device: DeviceInfo = { uhd: false };

/** Reads the TV's panel and Dolby features once at startup (webOS Luna calls). */
export async function initCaps(): Promise<void> {
  try {
    device = await deviceInfo();
  } catch {
    device = { uhd: false };
  }
  caps = null;
}

function mseSupported(): boolean {
  return typeof MediaSource !== 'undefined' && typeof MediaSource.isTypeSupported === 'function';
}

function supported(type: string): boolean {
  try {
    if (typeof MediaSource !== 'undefined' && MediaSource.isTypeSupported(type)) return true;
  } catch {
    /* fall back to canPlayType */
  }
  try {
    return document.createElement('video').canPlayType(type) === 'probably';
  } catch {
    return false;
  }
}

export function playbackCaps(): PlaybackCaps {
  if (caps) return caps;
  const hevc = HEVC_TYPES.every(supported);
  let hdrDisplay = device.hdr10 === true;
  try {
    if (window.matchMedia && window.matchMedia('(dynamic-range: high)').matches) hdrDisplay = true;
  } catch {
    /* old engine */
  }
  // Without the config answer: every LG UHD panel since webOS 3 shows HDR10
  if (device.hdr10 === undefined && isWebOS() && device.uhd) hdrDisplay = true;
  const mode = getHdrMode();
  const hdr = hevc && (mode === 'on' || (mode === 'auto' && hdrDisplay));
  // Audio is checked muxed with video, as a muxed /play stream delivers it: an LG TV (Chrome 120)
  // accepts 'audio/mp4; codecs="ec-3"' alone but refuses "avc1/hvc1 + ec-3" in one SourceBuffer.
  // Such codecs are split-only: the server sends them as a separate audio stream (?track=audio).
  // The streams go through MSE: canPlayType answers for the TV's own player, which takes muxed E-AC-3
  const allowed = ([name]: [string, string]) => !safeAudio || SAFE_AUDIO.indexOf(name) >= 0;
  const muxed = (type: string) => (mseSupported() ? MediaSource.isTypeSupported(withVideo(type)) : supported(withVideo(type)));
  const audio = AUDIO_TYPES.filter(allowed).filter(([, type]) => supported(type) && muxed(type)).map(([name]) => name);
  if (audio.indexOf('aac') < 0) audio.unshift('aac');
  const splitAudio = mseSupported()
    ? AUDIO_TYPES.filter(allowed).filter(([name, type]) => audio.indexOf(name) < 0 && MediaSource.isTypeSupported(type)).map(([name]) => name)
    : [];
  // Dolby Vision only when the TV reports it (config) or its decoder accepts a DV codec string
  const dv = hdr && (device.dolbyVision === true || (device.dolbyVision === undefined && DV_TYPES.some(supported)));
  // Atmos only with a decodable E-AC-3 (muxed or split) and a TV that says it handles Atmos
  const atmos = (audio.indexOf('eac3') >= 0 || splitAudio.indexOf('eac3') >= 0) && device.dolbyAtmos === true;
  caps = { hevc, hdr, dv, atmos, audio, splitAudio };
  return caps;
}

/**
 * Resolve-family calls (ranking, cache key) declare every codec the TV plays, split-only ones
 * included, so Atmos releases rank first. probe / seekpoint / play declare the muxed ones in
 * acodecs and the split-only ones in split_acodecs: a muxed stream never copies those.
 */
function capsEntries(stream = false): [string, string][] {
  const c = playbackCaps();
  const out: [string, string][] = [];
  if (c.hevc) out.push(['hevc', '1']);
  if (c.hevc && c.hdr) out.push(['hdr', '1']);
  if (c.hevc && c.dv) out.push(['dv', '1']);
  if (c.atmos) out.push(['atmos', '1']);
  if (!stream) {
    out.push(['acodecs', c.audio.concat(c.splitAudio).join(',')]);
    return out;
  }
  out.push(['acodecs', c.audio.join(',')]);
  if (c.splitAudio.length > 0) out.push(['split_acodecs', c.splitAudio.join(',')]);
  return out;
}

/** "&hevc=1&hdr=1&dv=1&atmos=1&acodecs=...&split_acodecs=..." for probe, seekpoint and play. */
export function capsQuery(): string {
  return capsEntries(true).map(([k, v]) => `&${k}=${v}`).join('');
}

function addCaps(q: URLSearchParams): URLSearchParams {
  capsEntries().forEach(([k, v]) => q.set(k, v));
  return q;
}

// ---- Types ----

export interface SourceHealth {
  rate: number;
  need: number;
  active_peers: number;
  seeders: number;
  keeps_up: boolean;
  dead?: boolean;
}

export interface ResolvedSource {
  magnet: string;
  hash: string;
  index: number;
  file_name?: string;
  torrent_title?: string;
  quality?: string;
  hdr?: boolean;
  health?: SourceHealth;
  duration_verified?: boolean;
  cached?: boolean;
  format?: SourceFormat;
  /** Plays from a debrid service (TorBox) instead of the swarm */
  debrid?: boolean;
  source?: string;
  /** A cinema recording (resolved with cinema=1) */
  cinema?: boolean;
}

/** What resolve knows about the release (docs/API.md "Playback capabilities") */
export interface SourceFormat {
  audio?: string;
  channels?: number;
  atmos?: boolean;
  dv?: boolean;
  dv_only?: boolean;
  hdr10?: boolean;
  hdr10plus?: boolean;
  source?: string;
  probed?: boolean;
  /** Languages of the text subtitles inside (embedded tracks, files next to the video) */
  subs?: string[];
}

export interface StreamSource {
  hash: string;
  index: number;
  magnet?: string;
  file_name?: string;
  quality?: string;
  hdr?: boolean;
  starving?: boolean;
  torrent_title?: string;
  format?: SourceFormat;
  /** Swarm rate / needed bitrate measured by the resolve (undefined: not measured) */
  ratio?: number;
  debrid?: boolean;
  /** A cinema recording of a film still in cinemas */
  cinema?: boolean;
}

export const sourceOf = (r: ResolvedSource): StreamSource => ({
  hash: r.hash,
  index: r.index,
  magnet: r.magnet,
  file_name: r.file_name,
  torrent_title: r.torrent_title,
  quality: r.quality,
  hdr: r.hdr,
  starving: r.health?.keeps_up === false,
  format: r.format,
  ratio: healthRatio(r.health),
  debrid: r.debrid === true || r.source === 'torbox',
  cinema: r.cinema === true,
});

export interface StreamMeta {
  tmdb_id: number;
  media_type: MediaType;
  title: string;
  poster_path?: string;
  backdrop_path?: string;
  season?: number;
  episode?: number;
  episode_title?: string;
  year?: number;
  runtime?: number;
  original_language?: string;
  /** Play a cinema recording of a film that is only in cinemas (resolve with cinema=1) */
  cinema?: boolean;
}

export interface PlayRequest {
  meta: StreamMeta;
  source?: StreamSource;
  startAt?: number;
  seasons?: SeasonInfo[];
}

export type QualityCap = 1080 | 720;

export interface ResolveParams {
  type: MediaType;
  title: string;
  year?: number;
  season?: number;
  episode?: number;
  tmdb_id?: number;
  runtime?: number;
  maxq?: QualityCap;
  /** A cinema recording of a film only in cinemas (the viewer asked for it) */
  cinema?: boolean;
}

export const paramsOf = (m: StreamMeta, maxq?: QualityCap): ResolveParams => ({
  type: m.media_type, title: m.title, tmdb_id: m.tmdb_id, year: m.year, season: m.season, episode: m.episode, runtime: m.runtime, maxq,
  cinema: m.cinema,
});

function resolveQuery(p: ResolveParams): URLSearchParams {
  const q = new URLSearchParams({ type: p.type, title: p.title.trim() });
  if (p.type === 'movie' && p.year) q.set('year', String(p.year));
  if (p.type === 'tv') {
    q.set('season', String(p.season ?? 1));
    q.set('episode', String(p.episode ?? 1));
  }
  if (p.tmdb_id && p.tmdb_id > 0) q.set('tmdb_id', String(p.tmdb_id));
  if (p.runtime && p.runtime > 0) q.set('runtime', String(Math.round(p.runtime)));
  if (p.maxq) q.set('maxq', String(p.maxq));
  if (p.cinema) q.set('cinema', '1');
  return q;
}

// ---- Availability ----

export type AvailabilityStatus = 'available' | 'not_released' | 'in_theaters' | 'unknown';

export interface Availability {
  status: AvailabilityStatus;
  release_date: string | null;
  theatrical_date?: string | null;
  digital_date?: string | null;
}

export class StreamError extends Error {
  status: number;
  availability?: Availability;
  constructor(message: string, status: number, availability?: Availability) {
    super(message);
    this.status = status;
    this.availability = availability;
  }
}

export async function getAvailability(media_type: MediaType, tmdb_id: number, season?: number, episode?: number): Promise<Availability | null> {
  const q = new URLSearchParams({ media_type, tmdb_id: String(tmdb_id) });
  if (media_type === 'tv' && season && episode) {
    q.set('season', String(season));
    q.set('episode', String(episode));
  }
  try {
    const data = await getJson(`/api/availability?${q.toString()}`);
    return data && typeof data.status === 'string' ? (data as Availability) : null;
  } catch {
    return null;
  }
}

/** Button text for a title that cannot be streamed yet, else null. */
export function unavailableLabel(a: Availability | null, locale: Locale, date: (d: string) => string): string | null {
  if (!a) return null;
  if (a.status === 'not_released') {
    const d = a.release_date || a.digital_date;
    return d ? translate(locale, 'availability.comingOn', { date: date(d) }) : translate(locale, 'home.comingSoon');
  }
  if (a.status === 'in_theaters') {
    return a.digital_date
      ? translate(locale, 'availability.inTheatersUntil', { date: date(a.digital_date) })
      : translate(locale, 'availability.inTheaters');
  }
  return null;
}

// ---- Resolve / warm-up / release ----

export async function resolveSource(
  params: ResolveParams,
  signal?: AbortSignal,
  opts: { refresh?: boolean; avoid?: string[] } = {},
): Promise<ResolvedSource> {
  const q = addCaps(resolveQuery(params));
  if (opts.refresh) q.set('refresh', '1');
  if (opts.avoid && opts.avoid.length) q.set('avoid', opts.avoid.join(','));
  try {
    return await getJson<ResolvedSource>(`/api/torrent-stream/resolve?${q.toString()}`, { signal });
  } catch (e) {
    if (e instanceof HttpError) {
      const b = e.body;
      const availability = e.status === 425 && b
        ? { status: (b.status as AvailabilityStatus) || 'not_released', release_date: b.release_date || null, digital_date: b.digital_date || null }
        : undefined;
      throw new StreamError(e.message, e.status, availability);
    }
    throw e;
  }
}

export interface SideOpts {
  /** Buffer verdict: the server then treats the current source as starving at any point of playback */
  state?: 'draining' | 'critical';
  avoid?: string[];
}

async function optionalSource(path: string, params: ResolveParams, current: string, currentQuality: number, signal?: AbortSignal, opts: SideOpts = {}) {
  const q = addCaps(resolveQuery(params));
  q.set('current', current);
  if (currentQuality > 0) q.set('current_q', String(currentQuality));
  if (opts.state) q.set(opts.state, '1');
  if (opts.avoid && opts.avoid.length) q.set('avoid', opts.avoid.slice(-5).join(','));
  try {
    const res = await request(`${path}?${q.toString()}`, { signal });
    return res.status === 200 ? ((await res.json()) as ResolvedSource) : null;
  } catch {
    return null;
  }
}

/** A higher resolution the server warmed up that keeps up now, else null. */
export const checkUpgrade = (p: ResolveParams, current: string, q: number, signal?: AbortSignal) =>
  optionalSource('/api/torrent-stream/upgrade', p, current, q, signal);

/** A different source of the same quality that keeps up now, else null. */
export const checkSidegrade = (p: ResolveParams, current: string, q: number, signal?: AbortSignal, opts?: SideOpts) =>
  optionalSource('/api/torrent-stream/sidegrade', p, current, q, signal, opts);

const PREFETCH_TTL_MS = 10 * 60 * 1000;
const prefetched = new Map<string, number>();
const titleKey = (p: ResolveParams) => resolveQuery({ ...p, maxq: undefined }).toString().toLowerCase();

/** Warms a source on intent (detail page opened, next episode). Deduplicated until released. */
export function prefetchResolve(params: ResolveParams, next?: { position: number; duration: number }): void {
  if (!params.title.trim()) return;
  const q = addCaps(resolveQuery(params));
  if (next) {
    q.set('next_pos', String(Math.round(next.position)));
    q.set('next_dur', String(Math.round(next.duration)));
  }
  const key = `${titleKey(params)}|${params.maxq ?? ''}`;
  const last = prefetched.get(key);
  if (last && Date.now() - last < PREFETCH_TTL_MS) return;
  prefetched.set(key, Date.now());
  getJson(`/api/torrent-stream/prefetch?${q.toString()}`, { method: 'POST' })
    .then(data => { if (data?.status === 'busy' || data?.status === 'early' || data?.status === 'deferred') prefetched.delete(key); })
    .catch(() => prefetched.delete(key));
}

/** Lets the server drop a title's warmed torrents (after `delaySec`, cancelled by any new intent). */
export function releaseTitle(params: ResolveParams, delaySec = 0): void {
  if (!params.title.trim()) return;
  const base = titleKey(params);
  Array.from(prefetched.keys()).forEach(k => { if (k.indexOf(`${base}|`) === 0) prefetched.delete(k); });
  const q = addCaps(resolveQuery({ ...params, maxq: undefined }));
  if (delaySec > 0) q.set('delay', String(Math.round(delaySec)));
  beacon(`/api/torrent-stream/release?${q.toString()}`);
}

/** A paused player keeps its file ready on the server (stream guard, TorBox link and connection) */
export function keepSourceAlive(hash: string, index: number): void {
  if (/^[0-9a-f]{40}$/i.test(hash)) beacon(`/api/torrent-stream/keepalive?hash=${hash.toLowerCase()}&index=${index}`);
}

/** Drops torrents a closing player used (the server never drops one that is playing again). */
export function releaseTorrents(hashes: string[]): void {
  const list = Array.from(new Set(hashes.map(h => h.toLowerCase()).filter(h => /^[0-9a-f]{40}$/.test(h)))).slice(0, 10);
  if (list.length > 0) beacon(`/api/torrent-stream/release?hash=${list.join(',')}`);
}

/** Next episode is warmed once 10 minutes are left (85 % of episodes under 25 minutes). */
export function nextWarmDue(position: number, duration: number): boolean {
  if (duration <= 0 || position <= 0) return false;
  if (duration < 25 * 60) return position >= duration * 0.85;
  return duration - position <= 10 * 60;
}

export function runtimeCheckQuery(meta: StreamMeta): string {
  if (!meta.tmdb_id) return '';
  const q = new URLSearchParams({ type: meta.media_type, tmdb_id: String(meta.tmdb_id) });
  if (meta.media_type === 'tv') {
    q.set('season', String(meta.season ?? 1));
    q.set('episode', String(meta.episode ?? 1));
  }
  if (meta.runtime && meta.runtime > 0) q.set('runtime', String(Math.round(meta.runtime)));
  return `&${q.toString()}`;
}

// ---- Continue watching ----

export interface StreamProgress {
  id?: number;
  profile_id: number;
  media_type: MediaType;
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

export const FINISHED_RATIO = 0.92;
export const isFinished = (p: { position: number; duration: number }) => p.duration > 0 && p.position >= p.duration * FINISHED_RATIO;
export const progressPercent = (p: { position: number; duration: number }) =>
  p.duration > 0 ? Math.min(100, Math.max(0, (p.position / p.duration) * 100)) : 0;

export async function saveProgress(p: StreamProgress, keepalive = false): Promise<void> {
  const body: Partial<StreamProgress> = { ...p };
  delete body.id;
  delete body.updated_at;
  try {
    await request('/api/stream-progress', { method: 'PUT', body, keepalive });
  } catch {
    /* best effort */
  }
}

export async function progressList(profileId: number): Promise<StreamProgress[]> {
  const data = await getJson<StreamProgress[]>(`/api/stream-progress?profile_id=${profileId}`);
  return Array.isArray(data) ? data : [];
}

/** Every saved entry of one title (finished ones included), newest first. */
export async function titleProgress(profileId: number, type: MediaType, tmdbId: number): Promise<StreamProgress[]> {
  const q = new URLSearchParams({ profile_id: String(profileId), media_type: type, tmdb_id: String(tmdbId) });
  try {
    const data = await getJson<StreamProgress[]>(`/api/stream-progress/title?${q.toString()}`);
    const time = (p: StreamProgress) => (p.updated_at ? Date.parse(p.updated_at) || 0 : 0);
    return Array.isArray(data) ? data.slice().sort((a, b) => time(b) - time(a)) : [];
  } catch {
    return [];
  }
}

export function findProgress(entries: StreamProgress[], season = 0, episode = 0): StreamProgress | null {
  return entries.find(p => (p.season || 0) === season && (p.episode || 0) === episode) || null;
}

export function nextEpisodeOf(seasons: SeasonInfo[], season: number, episode: number): { season: number; episode: number } | null {
  const cur = seasons.find(s => s.season_number === season);
  if (cur && episode < cur.episode_count) return { season, episode: episode + 1 };
  const next = seasons.find(s => s.season_number > season && s.episode_count > 0);
  return next ? { season: next.season_number, episode: 1 } : null;
}

export function episodeLabel(locale: Locale, season?: number, episode?: number): string {
  return season && episode ? translate(locale, 'player.episodeShort', { s: season, e: episode }) : '';
}

/** Saved progress -> a player request that reuses the stored torrent (no resolve). */
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

// ---- Ratings and favorites ----

export type RatingValue = 'dislike' | 'like' | 'love';

export interface Rating {
  rating: RatingValue | null;
  favorite: boolean;
}

export async function getRating(profileId: number, type: MediaType, tmdbId: number): Promise<Rating> {
  try {
    const r = await getJson(`/api/ratings/item?profile_id=${profileId}&media_type=${type}&tmdb_id=${tmdbId}`);
    return { rating: r?.rating ?? null, favorite: !!r?.favorite };
  } catch {
    return { rating: null, favorite: false };
  }
}

export async function putRating(
  profileId: number,
  t: { type: MediaType; id: number; title: string; poster: string; backdrop: string },
  patch: Partial<Rating>,
): Promise<Rating | null> {
  try {
    const r = await getJson('/api/ratings', {
      method: 'PUT',
      body: { profile_id: profileId, media_type: t.type, tmdb_id: t.id, title: t.title, poster_path: t.poster, backdrop_path: t.backdrop, ...patch },
    });
    return { rating: r?.rating ?? null, favorite: !!r?.favorite };
  } catch {
    return null;
  }
}

// ---- Per-profile player preferences (same key and shape as the web app) ----

export interface PlayerPrefs {
  audioLang?: string;
  sub?: 'off' | { lang: string; id?: string };
  subSize?: string;
  /** Subtitle look besides the size (lib/subtitleStyle.ts) */
  subStyle?: Record<string, string>;
  autoplayNext?: boolean;
}

const prefsKey = (pid: number | null) => `hfx-player:${pid ?? 0}`;
export const readPlayerPrefs = (pid: number | null): PlayerPrefs => readJson<PlayerPrefs>(prefsKey(pid)) || {};
export function writePlayerPrefs(pid: number | null, patch: Partial<PlayerPrefs>): void {
  writeJson(prefsKey(pid), { ...readPlayerPrefs(pid), ...patch });
}

/**
 * A release of the title whose subtitles include `lang` when the playing one lacks it. retryAfter (seconds)
 * is set while the server scans other releases' headers in the background.
 */
export async function checkSubAlt(p: ResolveParams, current: string, currentQuality: number, lang: string): Promise<{ source: ResolvedSource | null; retryAfter?: number }> {
  const q = addCaps(resolveQuery(p));
  q.set('current', current);
  q.set('lang', lang);
  if (currentQuality > 0) q.set('current_q', String(currentQuality));
  try {
    const res = await request(`/api/torrent-stream/subalt?${q.toString()}`);
    if (res.status === 200) return { source: (await res.json()) as ResolvedSource };
    const retry = Number(res.headers.get('Retry-After'));
    return { source: null, retryAfter: retry > 0 ? retry : undefined };
  } catch {
    return { source: null };
  }
}

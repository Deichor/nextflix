import { base, getJson, request } from '../lib/http';
import type { Locale, MessageKey } from '../lib/i18n';
import { getProfileId } from '../lib/profile';
import { seasonEpisodes, tvSeasons, isPlaceholderEpisodeName, type SeasonInfo } from '../lib/catalog';
import {
  StreamError, capsQuery, checkSidegrade, checkSubAlt, checkUpgrade, findProgress, isFinished, nextEpisodeOf, nextWarmDue, paramsOf,
  playbackCaps, prefetchResolve, readPlayerPrefs, releaseTitle, releaseTorrents, resolveSource, runtimeCheckQuery,
  saveProgress, sourceOf, titleProgress, writePlayerPrefs,
  type Availability, type PlayRequest, type QualityCap, type ResolveParams, type StreamMeta, type StreamSource, restrictToSafeAudio,
} from '../lib/stream';
import {
  LATE_SUBTITLE_POLL_MS, LIVE_CUES_POLL_MS, SUBALT_RETRY_MAX, bestSubtitleOf, fetchCueFile, fetchSubtitles,
  hasSubtitleLang, lateSubtitles, mergeSubtitles, normLang, pickAudioTrack, pickSubtitle,
  type AudioTrackInfo, type Cue, type SubtitleItem,
} from '../lib/subtitles';
import { BufferMonitor, bufferedEnd, type BufferVerdict } from './bufferHealth';
import { holdDone, needsStartHold, type HoldKind } from './startBuffer';
import { MseStream, mseAvailable, type MseFailure } from './mseStream';

// Port of web/src/features/watch/components/TorrentStreamPlayer.tsx for one <video> element.
// The stream is a live fragmented MP4 requested at `t` that really starts at `offset`
// (/seekpoint), so positions are offset + video.currentTime. Seeks outside the buffer
// restart the stream. Quality switches (4K upgrade, sidegrade, step down) restart it too.
// The stream reaches the element through MSE (mseStream.ts); webOS decodes no frame of it as `src`.

type Origin = 'given' | 'saved' | 'cached' | 'fresh';
type Timer = ReturnType<typeof setTimeout>;

const originalLangs = new Map<string, Promise<string | undefined>>();

/** TMDB original_language of a title (cached per session); undefined when unknown or slower than waitMs */
function originalLanguage(tmdbId: number, type: string, waitMs: number): Promise<string | undefined> {
  const key = `${type}:${tmdbId}`;
  let p = originalLangs.get(key);
  if (!p) {
    p = getJson(`/api/tmdb-movie/${tmdbId}?type=${type}`)
      .then((d: any) => (typeof d?.original_language === 'string' && d.original_language) || undefined)
      .catch(() => undefined);
    originalLangs.set(key, p);
    p.then(lang => { if (!lang) originalLangs.delete(key); });
  }
  return Promise.race([p, new Promise<undefined>(resolve => setTimeout(() => resolve(undefined), waitMs))]);
}

export interface VideoInfo {
  codec: string;
  width: number;
  height: number;
  hdr: boolean;
  dolbyVision: number;
  direct: boolean;
}

export interface PlayerState {
  phase: 'resolving' | 'ready' | 'error' | 'unreleased';
  resolveElapsed: number;
  stage: MessageKey;
  error: { key?: MessageKey; text?: string } | null;
  unreleased: Availability | null;
  meta: StreamMeta;
  duration: number;
  position: number;
  playing: boolean;
  buffering: boolean;
  /** Waiting paused for a larger buffer (startBuffer.ts) */
  holding: boolean;
  status: MessageKey;
  reconnecting: boolean;
  ended: boolean;
  fatal: { key?: MessageKey; text?: string } | null;
  audioTracks: AudioTrackInfo[];
  audio: number;
  quality: { label: string; hdr: boolean; dv: boolean } | null;
  /** "Atmos" / "7.1" / "5.1" for the selected track as it reaches the TV */
  audioBadge: string;
  subs: SubtitleItem[] | null;
  subId: string | null;
  cues: Cue[];
  toast: MessageKey | '';
  /** Another release carries the profile language's subtitle that the playing file lacks (subtitle panel row) */
  subAlt?: { busy: boolean } | null;
  countdown: number | null;
  next: { season: number; episode: number } | null;
}

const STALL_MS = 12000;
const FIRST_START_MS = 30000;
const MAX_RECOVERIES = 3;
const LONG_STALL_MS = 8000;
const STALL_WINDOW_MS = 60000;
const MIN_STALL_MS = 1500;
const MAX_DOWNGRADES = 2;
const UPGRADE_POLL_MS = 15000;
const UPGRADE_FOR_MS = 5 * 60 * 1000;
const MAX_LENGTH_RETRIES = 2;
const SIDEGRADE_FIRST_MS = 8000;
const SIDEGRADE_POLL_MS = 15000;
const SIDEGRADE_FOR_MS = 3 * 60 * 1000;
const SIDEGRADE_EVERY_MS = 2 * 60 * 1000;
// Proactive sidegrade (bufferHealth.ts): while the buffer drains the server is asked right away,
// then every 5 s backing off to 10 s. One <video> means a switch restarts the stream, so an offer
// is held and used when the buffer is nearly empty or playback stalls; it is dropped once the
// stream was healthy again for RECOVERED_MS. At most one source switch per minute.
const PROACTIVE_POLL_MS = 5000;
const PROACTIVE_POLL_MAX_MS = 10000;
const SWITCH_EVERY_MS = 60000;
const RECOVERED_MS = 15000;
const TOAST_MS = 4000;
// Slower subtitle providers finish after the first answer: ask again (see loadSubtitles)
const SUBTITLE_RETRY_MS = [8000, 20000];

const releaseHeight = (q?: string) => (q === '2160p' ? 2160 : q === '1080p' ? 1080 : q === '720p' ? 720 : 0);
const videoQuality = (w: number, h: number) =>
  w >= 3200 || h >= 1800 ? 2160 : w >= 1800 || h >= 1000 ? 1080 : w >= 1200 || h >= 700 ? 720 : w > 0 || h > 0 ? 480 : 0;
const lowerCap = (q: number): QualityCap | null => (q >= 2160 ? 1080 : q >= 1080 ? 720 : null);

function resolutionLabel(w: number, h: number): string {
  if (w >= 3200 || h >= 1800) return '4K';
  if (w >= 1800 || h >= 1000) return '1080p';
  if (w >= 1200 || h >= 700) return '720p';
  return w > 0 || h > 0 ? 'SD' : '';
}

// What is really shown: the original when copied, at most 1080p SDR when the server converts it
function qualityOf(v: VideoInfo | null, src?: StreamSource): PlayerState['quality'] {
  if (v && v.width > 0) {
    const w = v.direct ? v.width : Math.min(1920, v.width);
    const h = Math.round(v.height * (w / v.width));
    return { label: resolutionLabel(w, h), hdr: v.direct && v.hdr, dv: v.direct && v.dolbyVision > 0 };
  }
  if (!src?.quality) return null;
  return { label: src.quality === '2160p' ? '4K' : src.quality, hdr: !!src.hdr && playbackCaps().hevc, dv: false };
}

export class PlayerEngine {
  state: PlayerState;
  private listeners = new Set<() => void>();
  private video: HTMLVideoElement;
  private locale: Locale;
  private pid = getProfileId();
  private destroyed = false;

  // current item
  private meta: StreamMeta;
  private startAt?: number;
  private given?: StreamSource;
  private seasons: SeasonInfo[];

  // source
  private origin: Origin = 'given';
  private source?: StreamSource;
  private autoRefreshed = false;
  private avoid: string[] = [];
  private lengthRetries = 0;
  private cap?: QualityCap;
  private triedCaps: QualityCap[] = [];
  private downgrades = 0;
  private upgraded = false;
  private lastSidegrade = 0;
  private busy = false;
  // Buffer health and the proactive sidegrade
  private health = new BufferMonitor();
  private healthTimer: ReturnType<typeof setInterval>;
  private sidePoll = { next: 0, delay: PROACTIVE_POLL_MS };
  private pendingSide: StreamSource | null = null;
  private lastSwitch = 0;
  // Torrents left for being slow: never switched to again this session
  private slow = new Set<string>();
  private resolveCtrl: AbortController | null = null;
  private resolveTick?: ReturnType<typeof setInterval>;

  // stream of the current source
  private session = 0;
  private hash = '';
  private index = 0;
  private offset = 0;
  private exact = true;
  private seekSeq = 0;
  private seekCtrl: AbortController | null = null;
  private mse: MseStream | null = null;
  // Set when a stream's codecs fit no SourceBuffer type: plain `src` from then on
  private noMse = !mseAvailable();
  private hasPlayed = false;
  private everPlayed = false;
  private recoveries = 0;
  private stallTimer?: Timer;
  private stallResetTimer?: Timer;
  private stall: { start: number; seek: boolean } | null = null;
  private stallTimes: number[] = [];
  private longStallTimer?: Timer;
  // Adaptive start buffer: the element waits paused; `since` counts from the first playable frames
  private hold: { kind: HoldKind; since: number | null; ahead: number } | null = null;
  private holdTimer?: ReturnType<typeof setInterval>;
  private pollTimers: Timer[] = [];
  // Subtitle alternative on offer, and the language the viewer switched for (picked on the next list)
  private subAltSource: StreamSource | null = null;
  private wantSubLang = '';
  private cueTimer?: Timer;
  private subDelay = 0;
  private subDecided = false;
  private subUserPicked = false;

  // session wide
  private played = new Set<string>();
  private current = '';
  private nextWarm: ResolveParams | null = null;
  private saveTimer: ReturnType<typeof setInterval>;
  private countdownTimer?: ReturnType<typeof setInterval>;
  private toastTimer?: Timer;

  constructor(video: HTMLVideoElement, req: PlayRequest, locale: Locale) {
    this.video = video;
    this.locale = locale;
    this.meta = req.meta;
    this.startAt = req.startAt;
    this.given = req.source;
    this.seasons = req.seasons || [];
    this.state = {
      phase: req.source ? 'ready' : 'resolving',
      resolveElapsed: 0,
      stage: 'player.findingSource',
      error: null,
      unreleased: null,
      meta: req.meta,
      duration: 0,
      position: 0,
      playing: false,
      buffering: true,
      holding: false,
      status: 'player.connecting',
      reconnecting: false,
      ended: false,
      fatal: null,
      audioTracks: [],
      audio: 0,
      quality: null,
      audioBadge: '',
      subs: null,
      subId: null,
      cues: [],
      toast: '',
      countdown: null,
      next: null,
    };
    this.bindVideo();
    this.saveTimer = setInterval(() => { if (this.state.playing) this.persist(); }, 10000);
    this.healthTimer = setInterval(() => this.sampleHealth(), 1000);
    document.addEventListener('visibilitychange', this.onVisibility);
    this.loadSeasons();
    this.startItem({});
  }

  // ---- state ----

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  };

  private set(patch: Partial<PlayerState>) {
    if (this.destroyed) return;
    this.state = { ...this.state, ...patch };
    this.listeners.forEach(fn => fn());
  }

  private toast(key: MessageKey) {
    clearTimeout(this.toastTimer);
    this.set({ toast: key });
    this.toastTimer = setTimeout(() => this.set({ toast: '' }), TOAST_MS);
  }

  /** Exact playback position in the file (seconds). */
  positionNow(): number {
    return this.offset + (this.video.currentTime || 0);
  }

  /** File time the subtitle overlay shows. */
  subtitleTime(): number {
    return this.positionNow() - this.subDelay;
  }

  private stale(session: number) {
    return this.destroyed || session !== this.session;
  }

  // ---- item: resolve the source ----

  private async startItem(retry: { refresh?: boolean; startAt?: number; switchTo?: StreamSource; wrongLength?: boolean }) {
    this.cancelResolve();
    const startPos = retry.startAt ?? this.startAt;
    if (retry.switchTo) return this.openSource(retry.switchTo, startPos || 0, 'fresh');
    if (this.given && !retry.refresh) return this.openSource(this.given, startPos || 0, 'given');

    this.stopStream();
    this.session++;
    const ctrl = new AbortController();
    this.resolveCtrl = ctrl;
    const t0 = Date.now();
    const stage = (ms: number): MessageKey => {
      if (retry.wrongLength) return 'player.findingComplete';
      if (ms < 4000) return retry.refresh ? 'player.tryingAnother' : 'player.findingSource';
      return ms < 12000 ? 'player.searchingIndexers' : 'player.checkingSources';
    };
    this.set({ phase: 'resolving', resolveElapsed: 0, stage: stage(0), error: null, unreleased: null, fatal: null });
    this.resolveTick = setInterval(() => {
      const ms = Date.now() - t0;
      this.set({ resolveElapsed: ms, stage: stage(ms) });
    }, 250);

    const m = this.meta;
    try {
      // Reuse the torrent saved with earlier progress instead of searching again
      if (this.pid && !retry.refresh) {
        const entries = await titleProgress(this.pid, m.media_type, m.tmdb_id);
        if (ctrl.signal.aborted) return;
        const saved = m.media_type === 'tv' ? findProgress(entries, m.season || 0, m.episode || 0) : findProgress(entries);
        if (saved?.hash) {
          const start = this.startAt ?? (isFinished(saved) ? 0 : Math.max(0, saved.position - 5));
          this.cancelResolve();
          this.openSource({ hash: saved.hash, index: saved.file_index, magnet: saved.magnet || undefined }, start, 'saved');
          return;
        }
      }
      const r = await resolveSource(paramsOf(m, this.cap), ctrl.signal, { refresh: retry.refresh, avoid: this.avoid });
      if (ctrl.signal.aborted) return;
      this.cancelResolve();
      this.openSource(sourceOf(r), startPos || 0, r.cached ? 'cached' : 'fresh');
    } catch (e: any) {
      if (ctrl.signal.aborted || e?.name === 'AbortError') return;
      this.cancelResolve();
      if (e instanceof StreamError && e.status === 425) {
        this.set({ phase: 'unreleased', unreleased: e.availability || { status: 'not_released', release_date: null } });
        return;
      }
      const notFound = e?.status === 404 || !e?.message;
      this.set({ phase: 'error', error: notFound ? { key: 'player.noSourceHint' } : { text: String(e.message) } });
    }
  }

  private cancelResolve() {
    this.resolveCtrl?.abort();
    this.resolveCtrl = null;
    if (this.resolveTick) clearInterval(this.resolveTick);
    this.resolveTick = undefined;
  }

  /** A failed source: a reused one (saved / cached / given) is resolved again once. */
  private sourceFailed(position: number, auto: boolean): boolean {
    const reused = this.origin !== 'fresh';
    if (auto && (!reused || this.autoRefreshed)) return false;
    if (auto) this.autoRefreshed = true;
    this.startItem({ refresh: true, startAt: position });
    return true;
  }

  // ---- source: probe, then stream ----

  private async openSource(src: StreamSource, startAt: number, origin: Origin) {
    this.stopStream();
    const session = ++this.session;
    this.source = src;
    this.origin = origin;
    const h = src.hash.toLowerCase();
    // A replaced torrent (upgrade, sidegrade, step down) is dropped at once
    if (this.current && this.current !== h) {
      releaseTorrents([this.current]);
      this.played.delete(this.current);
    }
    this.current = h;
    this.played.add(h);
    this.hash = h;
    this.index = src.index;
    this.subDecided = false;
    this.subUserPicked = false;
    this.subDelay = 0;
    this.noMse = !mseAvailable();
    this.set({
      phase: 'ready', error: null, unreleased: null, fatal: null, ended: false, buffering: true, reconnecting: false,
      status: 'player.connecting', duration: 0, position: startAt, audioTracks: [], audio: 0,
      quality: qualityOf(null, src), audioBadge: '', subs: null, subId: null, cues: [],
    });

    // TorrServer may have dropped a saved torrent since
    if (src.magnet) {
      try {
        await request(`/api/torrent-stream/files?magnet=${encodeURIComponent(src.magnet)}`);
      } catch {
        /* the probe reports real failures */
      }
      if (this.stale(session)) return;
    }

    this.set({ status: 'player.readingInfo' });
    let probe: any;
    try {
      probe = await getJson(`/api/torrent-stream/probe?hash=${h}&index=${src.index}${capsQuery()}${runtimeCheckQuery(this.meta)}`);
    } catch (e: any) {
      if (this.stale(session)) return;
      if (!this.sourceFailed(startAt, true)) this.set({ fatal: e?.message ? { text: String(e.message) } : { key: 'player.couldNotRead' } });
      return;
    }
    if (this.stale(session)) return;
    // Not the whole title (e.g. 86 of 173 minutes): look for a complete version
    if (probe?.duration_mismatch && this.lengthRetries < MAX_LENGTH_RETRIES) {
      this.lengthRetries++;
      this.avoid = [...this.avoid, h];
      this.startItem({ refresh: true, startAt, wrongLength: true });
      return;
    }
    const duration = Number(probe?.duration) || 0;
    const tracks: AudioTrackInfo[] = Array.isArray(probe?.audio_tracks) ? probe.audio_tracks : [];
    // Resumes (Continue Watching) carry no original language; the default track needs it
    if (!this.meta.original_language && tracks.length > 1) {
      const m = this.meta;
      const lang = await originalLanguage(m.tmdb_id, m.media_type, 1500);
      if (this.stale(session)) return;
      if (lang && this.meta === m) {
        this.meta = { ...m, original_language: lang };
        this.set({ meta: this.meta });
      }
    }
    const audio = pickAudioTrack(tracks, {
      profileLang: this.locale,
      originalLang: this.meta.original_language,
      prefLang: readPlayerPrefs(this.pid).audioLang,
    });
    const video: VideoInfo | null = probe?.width > 0
      ? { codec: probe.video_codec || '', width: probe.width, height: probe.height || 0, hdr: !!probe.hdr, dolbyVision: Number(probe.dolby_vision) || 0, direct: !!probe.direct }
      : null;
    this.set({ duration, audioTracks: tracks, audio, quality: qualityOf(video, src), audioBadge: this.audioBadge(tracks, audio), status: 'player.buffering' });
    this.videoHeight = video ? videoQuality(video.width, video.height) : 0;
    this.loadSubtitles(session);
    this.restartAt(startAt > 0 && (duration <= 0 || startAt < duration - 10) ? startAt : 0);
    this.scheduleUpgrade(session);
    if (src.starving) this.scheduleSidegrade(session, startAt);
  }

  private videoHeight = 0;

  private playUrl(t: number): string {
    return `${base()}/api/torrent-stream/play?hash=${this.hash}&index=${this.index}&t=${t.toFixed(3)}&audio=${this.state.audio}${capsQuery()}`;
  }

  /** Restarts the stream at `target`; /seekpoint tells where it really starts (copied video starts on a keyframe). */
  private restartAt(target: number) {
    if (!this.hash) return;
    this.health.reset();
    const seq = ++this.seekSeq;
    this.seekCtrl?.abort();
    this.seekCtrl = null;
    this.beginStall(true);
    clearTimeout(this.stallTimer);
    const v = this.video;
    this.detach();
    this.offset = target;
    this.exact = target <= 0;
    this.hasPlayed = false;
    this.set({ buffering: true, ended: false, position: target });
    const url = this.playUrl(target);
    if (this.noMse) {
      v.src = url;
    } else {
      this.mse = new MseStream(v, url, {
        onFail: e => this.onStreamFail(seq, e),
        onUnsupported: mimes => {
          if (seq !== this.seekSeq || this.destroyed) return;
          // The plain src path shows no picture on webOS: ask for converted audio instead
          if (restrictToSafeAudio()) {
            console.warn('[player] no SourceBuffer type for', mimes.join(' | '), '- retrying with AAC / Opus audio');
          } else {
            console.warn('[player] no SourceBuffer type for', mimes.join(' | '), '- playing the stream as src');
            this.noMse = true;
          }
          this.restartAt(target);
        },
      });
    }
    // A borderline or unmeasured swarm starts only with a larger buffer
    this.clearHold();
    // Only the first stream of a source holds; seeks and restarts play at once
    if (this.holdSource !== this.source && needsStartHold(this.source?.ratio)) {
      this.holdSource = this.source;
      this.startHold('start');
    }
    else v.play().catch(e => { if (e?.name !== 'AbortError') this.set({ playing: false }); });
    this.armStall();
    if (target <= 0) return;

    const ctrl = new AbortController();
    this.seekCtrl = ctrl;
    const timer = setTimeout(() => ctrl.abort(), 15000);
    const q = `hash=${this.hash}&index=${this.index}&t=${target.toFixed(3)}&audio=${this.state.audio}${capsQuery()}`;
    getJson(`/api/torrent-stream/seekpoint?${q}`, { signal: ctrl.signal })
      .then(data => (typeof data?.t === 'number' && data.t >= 0 && data.t <= target + 1 ? data.t : target))
      .catch(() => target)
      .then(t => {
        clearTimeout(timer);
        if (seq !== this.seekSeq || this.destroyed) return;
        this.offset = t;
        this.exact = true;
      });
  }

  private stopStream() {
    this.clearHold();
    clearTimeout(this.stallTimer);
    clearTimeout(this.stallResetTimer);
    clearTimeout(this.longStallTimer);
    this.pollTimers.forEach(t => clearTimeout(t));
    this.pollTimers = [];
    this.seekCtrl?.abort();
    this.seekCtrl = null;
    this.seekSeq++;
    this.stall = null;
    this.stallTimes = [];
    this.hasPlayed = false;
    this.everPlayed = false;
    this.recoveries = 0;
    this.health.reset();
    // A held sidegrade belongs to the stream it was meant to replace
    if (this.pendingSide) {
      releaseTorrents([this.pendingSide.hash.toLowerCase()]);
      this.pendingSide = null;
    }
    this.video.pause();
    this.detach();
  }

  /** Stops the download and empties the element. */
  private detach() {
    this.mse?.stop();
    this.mse = null;
    const v = this.video;
    if (v.getAttribute('src')) {
      v.removeAttribute('src');
      v.load();
    }
  }

  /** Downloading: the MSE reader, or the element's own fetch for a plain `src` */
  private fetching(): boolean {
    return this.mse ? this.mse.fetching : this.video.networkState === HTMLMediaElement.NETWORK_LOADING;
  }

  /** The MSE request or an append failed (a plain `src` reports this as an element error) */
  private onStreamFail(seq: number, e: MseFailure) {
    if (seq !== this.seekSeq || this.destroyed || this.state.phase !== 'ready') return;
    console.warn('[player] stream failed:', e.message);
    this.mse = null;
    if (this.hasPlayed) {
      this.recover();
      return;
    }
    clearTimeout(this.stallTimer);
    if (!this.sourceFailed(this.positionNow(), true)) this.set({ fatal: { key: 'player.playbackFailed' } });
  }

  // ---- video events ----

  private bindVideo() {
    const v = this.video;
    v.addEventListener('play', () => this.set({ playing: true }));
    v.addEventListener('pause', () => {
      if (this.hold) return; // paused by the buffer hold, not the viewer
      this.set({ playing: false });
      this.persist();
    });
    v.addEventListener('waiting', () => {
      // Stalling with a faster source in hand: switch now instead of waiting the stall out
      if (this.pendingSide && this.hasPlayed && !v.paused) {
        this.usePending();
        return;
      }
      this.set({ buffering: true });
      this.armStall();
      if (this.hasPlayed) this.beginStall(false);
      // Resume only with a few seconds buffered, not at the first frame
      if (this.hasPlayed && !v.seeking && !this.hold) this.startHold('stall');
    });
    v.addEventListener('playing', () => {
      this.set({ buffering: false, reconnecting: false });
      clearTimeout(this.stallTimer);
      this.endStall();
      this.hasPlayed = true;
      this.everPlayed = true;
      clearTimeout(this.stallResetTimer);
      this.stallResetTimer = setTimeout(() => { this.recoveries = 0; }, 60000);
    });
    v.addEventListener('timeupdate', () => this.onTime());
    v.addEventListener('ended', () => this.onEnded());
    v.addEventListener('error', () => this.onError());
  }

  private onTime() {
    const position = this.positionNow();
    this.set({ position });
    const m = this.meta;
    const next = this.state.next;
    if (m.media_type === 'tv' && next && !this.nextWarm && nextWarmDue(position, this.state.duration)) {
      this.nextWarm = { type: 'tv', title: m.title, tmdb_id: m.tmdb_id, season: next.season, episode: next.episode, maxq: this.cap };
      prefetchResolve(this.nextWarm, { position, duration: this.state.duration });
    }
  }

  private onEnded() {
    const duration = this.state.duration;
    // The stream ended well before the file did: ffmpeg or TorrServer dropped it
    if (duration > 0 && this.positionNow() < duration - 30) {
      this.recover();
      return;
    }
    clearTimeout(this.stallTimer);
    this.set({ playing: false, ended: true });
    this.persist({ finished: true });
    if (this.state.next && readPlayerPrefs(this.pid).autoplayNext !== false) this.startCountdown();
  }

  private onError() {
    if (!this.video.getAttribute('src') || this.state.phase !== 'ready') return;
    if (this.hasPlayed) {
      this.recover();
      return;
    }
    clearTimeout(this.stallTimer);
    if (!this.sourceFailed(this.positionNow(), true)) this.set({ fatal: { key: 'player.playbackFailed' } });
  }

  private armStall() {
    clearTimeout(this.stallTimer);
    const ms = (this.hasPlayed ? STALL_MS : FIRST_START_MS) * (1 + this.recoveries);
    this.stallTimer = setTimeout(() => {
      // A paused player is not stalled (a buffer hold is)
      if (!this.video.paused || this.hold) this.recover();
    }, ms);
  }

  /** Holds the element paused until startBuffer.ts says enough is buffered. */
  private holdSource: StreamSource | null | undefined = null;

  private startHold(kind: HoldKind) {
    this.clearHold();
    this.hold = { kind, since: kind === 'stall' ? Date.now() : null, ahead: 0 };
    this.set({ holding: true });
    const v = this.video;
    v.preload = 'auto'; // keep downloading while paused
    if (!v.paused) v.pause();
    this.holdTimer = setInterval(() => this.checkHold(), 250);
  }

  private clearHold() {
    clearInterval(this.holdTimer);
    this.holdTimer = undefined;
    this.hold = null;
    if (this.state.holding) this.set({ holding: false });
  }

  private checkHold() {
    const v = this.video;
    const h = this.hold;
    if (!h) return;
    const now = Date.now();
    if (h.since === null && v.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) h.since = now;
    const end = bufferedEnd(v.buffered, v.currentTime);
    const ahead = end >= 0 ? end - v.currentTime : 0;
    // A growing buffer is progress: the stall watchdog only restarts a stream that stopped
    if (ahead > h.ahead + 0.5) {
      h.ahead = ahead;
      this.armStall();
    }
    const dur = this.state.duration;
    const done = holdDone(h.kind, {
      ahead,
      waitedMs: h.since === null ? 0 : now - h.since,
      fetching: this.fetching(),
      atEnd: dur > 0 && end >= 0 && this.offset + end >= dur - 1,
    });
    if (!done) return;
    this.clearHold();
    v.play().catch(e => { if (e?.name !== 'AbortError') this.set({ playing: false }); });
  }

  private recover() {
    clearTimeout(this.stallTimer);
    if (this.recoveries >= MAX_RECOVERIES) {
      this.set({ reconnecting: false, fatal: { key: 'player.connectionLost' } });
      return;
    }
    this.recoveries++;
    this.set({ reconnecting: true });
    this.restartAt(this.positionNow());
  }

  // ---- adaptive quality (sidegrade first, then a lower cap) ----

  private beginStall(seek: boolean) {
    if (!this.everPlayed || this.stall) return;
    this.stall = { start: Date.now(), seek };
    clearTimeout(this.longStallTimer);
    this.longStallTimer = setTimeout(() => {
      if (this.stall && (!this.video.paused || this.hold)) this.struggling();
    }, LONG_STALL_MS);
  }

  private endStall() {
    clearTimeout(this.longStallTimer);
    const s = this.stall;
    this.stall = null;
    if (!s || s.seek) return;
    const now = Date.now();
    if (now - s.start < MIN_STALL_MS) return;
    this.stallTimes = this.stallTimes.filter(t => now - t < STALL_WINDOW_MS).concat(now);
    if (this.stallTimes.length >= 2) this.struggling();
  }

  private struggling() {
    this.stallTimes = [];
    if (this.pendingSide) {
      this.usePending();
      return;
    }
    const quality = this.videoHeight;
    this.trySidegrade(quality).then(switched => { if (!switched) this.stepDown(quality); });
  }

  /** Another source at the current position (restarts the stream). */
  private switchTo(src: StreamSource, toast: MessageKey) {
    this.lastSwitch = Date.now();
    this.openSource(src, this.positionNow(), 'fresh');
    this.toast(toast);
  }

  private async trySidegrade(quality: number): Promise<boolean> {
    const src = this.source;
    if (!src || this.busy || Date.now() - this.lastSidegrade < SIDEGRADE_EVERY_MS) return false;
    const session = this.session;
    this.busy = true;
    try {
      const up = await checkSidegrade(paramsOf(this.meta, this.cap), src.hash, quality || releaseHeight(src.quality), undefined,
        { state: 'critical', avoid: Array.from(this.slow) });
      if (!up || up.hash.toLowerCase() === src.hash.toLowerCase() || this.stale(session)) return false;
      this.lastSidegrade = Date.now();
      if (this.slow.has(up.hash.toLowerCase())) {
        releaseTorrents([up.hash.toLowerCase()]);
        return false;
      }
      this.slow.add(this.current);
      this.switchTo(sourceOf(up), 'player.switchedFaster');
      return true;
    } finally {
      this.busy = false;
    }
  }

  // ---- buffer health: proactive sidegrade ----

  private sampleHealth() {
    const v = this.video;
    const s = this.state;
    if (s.phase !== 'ready' || !v.getAttribute('src') || v.paused || v.seeking || s.buffering || s.ended || s.fatal ||
        !this.hasPlayed || !this.exact) return;
    const pos = this.offset + v.currentTime;
    const end = bufferedEnd(v.buffered, v.currentTime);
    const fileEnd = end >= 0 ? this.offset + end : pos;
    this.onHealth(this.health.push({
      at: Date.now(), pos, end: fileEnd,
      fetching: this.fetching(),
      atEnd: s.duration > 0 && fileEnd >= s.duration - 1,
    }));
  }

  private onHealth(h: BufferVerdict) {
    const pending = this.pendingSide;
    if (pending) {
      if (h.state === 'critical') {
        this.usePending();
      } else if (h.healthyMs >= RECOVERED_MS) {
        // Recovered: the held source is not needed
        this.pendingSide = null;
        releaseTorrents([pending.hash.toLowerCase()]);
      }
      return;
    }
    const poll = this.sidePoll;
    if (h.state === 'healthy') {
      poll.next = 0;
      poll.delay = PROACTIVE_POLL_MS;
      return;
    }
    const now = Date.now();
    if (now < poll.next) return;
    poll.next = now + poll.delay;
    poll.delay = Math.min(PROACTIVE_POLL_MAX_MS, poll.delay * 1.5);
    this.askSidegrade(h.state === 'critical' ? 'critical' : 'draining');
  }

  /** The buffer drains: ask for a faster source and hold it for the moment it is needed. */
  private async askSidegrade(state: 'draining' | 'critical') {
    const src = this.source;
    const now = Date.now();
    if (!src || this.busy || this.pendingSide || now - this.lastSidegrade < SIDEGRADE_EVERY_MS ||
        now - this.lastSwitch < SWITCH_EVERY_MS) return;
    const session = this.session;
    this.busy = true;
    try {
      const up = await checkSidegrade(paramsOf(this.meta, this.cap), src.hash, this.videoHeight || releaseHeight(src.quality), undefined,
        { state, avoid: Array.from(this.slow) });
      if (!up) return;
      const h = up.hash.toLowerCase();
      if (h === src.hash.toLowerCase()) return;
      this.lastSidegrade = Date.now();
      if (this.stale(session) || this.slow.has(h)) {
        releaseTorrents([h]);
        return;
      }
      this.pendingSide = sourceOf(up);
    } finally {
      this.busy = false;
    }
  }

  private usePending() {
    const next = this.pendingSide;
    if (!next) return;
    this.pendingSide = null;
    this.slow.add(this.current);
    this.switchTo(next, 'player.switchedFaster');
  }

  private stepDown(quality: number) {
    const src = this.source;
    const cap = lowerCap(quality || releaseHeight(src?.quality));
    if (!src || !cap || this.busy || this.downgrades >= MAX_DOWNGRADES) return;
    if (this.triedCaps.indexOf(cap) >= 0 || (this.cap && cap > this.cap)) return;
    this.triedCaps.push(cap);
    const session = this.session;
    this.busy = true;
    resolveSource(paramsOf(this.meta, cap), undefined, { avoid: this.avoid })
      .then(r => {
        if (this.stale(session)) return;
        this.cap = cap;
        if (r.health && !r.health.keeps_up) return;
        if (r.hash.toLowerCase() === src.hash.toLowerCase() && r.index === src.index) return;
        this.downgrades++;
        this.switchTo(sourceOf(r), 'player.switchedHd');
      })
      .catch(() => {})
      .then(() => { this.busy = false; });
  }

  /** A source the resolve measured as starving: look for a faster one early (also before the first frame when starting at 0). */
  private scheduleSidegrade(session: number, startAt: number) {
    const started = Date.now();
    const poll = async () => {
      if (this.stale(session) || Date.now() - started > SIDEGRADE_FOR_MS) return;
      if ((this.positionNow() > 0 || !startAt) && await this.trySidegrade(this.videoHeight)) return;
      if (!this.stale(session)) this.pollTimers.push(setTimeout(poll, SIDEGRADE_POLL_MS));
    };
    this.pollTimers.push(setTimeout(poll, SIDEGRADE_FIRST_MS));
  }

  /** Start low, switch up: below 4K on an HEVC TV, ask for the warmed 4K source; switch at the current position. */
  private scheduleUpgrade(session: number) {
    const src = this.source;
    const steppedDown = () => this.downgrades > 0 || !!this.cap || this.triedCaps.length > 0;
    if (!src || !playbackCaps().hevc || this.upgraded || steppedDown() || releaseHeight(src.quality) >= 2160) return;
    const started = Date.now();
    const poll = async () => {
      if (this.stale(session) || this.upgraded || steppedDown() || Date.now() - started > UPGRADE_FOR_MS) return;
      const quality = this.videoHeight || releaseHeight(src.quality);
      if (quality >= 2160) return;
      if (quality > 0 && this.positionNow() > 0 && this.everPlayed) {
        const up = await checkUpgrade(paramsOf(this.meta), src.hash, quality);
        if (this.stale(session)) return;
        if (up && releaseHeight(up.quality) > quality && !this.upgraded && !steppedDown()) {
          this.upgraded = true;
          this.switchTo(sourceOf(up), up.quality === '2160p' ? 'player.switchedTo4k' : 'player.switchedHigher');
          return;
        }
      }
      this.pollTimers.push(setTimeout(poll, UPGRADE_POLL_MS));
    };
    this.pollTimers.push(setTimeout(poll, UPGRADE_POLL_MS));
  }

  // ---- controls ----

  togglePlay() {
    const v = this.video;
    if (this.state.phase !== 'ready' || !v.getAttribute('src')) return;
    const hold = this.hold;
    if (hold) {
      // The viewer decides: Play during the start buffer starts now, Pause during a stall stays paused
      if (hold.kind === 'start') this.play();
      else this.pause();
      return;
    }
    if (v.paused) v.play().catch(() => {});
    else v.pause();
  }

  play() {
    this.clearHold();
    if (this.video.paused && this.video.getAttribute('src')) this.video.play().catch(() => {});
  }

  pause() {
    if (this.hold) {
      this.clearHold();
      this.set({ playing: false });
    }
    if (!this.video.paused) this.video.pause();
  }

  seekTo(t: number) {
    const duration = this.state.duration;
    if (!this.hash || duration <= 0) return;
    const target = Math.max(0, Math.min(duration - 1, t));
    const v = this.video;
    const rel = target - this.offset;
    // Already buffered: seek in place, no new stream
    if (!this.state.ended && rel >= 0 && inRanges(v.buffered, rel, 0.5) && inRanges(v.seekable, rel, 0)) {
      v.currentTime = rel;
      if (Math.abs(v.currentTime - rel) < 1) {
        this.set({ position: target });
        return;
      }
    }
    this.restartAt(target);
  }

  // Atmos only when the track is copied (the server re-encodes AC-3 / E-AC-3 to 5.1 for now)
  private audioBadge(tracks: AudioTrackInfo[], index: number): string {
    const track = tracks.find(t => t.index === index);
    if (!track) return '';
    if (track.direct && this.source?.format?.atmos && /e-?ac-?3|truehd/i.test(track.codec)) return 'Atmos';
    if (track.channels >= 8) return track.direct ? '7.1' : '5.1';
    return track.channels >= 6 ? '5.1' : '';
  }

  setAudio(n: number) {
    if (n === this.state.audio) return;
    this.set({ audio: n, audioBadge: this.audioBadge(this.state.audioTracks, n) });
    this.restartAt(this.positionNow());
    const lang = normLang(this.state.audioTracks.find(a => a.index === n)?.language);
    const keepOriginal = readPlayerPrefs(this.pid).audioLang === 'original' && lang === normLang(this.meta.original_language);
    if (lang && !keepOriginal) writePlayerPrefs(this.pid, { audioLang: lang });
  }

  setSubtitle(id: string | null) {
    this.subDecided = true;
    this.subUserPicked = true;
    const s = id ? (this.state.subs || []).find(x => x.id === id) : null;
    writePlayerPrefs(this.pid, { sub: s ? { lang: normLang(s.language), id: s.id } : 'off' });
    this.applySubtitle(id);
  }

  private applySubtitle(id: string | null) {
    clearTimeout(this.cueTimer);
    this.set({ subId: id, cues: [] });
    if (!id) return;
    const session = this.session;
    // A live embedded track (big file) is read from the playing position on: ask again while it grows
    const load = () => fetchCueFile(id, this.positionNow()).then(f => {
      if (this.stale(session) || this.state.subId !== id) return;
      if (f.cues.length > 0 || !f.partial) this.set({ cues: f.cues });
      if (f.partial) this.cueTimer = setTimeout(load, LIVE_CUES_POLL_MS);
    });
    load();
  }

  /** The viewer chose the release that carries the profile language's subtitle: restart on it at the same position. */
  switchSubAlt() {
    const src = this.subAltSource;
    if (!src || this.state.subAlt?.busy) return;
    this.subAltSource = null;
    this.wantSubLang = this.locale === 'tr' ? 'tr' : 'en';
    this.set({ subAlt: null });
    this.switchTo(src, 'player.switchedSubtitles');
  }

  retry() {
    if (this.state.phase === 'error') {
      this.startItem({ refresh: true });
      return;
    }
    this.set({ fatal: null });
    this.recoveries = 0;
    if (!this.sourceFailed(this.positionNow(), false)) this.restartAt(this.positionNow());
  }

  // ---- subtitles ----

  private loadSubtitles(session: number) {
    const m = this.meta;
    if (!m.tmdb_id) {
      this.set({ subs: [] });
      return;
    }
    const langs = this.locale === 'tr' ? ['tr', 'en'] : ['en', 'tr'];
    const src = this.source;
    const q = {
      tmdb_id: m.tmdb_id,
      media_type: m.media_type,
      season: m.season,
      episode: m.episode,
      hash: this.hash,
      index: this.index,
      langs,
      title: src?.torrent_title || src?.file_name || undefined,
      year: m.year,
    };
    const hasPreferred = (items: SubtitleItem[]) => hasSubtitleLang(items, langs[0]);
    let list: SubtitleItem[] = [];
    this.subAltSource = null;
    this.set({ subAlt: null });
    const show = (items: SubtitleItem[]) => {
      list = items;
      this.set({ subs: items });
      // The viewer switched to this release for its subtitle: show it as their choice
      if (this.wantSubLang && hasSubtitleLang(items, this.wantSubLang)) {
        const id = bestSubtitleOf(items, this.wantSubLang);
        writePlayerPrefs(this.pid, { sub: { lang: this.wantSubLang } });
        this.wantSubLang = '';
        this.subDecided = this.subUserPicked = true;
        this.applySubtitle(id);
        return;
      }
      // Default pick; a later, fuller list picks again while nothing is shown and the viewer chose nothing
      if (this.subUserPicked || (this.subDecided && this.state.subId)) return;
      this.subDecided = true;
      const track = this.state.audioTracks.find(a => a.index === this.state.audio);
      const id = pickSubtitle(items, { profileLang: this.locale, pref: readPlayerPrefs(this.pid).sub, audioLang: track?.language });
      if (id) this.applySubtitle(id);
    };
    // The profile's language is needed when the audio is in another one
    const needed = () => {
      const audio = normLang(this.state.audioTracks.find(a => a.index === this.state.audio)?.language) || normLang(m.original_language);
      return !hasPreferred(list) && audio !== langs[0];
    };
    // Late uploads: while it is missing the list is asked again every 10 minutes (caches skipped); a new
    // subtitle in that language is added and shown unless the viewer chose one
    const lateCheck = () => this.pollTimers.push(setTimeout(() => {
      if (this.stale(session) || hasPreferred(list)) return;
      fetchSubtitles(q, { refresh: true }).then(more => {
        if (this.stale(session)) return;
        const late = lateSubtitles(list, more, langs[0], this.subUserPicked || !!this.state.subId);
        if (!late.arrived) {
          if (more.length > 0) show(late.items);
          this.findSubAlt(session, langs[0]);
          lateCheck();
          return;
        }
        this.subAltSource = null;
        this.set({ subAlt: null });
        list = late.items;
        this.set({ subs: late.items });
        if (late.pick) {
          this.subDecided = true;
          this.applySubtitle(late.pick);
        }
        this.toast('player.subtitleFound');
      });
    }, LATE_SUBTITLE_POLL_MS));
    // After the retries: a release that carries the missing language is offered in the panel
    const settled = () => {
      if (this.stale(session) || !needed()) return;
      this.findSubAlt(session, langs[0]);
      lateCheck();
    };
    fetchSubtitles(q).then(items => {
      if (this.stale(session)) return;
      show(items);
      if (hasPreferred(items)) return;
      // The server answers with what is ready and keeps asking slower providers for its next call
      let left = SUBTITLE_RETRY_MS.length;
      SUBTITLE_RETRY_MS.forEach(ms => this.pollTimers.push(setTimeout(() => {
        if (this.stale(session)) return;
        if (hasPreferred(list)) { if (--left === 0) settled(); return; }
        fetchSubtitles(q).then(more => {
          if (this.stale(session)) return;
          if (more.length > 0) show(mergeSubtitles(list, more));
          if (--left === 0) settled();
        });
      }, ms)));
    });
  }

  /** Asks the server for a release of the title whose subtitles include lang (it may scan in the background). */
  private findSubAlt(session: number, lang: string) {
    const src = this.source;
    if (!src || this.subAltSource) return;
    let tries = 0;
    const ask = () => {
      tries++;
      checkSubAlt(paramsOf(this.meta, this.cap), src.hash, this.videoHeight || releaseHeight(src.quality), lang).then(r => {
        if (this.stale(session)) return;
        const h = r.source ? r.source.hash.toLowerCase() : '';
        if (r.source && h !== this.current && !this.slow.has(h)) {
          this.subAltSource = sourceOf(r.source);
          this.set({ subAlt: { busy: false } });
          return;
        }
        if (r.retryAfter && tries < SUBALT_RETRY_MAX) this.pollTimers.push(setTimeout(ask, r.retryAfter * 1000));
      });
    };
    ask();
  }

  // ---- episodes ----

  private loadSeasons() {
    const m = this.meta;
    if (m.media_type !== 'tv') return;
    const ready = (s: SeasonInfo[]) => {
      this.seasons = s;
      this.updateNext();
      this.fillEpisodeInfo();
    };
    if (this.seasons.length > 0) ready(this.seasons);
    else tvSeasons(m.tmdb_id).then(s => { if (!this.destroyed) ready(s); });
  }

  private updateNext() {
    const m = this.meta;
    const next = m.media_type === 'tv' && m.season && m.episode ? nextEpisodeOf(this.seasons, m.season, m.episode) : null;
    this.set({ next });
  }

  // Episode title (and runtime for later resolves) when only the numbers are known
  private fillEpisodeInfo() {
    const m = this.meta;
    if (m.media_type !== 'tv' || !m.season || !m.episode || (m.episode_title && m.runtime)) return;
    const season = this.seasons.find(s => s.season_number === m.season);
    if (!season) return;
    seasonEpisodes(m.tmdb_id, season).then(eps => {
      const ep = eps.find(e => e.episode_number === m.episode);
      if (!ep || this.meta !== m) return;
      const episode_title = m.episode_title || (isPlaceholderEpisodeName(ep) ? undefined : ep.name);
      const runtime = m.runtime || ep.runtime || undefined;
      this.meta = { ...m, episode_title, runtime };
      this.set({ meta: this.meta });
    });
  }

  playNext() {
    const next = this.state.next;
    if (!next) return;
    this.stopCountdown();
    // The finished episode is done; the next one was warmed for this moment
    releaseTitle(paramsOf(this.meta));
    this.nextWarm = null;
    this.meta = { ...this.meta, season: next.season, episode: next.episode, episode_title: undefined, runtime: undefined };
    this.startAt = undefined;
    this.given = undefined;
    this.autoRefreshed = false;
    this.avoid = [];
    this.lengthRetries = 0;
    this.set({ meta: this.meta, ended: false });
    this.updateNext();
    this.fillEpisodeInfo();
    this.startItem({});
  }

  private startCountdown() {
    this.stopCountdown();
    this.set({ countdown: 5 });
    this.countdownTimer = setInterval(() => {
      const n = (this.state.countdown ?? 1) - 1;
      if (n <= 0) {
        this.playNext();
        return;
      }
      this.set({ countdown: n });
    }, 1000);
  }

  stopCountdown() {
    if (this.countdownTimer) clearInterval(this.countdownTimer);
    this.countdownTimer = undefined;
    if (this.state.countdown !== null) this.set({ countdown: null });
  }

  // ---- progress ----

  private persist(opts: { finished?: boolean; keepalive?: boolean } = {}) {
    const m = this.meta;
    const duration = this.state.duration;
    // duration is reset while a new source opens, so a late pause event never saves a mix of two sources
    if (this.destroyed || !this.pid || !this.hash || duration <= 0 || this.state.phase !== 'ready') return;
    const pos = opts.finished ? duration : this.exact ? Math.min(duration, this.positionNow()) : this.offset;
    if (pos < 10) return;
    saveProgress({
      profile_id: this.pid,
      media_type: m.media_type,
      tmdb_id: m.tmdb_id,
      title: m.title,
      poster_path: m.poster_path || '',
      backdrop_path: m.backdrop_path || '',
      season: m.media_type === 'tv' ? m.season || 0 : 0,
      episode: m.media_type === 'tv' ? m.episode || 0 : 0,
      episode_title: m.episode_title || '',
      magnet: this.source?.magnet || '',
      hash: this.hash,
      file_index: this.index,
      position: Math.round(pos * 10) / 10,
      duration: Math.round(duration * 10) / 10,
    }, opts.keepalive);
  }

  // The TV's Home button sends the app to the background: pause and save
  private onVisibility = () => {
    if (!document.hidden) return;
    this.pause();
    this.persist({ keepalive: true });
  };

  /** Saves progress, stops the stream and lets the server drop every torrent of this session. */
  destroy() {
    if (this.destroyed) return;
    this.persist();
    this.destroyed = true;
    this.cancelResolve();
    this.stopStream();
    clearInterval(this.saveTimer);
    clearInterval(this.healthTimer);
    if (this.countdownTimer) clearInterval(this.countdownTimer);
    clearTimeout(this.toastTimer);
    document.removeEventListener('visibilitychange', this.onVisibility);
    releaseTitle(paramsOf(this.meta));
    if (this.nextWarm) releaseTitle(this.nextWarm);
    releaseTorrents(Array.from(this.played));
    this.listeners.clear();
  }
}

function inRanges(r: TimeRanges, t: number, endPad: number): boolean {
  for (let i = 0; i < r.length; i++) {
    if (t >= r.start(i) && t <= r.end(i) - endPad) return true;
  }
  return false;
}

import { base, getJson, request } from '../lib/http';
import type { Locale, MessageKey } from '../lib/i18n';
import { getProfileId } from '../lib/profile';
import { seasonEpisodes, tvSeasons, isPlaceholderEpisodeName, type SeasonInfo } from '../lib/catalog';
import {
  StreamError, capsQuery, checkSidegrade, checkSubAlt, checkUpgrade, findProgress, isFinished, nextEpisodeOf, nextWarmDue, paramsOf,
  keepSourceAlive, playbackCaps, prefetchResolve, readPlayerPrefs, releaseTitle, releaseTorrents, resolveSource, reuseSource, runtimeCheckQuery,
  saveProgress, sourceOf, titleProgress, writePlayerPrefs,
  type Availability, type PlayRequest, type QualityCap, type ResolveParams, type ResolvedSource, type StreamMeta, type StreamSource, restrictToSafeAudio,
} from '../lib/stream';
import {
  LATE_SUBTITLE_POLL_MS, LIVE_CUES_POLL_MS, SUBALT_RETRY_MAX, bestSubtitleOf, fetchCueFile, fetchSubtitles,
  hasSubtitleLang, lateSubtitles, mergeSubtitles, normLang, pickAudioTrack, pickSubtitle,
  type AudioTrackInfo, type Cue, type SubtitleItem,
} from '../lib/subtitles';
import { BufferMonitor, bufferedEnd, type BufferVerdict } from './bufferHealth';
import { healthRatio, holdDone, needsStartHold, type HoldKind } from './startBuffer';
import { lengthsMatch, timeScale } from './switchLength';
import { MseStream, mseAvailable, type MseFailure, type StreamUrls } from './mseStream';
import { PlaybackLog, markSeek, type LogContext } from './playbackLog';

// Port of web/src/features/watch/components/TorrentStreamPlayer.tsx for one <video> element.
// The stream is a live fragmented MP4 requested at `t` that really starts at `offset`
// (/seekpoint), so positions are offset + video.currentTime. Seeks outside the buffer
// restart the stream. The stream reaches the element through MSE (mseStream.ts); webOS decodes
// no frame of it as `src`. Audio the TV takes only on its own (E-AC-3 / Atmos) plays in split mode:
// a video and an audio stream on the file's timeline (offset 0, no seekpoint needed).
// Upgrades, sidegrades and audio track changes are spliced into the playing MediaSource without a
// restart; a source switch with a restart (loading screen) only follows a real stall.

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
  /** The source plays from TorBox (debrid) */
  debrid: boolean;
  /** The source is a cinema recording (played with cinema=1) */
  cinema: boolean;
  /** "Atmos" / "7.1" / "5.1" for the selected track as it reaches the TV */
  audioBadge: string;
  subs: SubtitleItem[] | null;
  subId: string | null;
  cues: Cue[];
  /** Seconds the subtitles are shown later (negative: earlier), for the playing file */
  subDelay: number;
  toast: MessageKey | '';
  /** Another release carries the profile language's subtitle that the playing file lacks (subtitle panel row) */
  subAlt?: { busy: boolean } | null;
  countdown: number | null;
  next: { season: number; episode: number } | null;
}

const STALL_MS = 12000;
const FIRST_START_MS = 30000;
// A torrent without a first frame this long after it opened hands over to a faster source (slowStart)
const SLOW_START_MS = 12000;
const MAX_RECOVERIES = 3;
// A failed source is resolved again (without it) this many times per item before an error shows
const MAX_AUTO_REFRESH = 3;
// While the viewer pauses the server keeps the file ready; after a long pause a stall restarts quickly
const KEEPALIVE_MS = 15000;
const LONG_PAUSE_MS = 10000;
const RESUME_STALL_MS = 2500;
const LONG_STALL_MS = 8000;
const STALL_WINDOW_MS = 60000;
// A stall counts once playback waited this long (waiting, so under 1 s buffered); shorter ones are hiccups
const MIN_STALL_MS = 3000;
// A wait mid-playback shows buffering and holds for a larger buffer only once it lasted this long:
// shorter waits (a gap of a few frames, a decoder hiccup) play on by themselves
const STALL_CONFIRM_MS = 800;
const MAX_DOWNGRADES = 2;
// The first upgrade poll comes soon after the start (a TorBox 4K is often ready at once), then every 10 s
const UPGRADE_FIRST_MS = 5000;
const UPGRADE_POLL_MS = 10000;
const UPGRADE_FOR_MS = 5 * 60 * 1000;
// An upgrade of another codec family (no splice) this early in playback restarts on it at once
const UPGRADE_RESTART_MS = 90 * 1000;
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
// A /seekpoint that failed (its pieces were not in yet) is asked again after these delays
const SEEKPOINT_RETRY_MS = [3000, 10000, 30000];
const MAX_SUB_DELAY_S = 60;
// Source switches without a stall are spliced into the playing MediaSource (mseStream.ts) about
// SPLICE_LEAD_S ahead; a restart (loading screen) only follows a real stall. A sidegrade offer must
// measure at least BETTER_RATIO (rate / bitrate) and more than the playing source.
const SPLICE_LEAD_S = 10;
const SPLICE_MIN_LEAD_S = 6;
const AUDIO_SPLICE_LEAD_S = 3;
const BETTER_RATIO = 1.3;
const SPLIT_KEY = 'nfx-tv:split';

type TrackInfo = AudioTrackInfo & { split_direct?: boolean };

/** Debug override (localStorage nfx-tv:split = "always" | "never"); "" = by codec */
function splitOverride(): string {
  try {
    return localStorage.getItem(SPLIT_KEY) || '';
  } catch {
    return '';
  }
}

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
  // Where the item starts (saved progress included): a retry before any position was shown resumes here
  private resumePos = 0;
  private given?: StreamSource;
  private seasons: SeasonInfo[];

  // source
  private origin: Origin = 'given';
  private source?: StreamSource;
  private autoRefreshes = 0;
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
  // Split mode: the server takes ?track= (probe `split`), and the TV refused no split stream yet
  private serverSplit = false;
  private splitBroken = false;
  // Position asked for, shown until a split stream's data is in (its media time is the file's)
  private startPos = 0;
  private splitMode = false;
  private splicing = false;
  // Why the last splice did not happen; an upgrade of another codec family waits for the next seek
  private spliceFail = '';
  private deferredUpgrade: { src: StreamSource; toast: MessageKey } | null = null;
  private realStallTimer?: Timer;
  private stallConfirmTimer?: Timer;
  private hasPlayed = false;
  private everPlayed = false;
  private recoveries = 0;
  private stallTimer?: Timer;
  private stallArmed = false;
  // The viewer's pause (0 = none), its keep-alive timer, and the last Play after a long pause
  private pausedAt = 0;
  private keepAliveTimer?: ReturnType<typeof setInterval>;
  private resumedAt = 0;
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
  private subDecided = false;
  private subUserPicked = false;
  // A spliced-in file: its list picks the subtitle again (the old file's may be timed for the other release)
  private subRepick = false;
  private seekRetry?: Timer;

  // session wide
  private played = new Set<string>();
  private current = '';
  private nextWarm: ResolveParams | null = null;
  private saveTimer: ReturnType<typeof setInterval>;
  private countdownTimer?: ReturnType<typeof setInterval>;
  private toastTimer?: Timer;
  // Playback diagnostics (playbackLog.ts); null when switched off
  private plog: PlaybackLog | null = null;

  constructor(video: HTMLVideoElement, req: PlayRequest, locale: Locale) {
    this.video = video;
    this.locale = locale;
    this.meta = req.meta;
    this.startAt = req.startAt;
    this.given = req.source;
    this.seasons = req.seasons || [];
    this.state = {
      phase: 'resolving', // a saved source is checked by the server first (reuseSource)
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
      debrid: false,
      cinema: false,
      subs: null,
      subId: null,
      cues: [],
      subDelay: 0,
      toast: '',
      countdown: null,
      next: null,
    };
    this.bindVideo();
    this.plog = PlaybackLog.create(video, () => this.positionNow(), () => this.logContext());
    this.saveTimer = setInterval(() => { if (this.state.playing) this.persist(); }, 10000);
    this.healthTimer = setInterval(() => this.sampleHealth(), 1000);
    document.addEventListener('visibilitychange', this.onVisibility);
    // Debug handle for DevTools (state, positions, the [restart] / [splice] logs)
    (window as any).__nxPlayer = this;
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
    // Every publish re-renders the whole player (slow on TV CPUs): nothing changed, nothing published
    const cur = this.state as any;
    const p = patch as any;
    let changed = false;
    for (const k in p) {
      if (cur[k] !== p[k]) {
        changed = true;
        break;
      }
    }
    if (!changed) return;
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
    if (this.splitMode && this.video.buffered.length === 0) return this.startPos;
    return this.offset + (this.video.currentTime || 0);
  }

  /** The file the stream plays (trick play thumbnails). */
  currentFile(): { hash: string; index: number } | null {
    return this.hash ? { hash: this.hash, index: this.index } : null;
  }

  /** File time the subtitle overlay shows. */
  subtitleTime(): number {
    return this.positionNow() - this.state.subDelay;
  }

  /** Subtitle timing (seconds, + shows them later); kept for the playing file like on the web */
  setSubDelay(seconds: number): number {
    const d = Math.round(Math.max(-MAX_SUB_DELAY_S, Math.min(MAX_SUB_DELAY_S, seconds)) * 10) / 10;
    if (d !== this.state.subDelay) this.set({ subDelay: d });
    return d;
  }

  private stale(session: number) {
    return this.destroyed || session !== this.session;
  }

  private logContext(): LogContext {
    const m = this.meta;
    return {
      type: m.media_type, tmdb: m.tmdb_id, s: m.season, e: m.episode, title: m.title,
      src: this.hash ? `${this.hash.slice(0, 8)}:${this.index}` : '',
      mode: this.splitMode ? 'split' : this.noMse ? 'src' : 'muxed',
      debrid: !!this.source?.debrid, q: this.state.quality?.label || '', dur: Math.round(this.state.duration),
    };
  }

  // ---- item: resolve the source ----

  /** fresh: resolve (with avoid) even though a saved or given source exists, keeping the server's cached source. */
  private async startItem(retry: { refresh?: boolean; fresh?: boolean; startAt?: number; switchTo?: StreamSource; wrongLength?: boolean }) {
    this.cancelResolve();
    const startPos = retry.startAt ?? this.startAt;
    if (retry.switchTo) return this.openSource(retry.switchTo, startPos || 0, 'fresh');

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
    // Published on a new stage or second (and once past 300 ms, when the status text shows), not every tick
    this.resolveTick = setInterval(() => {
      const ms = Date.now() - t0;
      const prev = this.state.resolveElapsed;
      if (stage(ms) !== this.state.stage || Math.floor(ms / 1000) !== Math.floor(prev / 1000) || (prev < 300 && ms >= 300)) {
        this.set({ resolveElapsed: ms, stage: stage(ms) });
      }
    }, 250);

    const m = this.meta;
    try {
      let start = startPos || 0;
      // Reuse the torrent saved with earlier progress instead of searching again
      let saved: StreamSource | undefined;
      let origin: Origin = 'given';
      if (!retry.refresh && !retry.fresh) {
        if (this.given) {
          saved = this.given;
        } else if (this.pid) {
          const entries = await titleProgress(this.pid, m.media_type, m.tmdb_id);
          if (ctrl.signal.aborted) return;
          const p = m.media_type === 'tv' ? findProgress(entries, m.season || 0, m.episode || 0) : findProgress(entries);
          if (p?.hash) {
            saved = { hash: p.hash, index: p.file_index, magnet: p.magnet || undefined };
            origin = 'saved';
            start = this.startAt ?? (isFinished(p) ? 0 : Math.max(0, p.position - 5));
          }
        }
      }
      this.resumePos = start;
      if (saved) {
        // The same TorBox / swarm decision as resolve: TorBox when it has the release cached, the
        // torrent when its swarm keeps up at the resume position, else a fresh resolve without it
        const a = await reuseSource(paramsOf(m, this.cap), saved, start, ctrl.signal);
        if (ctrl.signal.aborted) return;
        if (!a || a.action === 'play') {
          this.cancelResolve();
          this.openSource(a && a.source ? sourceOf(a.source) : saved, start, origin);
          return;
        }
        const h = saved.hash.toLowerCase();
        console.info(`[restart] saved source ${h.slice(0, 8)} not reused (${a.reason || 'slow'}): resolving again`);
        if (this.avoid.indexOf(h) < 0) this.avoid = [...this.avoid, h];
      }
      const r = await resolveSource(paramsOf(m, this.cap), ctrl.signal, { refresh: retry.refresh, avoid: this.avoid });
      if (ctrl.signal.aborted) return;
      this.cancelResolve();
      this.openSource(sourceOf(r), start, r.cached ? 'cached' : 'fresh');
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

  /** A failed source is resolved again (up to MAX_AUTO_REFRESH times per item); a fresh one is avoided. */
  private sourceFailed(position: number, auto: boolean): boolean {
    if (auto && this.autoRefreshes >= MAX_AUTO_REFRESH) return false;
    if (auto) {
      this.autoRefreshes++;
      // A fresh source that failed is not picked again; a reused one gets a fresh resolve
      const failed = this.source?.hash.toLowerCase();
      if (this.origin === 'fresh' && failed && this.avoid.indexOf(failed) < 0) this.avoid = [...this.avoid, failed];
    }
    this.startItem({ refresh: true, startAt: position });
    return true;
  }

  // ---- source: probe, then stream ----

  private async openSource(src: StreamSource, startAt: number, origin: Origin, reason = 'open') {
    const prev = this.source && this.source.hash.toLowerCase() !== src.hash.toLowerCase() ? { src: this.source, origin: this.origin, duration: this.state.duration } : null;
    // Sent first: queued events belong to the previous source's context
    this.plog?.flush();
    this.plog?.note('open', { why: reason, origin, to: `${src.hash.slice(0, 8)}:${src.index}`, at: startAt, debrid: !!src.debrid });
    this.stopStream();
    const session = ++this.session;
    // Counted from the open: a slow probe (the header from a weak swarm) counts too
    if (!src.debrid) this.pollTimers.push(setTimeout(() => this.slowStart(session), SLOW_START_MS));
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
    // The subtitle timing belongs to the file: kept when the same one opens again
    const subDelay = this.hash === h && this.index === src.index ? this.state.subDelay : 0;
    this.hash = h;
    this.index = src.index;
    this.subDecided = false;
    this.subUserPicked = false;
    this.subRepick = false;
    this.noMse = !mseAvailable();
    this.set({
      phase: 'ready', error: null, unreleased: null, fatal: null, ended: false, buffering: true, reconnecting: false,
      status: 'player.connecting', duration: 0, position: startAt, audioTracks: [], audio: 0,
      quality: qualityOf(null, src), audioBadge: '', subs: null, subId: null, cues: [], subDelay, debrid: !!src.debrid, cinema: !!src.cinema,
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
    const tracks: TrackInfo[] = Array.isArray(probe?.audio_tracks) ? probe.audio_tracks : [];
    this.serverSplit = probe?.split === true;
    // Another cut of the title (a WEB-DL vs a BluRay): the same moment, mapped by the lengths
    if (reason !== 'open' && reason !== 'revert' && prev && prev.duration > 0 && duration > 0 && lengthsMatch(prev.duration, duration)) {
      startAt *= timeScale(prev.duration, duration);
    }
    // A switch never starts over: a file that does not reach the position (a pack's wrong file, another
    // cut) sends the player back to the source it came from (its torrent is kept 60 s after its stream)
    if (reason !== 'open' && reason !== 'revert' && prev && startAt > 30 && duration > 0 &&
        (startAt >= duration - 10 || (prev.duration > 0 && Math.abs(duration - prev.duration) > 60))) {
      console.info(`[restart] guard: ${h.slice(0, 8)}:${src.index} lasts ${duration.toFixed(0)} s (was ${prev.duration.toFixed(0)}), position ${startAt.toFixed(0)}: back to ${prev.src.hash.slice(0, 8)}`);
      releaseTorrents([h]);
      this.openSource(prev.src, startAt, prev.origin, 'revert');
      return;
    }
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
    this.restartAt(startAt > 0 && (duration <= 0 || startAt < duration - 10) ? startAt : 0, reason);
    this.scheduleUpgrade(session);
    if (src.starving) this.scheduleSidegrade(session, startAt);
  }

  /**
   * A torrent without a first frame SLOW_START_MS after it opened: a sidegrade that keeps up, else resolve
   * again without it (a TorBox or faster release) at the same position, instead of restarting the same
   * swarm for minutes. TorBox sources keep the usual restarts.
   */
  private async slowStart(session: number) {
    const src = this.source;
    if (this.stale(session) || this.everPlayed || this.state.fatal || !src || src.debrid || this.autoRefreshes >= MAX_AUTO_REFRESH) return;
    const pos = this.positionNow() || this.state.position;
    const h = this.current;
    console.info(`[restart] no frame ${SLOW_START_MS / 1000} s after the start of ${h.slice(0, 8)}: trying another source`);
    this.slow.add(h);
    if (await this.trySidegrade(this.videoHeight, 'restart')) return;
    if (this.stale(session) || this.everPlayed) return;
    this.autoRefreshes++;
    if (this.avoid.indexOf(h) < 0) this.avoid = [...this.avoid, h];
    this.startItem({ fresh: true, startAt: pos });
  }

  private videoHeight = 0;

  private playUrl(t: number): string {
    return this.urlOf(this.hash, this.index, t, this.state.audio);
  }

  private urlOf(hash: string, index: number, t: number, audio: number, track = ''): string {
    const tr = track ? `&track=${track}` : '';
    return `${base()}/api/torrent-stream/play?hash=${hash}&index=${index}&t=${t.toFixed(3)}&audio=${audio}${capsQuery()}${tr}`;
  }

  /** The streams of one source at t: split (video + audio) or muxed */
  private streamUrls(hash: string, index: number, t: number, audio: number, split: boolean): StreamUrls {
    return split
      ? { video: this.urlOf(hash, index, t, audio, 'video'), audio: this.urlOf(hash, index, t, audio, 'audio') }
      : { muxed: this.urlOf(hash, index, t, audio) };
  }

  /**
   * Split mode for audio track n of the probed tracks: the server takes ?track= and the TV plays the
   * track's codec only on its own (E-AC-3 / AC-3), which the server then copies. `sticky`: a split
   * stream stays split (any codec works in an audio-only SourceBuffer), so a splice keeps its layout.
   */
  private wantsSplit(tracks: TrackInfo[], n: number, sticky = false): boolean {
    if (!this.serverSplit || this.noMse || this.splitBroken) return false;
    const o = splitOverride();
    if (o === 'never') return false;
    if (o === 'always' || (sticky && this.mse?.split)) return true;
    const tr = tracks.find(t => t.index === n);
    return !!tr && !tr.direct && !!tr.split_direct && playbackCaps().splitAudio.indexOf(tr.codec) >= 0;
  }

  /** Restarts the stream at `target`; /seekpoint tells where a muxed one really starts (copied video starts on a keyframe). */
  private restartAt(target: number, reason = 'restart') {
    if (!this.hash) return;
    this.health.reset();
    const seq = ++this.seekSeq;
    this.seekCtrl?.abort();
    this.seekCtrl = null;
    this.beginStall(true);
    this.disarmStall();
    clearTimeout(this.realStallTimer);
    const v = this.video;
    this.detach();
    const split = this.wantsSplit(this.state.audioTracks, this.state.audio);
    console.info(`[restart] ${reason} t=${target.toFixed(1)} ${this.hash.slice(0, 8)}:${this.index} ${split ? 'split' : this.noMse ? 'src' : 'muxed'}`);
    this.plog?.note('restart', { why: reason, at: target, mode: split ? 'split' : this.noMse ? 'src' : 'muxed', recoveries: this.recoveries });
    // Split streams carry the file's timestamps: position = currentTime
    this.splitMode = split;
    this.offset = split ? 0 : target;
    this.startPos = target;
    this.exact = split || target <= 0;
    this.hasPlayed = false;
    this.set({ buffering: true, ended: false, position: target });
    if (this.noMse) {
      v.src = this.playUrl(target);
    } else {
      this.mse = new MseStream(v, this.streamUrls(this.hash, this.index, target, this.state.audio, split), {
        onFail: e => this.onStreamFail(seq, e),
        onUnsupported: mimes => {
          if (seq !== this.seekSeq || this.destroyed) return;
          if (split) {
            // The TV refused a split SourceBuffer: muxed streams with converted audio from now on
            console.warn('[player] no SourceBuffer type for', mimes.join(' | '), '- leaving split mode');
            this.splitBroken = true;
          } else if (restrictToSafeAudio()) {
            // The plain src path shows no picture on webOS: ask for converted audio instead
            console.warn('[player] no SourceBuffer type for', mimes.join(' | '), '- retrying with AAC / Opus audio');
          } else {
            console.warn('[player] no SourceBuffer type for', mimes.join(' | '), '- playing the stream as src');
            this.noMse = true;
          }
          this.restartAt(target, 'codec');
        },
      }, split ? target : 0);
    }
    // A borderline or unmeasured swarm starts only with a larger buffer
    this.clearHold();
    // Only the first stream of a source holds; seeks and restarts play at once
    if (this.holdSource !== this.source && needsStartHold(this.source?.ratio) && !this.source?.debrid) {
      this.holdSource = this.source;
      this.startHold('start');
    }
    else v.play().catch(e => { if (e?.name !== 'AbortError') this.set({ playing: false }); });
    this.armStall();
    clearTimeout(this.seekRetry);
    if (target <= 0 || split) return;

    // Until the answer comes positions (and subtitles) run up to one keyframe interval ahead. An unknown
    // start (ffprobe waiting for pieces from a slow swarm) is asked again once the stream's data is cached,
    // instead of keeping the requested time as the offset for the whole stream.
    const q = `hash=${this.hash}&index=${this.index}&t=${target.toFixed(3)}&audio=${this.state.audio}${capsQuery()}`;
    const ask = (attempt: number) => {
      const ctrl = new AbortController();
      this.seekCtrl = ctrl;
      const timer = setTimeout(() => ctrl.abort(), 15000);
      getJson(`/api/torrent-stream/seekpoint?${q}`, { signal: ctrl.signal })
        .then(data => (typeof data?.t === 'number' && data.t >= 0 && data.t <= target + 1 && data.exact !== false ? data.t : NaN))
        .catch(() => NaN)
        .then(t => {
          clearTimeout(timer);
          if (seq !== this.seekSeq || this.destroyed) return;
          if (isNaN(t) && attempt < SEEKPOINT_RETRY_MS.length) {
            this.seekRetry = setTimeout(() => { if (seq === this.seekSeq && !this.destroyed) ask(attempt + 1); }, SEEKPOINT_RETRY_MS[attempt]);
            return;
          }
          if (isNaN(t)) console.info(`[restart] no seek point for t=${target.toFixed(1)}: positions may be off by a keyframe interval`);
          this.plog?.note('seekpoint', { at: target, fix: isNaN(t) ? 'none' : t - target, attempt });
          this.offset = isNaN(t) ? target : t;
          this.exact = true;
        });
    };
    ask(0);
  }

  private stopStream() {
    this.clearHold();
    this.disarmStall();
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
    this.plog?.note('stream-fail', { msg: String(e.message).slice(0, 60), played: this.hasPlayed });
    this.mse = null;
    if (this.hasPlayed) {
      this.recover();
      return;
    }
    this.disarmStall();
    if (!this.sourceFailed(this.positionNow(), true)) this.set({ fatal: { key: 'player.playbackFailed' } });
  }

  // ---- video events ----

  private videoListeners: [string, () => void][] = [];

  /** Video events; none runs after destroy() (stopStream and React's unmount pause the element later) */
  private bindVideo() {
    const v = this.video;
    const on = (type: string, fn: () => void) => {
      const h = () => { if (!this.destroyed) fn(); };
      this.videoListeners.push([type, h]);
      v.addEventListener(type, h);
    };
    on('play', () => {
      this.set({ playing: true });
      clearInterval(this.keepAliveTimer);
      if (this.pausedAt && Date.now() - this.pausedAt >= LONG_PAUSE_MS) this.resumedAt = Date.now();
      this.pausedAt = 0;
    });
    on('pause', () => {
      if (this.hold) return; // paused by the buffer hold, not the viewer
      this.set({ playing: false });
      this.persist();
      if (v.ended) return;
      this.pausedAt = Date.now();
      clearInterval(this.keepAliveTimer);
      this.keepAliveTimer = setInterval(() => { if (this.hash) keepSourceAlive(this.hash, this.index); }, KEEPALIVE_MS);
    });
    on('waiting', () => {
      // A real stall (still waiting with under 1 s buffered after MIN_STALL_MS) with a faster source
      // in hand: switch to it instead of waiting the stall out
      clearTimeout(this.realStallTimer);
      if (this.hasPlayed && !v.seeking) {
        this.realStallTimer = setTimeout(() => {
          const end = bufferedEnd(v.buffered, v.currentTime);
          const ahead = end >= 0 ? end - v.currentTime : 0;
          if (this.state.buffering && ahead < 1 && this.hold?.kind !== 'start' && this.pendingSide) this.usePending('stall');
        }, MIN_STALL_MS);
      }
      if (!this.hasPlayed) this.set({ buffering: true });
      // Not re-armed by every waiting: a stall hold ends (pause, play, waiting) every few seconds, which
      // kept the watchdog from ever restarting a stream that delivers nothing
      if (!this.stallArmed) this.armStall();
      if (this.hasPlayed) this.beginStall(false);
      if (this.hasPlayed) this.confirmStall();
    });
    on('playing', () => {
      clearTimeout(this.stallConfirmTimer);
      this.set({ buffering: false, reconnecting: false });
      this.disarmStall();
      this.endStall();
      this.hasPlayed = true;
      this.everPlayed = true;
      clearTimeout(this.stallResetTimer);
      this.stallResetTimer = setTimeout(() => { this.recoveries = 0; }, 60000);
    });
    on('timeupdate', () => this.onTime());
    on('ended', () => this.onEnded());
    on('error', () => this.onError());
  }

  private onTime() {
    const position = this.positionNow();
    // timeupdate fires ~4 times a second; the UI shows whole seconds (exact time: positionNow)
    if (Math.floor(position) !== Math.floor(this.state.position)) this.set({ position });
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
      this.plog?.note('early-end');
      this.recover();
      return;
    }
    this.disarmStall();
    this.set({ playing: false, ended: true });
    this.persist({ finished: true });
    // A countdown the end credits started goes on
    if (this.state.next && readPlayerPrefs(this.pid).autoplayNext !== false && !this.countdownTimer) this.startCountdown();
  }

  private onError() {
    if (!this.video.getAttribute('src') || this.state.phase !== 'ready') return;
    this.plog?.note('error', { code: this.video.error?.code ?? 0, played: this.hasPlayed });
    if (this.hasPlayed) {
      this.recover();
      return;
    }
    this.disarmStall();
    if (!this.sourceFailed(this.positionNow(), true)) this.set({ fatal: { key: 'player.playbackFailed' } });
  }

  private disarmStall() {
    clearTimeout(this.stallTimer);
    this.stallArmed = false;
  }

  private armStall() {
    clearTimeout(this.stallTimer);
    this.stallArmed = true;
    // Right after a long pause a stalled stream has probably died meanwhile: restart it quickly
    const base = !this.hasPlayed ? FIRST_START_MS : Date.now() - this.resumedAt < 60000 ? RESUME_STALL_MS : STALL_MS;
    const ms = base * (1 + this.recoveries);
    this.stallTimer = setTimeout(() => {
      this.stallArmed = false;
      // A paused player is not stalled (a buffer hold is)
      if (!this.video.paused || this.hold) this.recover();
    }, ms);
  }

  /** A wait mid-playback that lasts STALL_CONFIRM_MS: show buffering and resume only with a few seconds buffered. */
  private confirmStall() {
    clearTimeout(this.stallConfirmTimer);
    const v = this.video;
    const seq = this.seekSeq;
    const from = v.currentTime;
    this.stallConfirmTimer = setTimeout(() => {
      if (seq !== this.seekSeq || this.destroyed || !this.hasPlayed || v.ended || (v.paused && !this.hold)) return;
      if (v.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA && v.currentTime > from) return;
      this.plog?.note('buffering', { rs: v.readyState, seeking: v.seeking });
      this.set({ buffering: true });
      if (!v.seeking && !this.hold) this.startHold('stall');
    }, STALL_CONFIRM_MS);
  }

  /** Holds the element paused until startBuffer.ts says enough is buffered. */
  private holdSource: StreamSource | null | undefined = null;

  private startHold(kind: HoldKind) {
    this.clearHold();
    this.hold = { kind, since: kind === 'stall' ? Date.now() : null, ahead: 0 };
    this.plog?.note('hold', { kind });
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
    this.plog?.note('hold-end', { kind: h.kind, ahead, waited: h.since === null ? 0 : now - h.since });
    this.clearHold();
    v.play().catch(e => { if (e?.name !== 'AbortError') this.set({ playing: false }); });
  }

  private recover() {
    this.disarmStall();
    if (this.recoveries >= MAX_RECOVERIES) {
      // Out of restarts: another source first, an error only when that is out of tries too
      if (!this.sourceFailed(this.positionNow(), true)) this.set({ reconnecting: false, fatal: { key: 'player.connectionLost' } });
      return;
    }
    this.recoveries++;
    this.restartAt(this.positionNow(), 'stall');
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

  /** Real stalls (two within a minute, or one over LONG_STALL_MS): a faster source, else a lower resolution */
  private struggling() {
    this.stallTimes = [];
    if (this.pendingSide) {
      this.usePending('stall');
      return;
    }
    const quality = this.videoHeight;
    this.trySidegrade(quality, 'restart').then(switched => { if (!switched) this.stepDown(quality); });
  }

  /** Another source at the current position (restarts the stream: only after a real stall). */
  private switchTo(src: StreamSource, toast: MessageKey, reason: string) {
    this.lastSwitch = Date.now();
    // The shown position when the element has none (between streams)
    const pos = this.positionNow() || this.state.position;
    this.openSource(src, pos, 'fresh', reason);
    this.toast(toast);
  }

  /** A sidegrade offer measured clearly faster than the playing source */
  private betterOffer(up: ResolvedSource): boolean {
    const r = healthRatio(up.health);
    if (r === undefined || r < BETTER_RATIO) return false;
    const cur = this.source?.ratio;
    return cur === undefined || r > cur;
  }

  /** Asks for a faster source: `restart` switches at once (stalled or not started yet), `splice` seamlessly. */
  private async trySidegrade(quality: number, mode: 'restart' | 'splice'): Promise<boolean> {
    const src = this.source;
    if (!src || this.busy || this.splicing || Date.now() - this.lastSidegrade < SIDEGRADE_EVERY_MS) return false;
    const session = this.session;
    this.busy = true;
    let offer: StreamSource | null = null;
    try {
      const up = await checkSidegrade(paramsOf(this.meta, this.cap), src.hash, quality || releaseHeight(src.quality), undefined,
        { state: 'critical', avoid: Array.from(this.slow) });
      if (!up || up.hash.toLowerCase() === src.hash.toLowerCase() || this.stale(session)) return false;
      this.lastSidegrade = Date.now();
      if (this.slow.has(up.hash.toLowerCase()) || !this.betterOffer(up)) {
        console.info(`[sidegrade] offer ${up.hash.slice(0, 8)} not better (ratio ${healthRatio(up.health)?.toFixed(2)})`);
        releaseTorrents([up.hash.toLowerCase()]);
        return false;
      }
      offer = sourceOf(up);
      if (mode === 'restart') {
        this.slow.add(this.current);
        this.switchTo(offer, 'player.switchedFaster', 'sidegrade');
        return true;
      }
    } finally {
      this.busy = false;
    }
    const from = this.current;
    if (await this.spliceTo(offer, 'sidegrade', 'player.switchedFaster')) {
      this.slow.add(from);
      return true;
    }
    // Held for a real stall
    if (!this.stale(session) && !this.pendingSide) this.pendingSide = offer;
    else releaseTorrents([offer.hash.toLowerCase()]);
    return false;
  }

  // ---- buffer health: proactive sidegrade ----

  private sampleHealth() {
    const v = this.video;
    const s = this.state;
    // readyState: a wait not confirmed as a stall yet (confirmStall) is not smooth playback either
    if (s.phase !== 'ready' || !v.getAttribute('src') || v.paused || v.seeking || s.buffering || s.ended || s.fatal ||
        v.readyState < HTMLMediaElement.HAVE_FUTURE_DATA || !this.hasPlayed || !this.exact) return;
    const pos = this.offset + v.currentTime;
    const end = bufferedEnd(v.buffered, v.currentTime);
    const fileEnd = end >= 0 ? this.offset + end : pos;
    this.onHealth(this.health.push({
      at: Date.now(), pos, end: fileEnd,
      fetching: this.fetching(),
      atEnd: s.duration > 0 && fileEnd >= s.duration - 1,
      cap: this.mse ? this.mse.aheadCap() : undefined,
    }));
  }

  private onHealth(h: BufferVerdict) {
    const pending = this.pendingSide;
    if (pending) {
      if (h.healthyMs >= RECOVERED_MS) {
        // Recovered: the held source is not needed
        this.pendingSide = null;
        releaseTorrents([pending.hash.toLowerCase()]);
      } else if (h.state !== 'healthy') {
        // Seamless while the buffer lasts; a restart only follows a real stall
        this.splicePending();
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
    console.info(`[health] ${h.state} ahead=${h.ahead.toFixed(1)} growth=${h.growth === null ? '-' : h.growth.toFixed(2)}`);
    this.plog?.note('health', { state: h.state, ahead: h.ahead, growth: h.growth ?? undefined });
    this.askSidegrade(h.state === 'critical' ? 'critical' : 'draining');
  }

  /** The buffer drains: ask for a faster source; only a clearly faster one is held (and spliced in). */
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
      if (this.stale(session) || this.slow.has(h) || !this.betterOffer(up)) {
        console.info(`[sidegrade] offer ${h.slice(0, 8)} not better (ratio ${healthRatio(up.health)?.toFixed(2)})`);
        releaseTorrents([h]);
        return;
      }
      this.pendingSide = sourceOf(up);
    } finally {
      this.busy = false;
    }
  }

  private splicePending() {
    const next = this.pendingSide;
    if (!next || this.splicing) return;
    const from = this.current;
    this.spliceTo(next, 'sidegrade', 'player.switchedFaster').then(ok => {
      if (!ok) return;
      if (this.pendingSide === next) this.pendingSide = null;
      this.slow.add(from);
    });
  }

  /** Switches to the held faster source with a restart (after a real stall). */
  private usePending(reason: string) {
    const next = this.pendingSide;
    if (!next || this.splicing) return;
    this.pendingSide = null;
    this.slow.add(this.current);
    this.switchTo(next, 'player.switchedFaster', reason);
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
        this.switchTo(sourceOf(r), 'player.switchedHd', 'stepdown');
      })
      .catch(() => {})
      .then(() => { this.busy = false; });
  }

  /**
   * A source the resolve measured as starving: look for a faster one early (also before the first frame
   * when starting at 0). Before the first frame it restarts on it; once playing it is spliced in.
   */
  private scheduleSidegrade(session: number, startAt: number) {
    const started = Date.now();
    const poll = async () => {
      if (this.stale(session) || Date.now() - started > SIDEGRADE_FOR_MS) return;
      if ((this.positionNow() > 0 || !startAt) && await this.trySidegrade(this.videoHeight, this.everPlayed ? 'splice' : 'restart')) return;
      if (!this.stale(session)) this.pollTimers.push(setTimeout(poll, SIDEGRADE_POLL_MS));
    };
    this.pollTimers.push(setTimeout(poll, SIDEGRADE_FIRST_MS));
  }

  /** Start low, switch up: below 4K on an HEVC TV, ask for the warmed 4K source and splice it in. */
  private scheduleUpgrade(session: number) {
    const src = this.source;
    const steppedDown = () => this.downgrades > 0 || !!this.cap || this.triedCaps.length > 0;
    // After a step down (a slow swarm) only a TorBox source comes back up: it plays at line speed
    if (!src || !playbackCaps().hevc || this.upgraded || releaseHeight(src.quality) >= 2160) return;
    const started = Date.now();
    const poll = async () => {
      if (this.stale(session) || this.upgraded || Date.now() - started > UPGRADE_FOR_MS) return;
      const quality = this.videoHeight || releaseHeight(src.quality);
      if (quality >= 2160) return;
      if (quality > 0 && this.positionNow() > 0 && this.everPlayed) {
        const up = await checkUpgrade(paramsOf(this.meta), src.hash, quality);
        if (this.stale(session)) return;
        if (up && releaseHeight(up.quality) > quality && !this.upgraded && (!steppedDown() || up.debrid)) {
          // Back up from a step down: later resolves are no longer capped
          if (steppedDown()) {
            this.cap = undefined;
            this.triedCaps = [];
          }
          const secs = () => `${Math.round((Date.now() - started) / 1000)} s after the start`;
          const toast: MessageKey = up.quality === '2160p' ? 'player.switchedTo4k' : 'player.switchedHigher';
          console.info(`[upgrade] ${up.hash.slice(0, 8)}${up.debrid ? ' (TorBox)' : ''} offered ${secs()}`);
          // Seamless where possible: a restart would show the loading screen while playback is fine
          if (await this.spliceTo(sourceOf(up), 'upgrade', toast)) {
            console.info(`[upgrade] ${up.hash.slice(0, 8)} spliced ${secs()}`);
            this.upgraded = true;
            return;
          }
          if (this.spliceFail === 'unsupported' && !this.stale(session)) {
            this.upgraded = true;
            // Another codec family cannot be spliced on this TV. Early on a short restart onto it beats
            // the lower quality for the whole title; later it waits for the next seek (a restart anyway)
            if (Date.now() - started < UPGRADE_RESTART_MS) {
              console.info(`[upgrade] ${up.hash.slice(0, 8)}: codec change, restarting on it ${secs()}`);
              this.lastSwitch = Date.now();
              this.openSource(sourceOf(up), this.positionNow(), 'fresh', 'upgrade');
              this.toast(toast);
              return;
            }
            console.info(`[upgrade] ${up.hash.slice(0, 8)} waits for the next seek (codec change)`);
            this.deferredUpgrade = { src: sourceOf(up), toast };
            return;
          }
          if (this.stale(session)) return;
        }
      }
      this.pollTimers.push(setTimeout(poll, UPGRADE_POLL_MS));
    };
    this.pollTimers.push(setTimeout(poll, UPGRADE_FIRST_MS));
  }

  // ---- seamless switch (splice into the playing MediaSource) ----

  /** The track of `tracks` closest to the playing one (same language, same channels first) */
  private matchingTrack(tracks: TrackInfo[]): number {
    const cur = this.state.audioTracks.find(t => t.index === this.state.audio);
    const lang = normLang(cur?.language);
    const same = lang ? tracks.filter(t => normLang(t.language) === lang) : [];
    const pick = same.find(t => t.channels === cur?.channels) || same[0];
    if (pick) return pick.index;
    return pickAudioTrack(tracks, { profileLang: this.locale, originalLang: this.meta.original_language, prefLang: readPlayerPrefs(this.pid).audioLang });
  }

  /** Where a stream of this file asked for at t starts (the keyframe before t for copied video); NaN when unknown */
  private async seekPointOf(hash: string, index: number, t: number, audio: number, split: boolean): Promise<number> {
    const q = `hash=${hash}&index=${index}&t=${t.toFixed(3)}&audio=${audio}${capsQuery()}${split ? '&track=video' : ''}`;
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 10000);
    try {
      const d = await getJson(`/api/torrent-stream/seekpoint?${q}`, { signal: ctrl.signal });
      // Without exact: the stream may start elsewhere, and a muxed splice needs its start exactly
      return typeof d?.t === 'number' && d.t >= 0 && d.t <= t + 1 && (d.exact !== false) ? d.t : NaN;
    } catch {
      return NaN;
    } finally {
      clearTimeout(timer);
    }
  }

  /**
   * Splices the streams of hash:index (audio track n) into the playing MediaSource about `lead` s
   * ahead, inside what is buffered. The layout stays (split stays split, muxed stays muxed);
   * `audioOnly` replaces only the audio of a split stream. Returns the media time it took over at.
   */
  private async spliceStreams(mse: MseStream, hash: string, index: number, n: number, lead: number, minLead: number,
    readyS: number, audioOnly: boolean, reason: string): Promise<number | null> {
    this.spliceFail = '';
    const v = this.video;
    const now = this.positionNow();
    const end = bufferedEnd(v.buffered, v.currentTime);
    const latest = (end >= 0 ? this.offset + end : now) - 1;
    if (latest < now + minLead) {
      console.info(`[splice] ${reason}: only ${(latest + 1 - now).toFixed(1)} s buffered`);
      return null;
    }
    const split = mse.split;
    let target = Math.min(now + lead, latest);
    let t0 = target;
    for (let i = 0; i < 3; i++) {
      t0 = await this.seekPointOf(hash, index, target, n, split);
      if (this.mse !== mse) return null;
      if (isNaN(t0)) {
        console.info(`[splice] ${reason}: no seek point for ${hash.slice(0, 8)}`);
        return null;
      }
      if (t0 >= this.positionNow() + minLead || target >= latest) break;
      target = Math.min(latest, target + Math.max(4, this.positionNow() + minLead - t0 + 1));
    }
    if (t0 < this.positionNow() + minLead) {
      console.info(`[splice] ${reason}: no keyframe in reach (${t0.toFixed(2)})`);
      return null;
    }
    const urls: StreamUrls = audioOnly ? { audio: this.urlOf(hash, index, target, n, 'audio') } : this.streamUrls(hash, index, target, n, split);
    const tsOffset = split ? 0 : t0 - this.offset;
    console.info(`[splice] ${reason} prepare ${hash.slice(0, 8)}:${index} audio=${n} at ${t0.toFixed(2)} (now ${this.positionNow().toFixed(2)}, ${split ? 'split' : 'muxed'})`);
    const res = await mse.splice(urls, tsOffset, t0 - this.offset, readyS);
    if (!res.ok) {
      console.info(`[splice] ${reason} dropped: ${res.reason}`);
      this.plog?.note('splice-drop', { why: reason, res: res.reason });
      this.spliceFail = res.reason;
      return null;
    }
    console.info(`[splice] ${reason} committed at ${(res.cut + this.offset).toFixed(2)} (now ${this.positionNow().toFixed(2)}) ${mse.mime}`);
    this.plog?.note('splice', { why: reason, to: `${hash.slice(0, 8)}:${index}`, audio: n, cut: res.cut + this.offset });
    return res.cut;
  }

  /** Switches to src without a restart; false when it could not (nothing changed then). */
  private async spliceTo(src: StreamSource, reason: string, toast: MessageKey): Promise<boolean> {
    const mse = this.mse;
    this.spliceFail = '';
    if (!mse || this.splicing || mse.splicing || this.state.phase !== 'ready' || !this.hasPlayed || !this.exact) return false;
    this.splicing = true;
    const session = this.session;
    const h = src.hash.toLowerCase();
    const live = () => !this.stale(session) && this.mse === mse;
    try {
      if (src.magnet) {
        try {
          await request(`/api/torrent-stream/files?magnet=${encodeURIComponent(src.magnet)}`);
        } catch {
          /* the probe reports real failures */
        }
        if (!live()) return false;
      }
      let probe: any;
      try {
        probe = await getJson(`/api/torrent-stream/probe?hash=${h}&index=${src.index}${capsQuery()}${runtimeCheckQuery(this.meta)}`);
      } catch {
        return false;
      }
      if (!live()) return false;
      const duration = Number(probe?.duration) || 0;
      // Spliced only where the same time is the same moment (within 10 s); a cut further off restarts on it
      if (probe?.duration_mismatch || duration <= 0 || !lengthsMatch(this.state.duration, duration) || timeScale(this.state.duration, duration) !== 1) {
        console.info(`[splice] ${reason}: ${h.slice(0, 8)} has another length (${duration.toFixed(0)} s)`);
        return false;
      }
      const tracks: TrackInfo[] = Array.isArray(probe?.audio_tracks) ? probe.audio_tracks : [];
      if (mse.split && probe?.split !== true) return false;
      const audio = this.matchingTrack(tracks);
      const cut = await this.spliceStreams(mse, h, src.index, audio, SPLICE_LEAD_S, SPLICE_MIN_LEAD_S, 4, false, reason);
      if (cut === null || !live()) return false;
      // The new source plays from `cut` on
      const old = this.current;
      const next = ++this.session;
      this.source = src;
      this.origin = 'fresh';
      this.hash = h;
      this.index = src.index;
      this.current = h;
      this.played.add(h);
      this.lastSwitch = Date.now();
      this.serverSplit = probe?.split === true;
      const video: VideoInfo | null = probe?.width > 0
        ? { codec: probe.video_codec || '', width: probe.width, height: probe.height || 0, hdr: !!probe.hdr, dolbyVision: Number(probe.dolby_vision) || 0, direct: !!probe.direct }
        : null;
      this.videoHeight = video ? videoQuality(video.width, video.height) : 0;
      this.set({ duration, audioTracks: tracks, audio, quality: qualityOf(video, src), audioBadge: this.audioBadge(tracks, audio), debrid: !!src.debrid, cinema: !!src.cinema, subDelay: 0 });
      this.health.reset();
      // The old file's subtitle stays until the new list picks one for this file (as the web player does)
      this.subRepick = true;
      this.loadSubtitles(next);
      this.scheduleUpgrade(next);
      this.watchSplice(cut, old, toast, reason);
      return true;
    } finally {
      this.splicing = false;
    }
  }

  /** The audio track changes without a restart; false when it could not. */
  private async spliceAudio(n: number): Promise<boolean> {
    const mse = this.mse;
    if (!mse || this.splicing || mse.splicing || this.state.phase !== 'ready' || !this.hasPlayed || !this.exact) return false;
    // A muxed stream stays muxed: a track the TV only plays on its own needs a restart in split mode
    if (!mse.split && this.wantsSplit(this.state.audioTracks, n)) return false;
    this.splicing = true;
    try {
      const cut = mse.split
        ? await this.spliceStreams(mse, this.hash, this.index, n, AUDIO_SPLICE_LEAD_S, 2, 2, true, 'audio')
        : await this.spliceStreams(mse, this.hash, this.index, n, 5, 3, 2, false, 'audio');
      if (cut === null || this.mse !== mse || this.state.audio !== n) return false;
      this.watchSplice(cut, '', '', 'audio');
      return true;
    } finally {
      this.splicing = false;
    }
  }

  /** Done once playback passed the cut with frames advancing: the old torrent can go. */
  private watchSplice(cut: number, old: string, toast: MessageKey | '', reason: string) {
    const v = this.video;
    const frames = () => (v.getVideoPlaybackQuality ? v.getVideoPlaybackQuality().totalVideoFrames : 0);
    const frames0 = frames();
    const started = Date.now();
    const session = this.session;
    const check = () => {
      const passed = v.currentTime >= cut + 0.5 && frames() > frames0;
      if (!passed && !this.stale(session) && Date.now() - started < 60000) {
        this.pollTimers.push(setTimeout(check, 250));
        return;
      }
      console.info(`[splice] ${reason} ${passed ? 'done' : 'not reached'} at ${this.positionNow().toFixed(2)} ${v.videoWidth}x${v.videoHeight} frames=${frames() - frames0}`);
      this.plog?.note('splice-end', { why: reason, ok: passed, ms: Date.now() - started, frames: frames() - frames0 });
      if (old && old !== this.current) {
        releaseTorrents([old]);
        this.played.delete(old);
      }
      if (toast && passed && !this.stale(session)) this.toast(toast);
    };
    check();
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

  /** `why` names the seek in the playback log (user, skip-intro ...) */
  seekTo(t: number, why = 'user') {
    const duration = this.state.duration;
    if (!this.hash || duration <= 0) return;
    const target = Math.max(0, Math.min(duration - 1, t));
    const v = this.video;
    const rel = target - this.offset;
    // Already buffered: seek in place, no new stream
    if (!this.state.ended && rel >= 0 && inRanges(v.buffered, rel, 0.5) && inRanges(v.seekable, rel, 0)) {
      markSeek(why);
      v.currentTime = rel;
      if (Math.abs(v.currentTime - rel) < 1) {
        this.set({ position: target });
        return;
      }
    }
    const up = this.deferredUpgrade;
    if (up) {
      this.deferredUpgrade = null;
      this.lastSwitch = Date.now();
      this.openSource(up.src, target, 'fresh', 'upgrade');
      this.toast(up.toast);
      return;
    }
    this.restartAt(target, why === 'user' ? 'seek' : `seek ${why}`);
  }

  // Atmos only when the track is copied (muxed, or on its own in split mode); converted audio is 5.1 at most
  private audioBadge(tracks: TrackInfo[], index: number): string {
    const track = tracks.find(t => t.index === index);
    if (!track) return '';
    const direct = track.direct || (!!track.split_direct && this.wantsSplit(tracks, index, true));
    if (direct && this.source?.format?.atmos && /e-?ac-?3|truehd/i.test(track.codec)) return 'Atmos';
    if (track.channels >= 8) return direct ? '7.1' : '5.1';
    return track.channels >= 6 ? '5.1' : '';
  }

  setAudio(n: number) {
    if (n === this.state.audio) return;
    this.set({ audio: n, audioBadge: this.audioBadge(this.state.audioTracks, n) });
    // Spliced in a few seconds ahead; a restart when that cannot work
    this.spliceAudio(n).then(ok => {
      if (!ok && this.state.audio === n && !this.destroyed) this.restartAt(this.positionNow(), 'audio');
    });
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
    let loads = 0;
    let wasPartial = false;
    // A live embedded track (big file) is read from the playing position on: ask again while it grows
    const load = () => fetchCueFile(id, this.positionNow()).then(f => {
      if (this.stale(session) || this.state.subId !== id) return;
      if (f.cues.length === 0 && !f.partial) {
        // Nothing came (an OpenSubtitles quota answers 429): the next one of the language, another provider first
        const alt = this.subtitleFallback(id);
        console.info(`[subtitles] ${id.slice(0, 16)} gave no cues${alt ? `, trying ${alt.slice(0, 16)}` : ''}`);
        if (alt) this.applySubtitle(alt);
        return;
      }
      // The first cues, and the final file replacing a partial one (live track done, or timed to the file)
      if (loads++ === 0 || (wasPartial && !f.partial)) {
        const prev = this.state.cues[0]?.start;
        const first = f.cues[0]?.start;
        this.plog?.note(loads === 1 ? 'subs' : 'subs-swap', { id: id.slice(0, 12), cues: f.cues.length, partial: f.partial,
          shift: prev !== undefined && first !== undefined ? first - prev : undefined });
      }
      wasPartial = f.partial;
      if (f.cues.length > 0 || !f.partial) this.set({ cues: f.cues });
      if (f.partial) this.cueTimer = setTimeout(load, LIVE_CUES_POLL_MS);
    });
    load();
  }

  private subFailed = new Set<string>();

  /** Another subtitle of the failed one's language (not one that failed, another provider first) */
  private subtitleFallback(failed: string): string | null {
    this.subFailed.add(failed);
    const items = this.state.subs || [];
    const bad = items.find(x => x.id === failed);
    if (!bad) return null;
    const lang = normLang(bad.language);
    const pool = items.filter(x => normLang(x.language) === lang && !this.subFailed.has(x.id) && x.forced === bad.forced);
    const rank = (x: SubtitleItem) => (x.source !== bad.source ? 0 : 2) + (x.synced ? 0 : 1);
    pool.sort((a, b) => rank(a) - rank(b));
    return pool[0]?.id ?? null;
  }

  /** The viewer chose the release that carries the profile language's subtitle: restart on it at the same position. */
  switchSubAlt() {
    const src = this.subAltSource;
    if (!src || this.state.subAlt?.busy) return;
    this.subAltSource = null;
    this.wantSubLang = this.locale === 'tr' ? 'tr' : 'en';
    this.set({ subAlt: null });
    this.spliceTo(src, 'subalt', 'player.switchedSubtitles').then(ok => {
      if (!ok && !this.destroyed) this.switchTo(src, 'player.switchedSubtitles', 'subalt');
    });
  }

  retry() {
    // The last shown position (positionNow keeps an old offset without a stream), else where the item was to start
    const at = this.state.position > 0 ? this.state.position : this.resumePos;
    if (this.state.phase === 'error') {
      this.startItem({ refresh: true, startAt: at });
      return;
    }
    this.set({ fatal: null });
    this.recoveries = 0;
    if (!this.sourceFailed(at, false)) this.restartAt(at, 'retry');
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
      const repick = this.subRepick && items.length > 0;
      if (repick) this.subRepick = false;
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
      if (!repick && (this.subUserPicked || (this.subDecided && this.state.subId))) return;
      this.subDecided = true;
      const track = this.state.audioTracks.find(a => a.index === this.state.audio);
      const id = pickSubtitle(items, { profileLang: this.locale, pref: readPlayerPrefs(this.pid).sub, audioLang: track?.language });
      if (repick ? id !== this.state.subId : id) this.applySubtitle(id);
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

  playNext(opts: { fromCredits?: boolean } = {}) {
    const next = this.state.next;
    if (!next) return;
    // Leaving from the end credits: this episode counts as watched
    if (opts.fromCredits && !this.state.ended) this.persist({ finished: true });
    this.plog?.note('next', { credits: !!opts.fromCredits });
    this.plog?.flush();
    this.stopCountdown();
    if (this.deferredUpgrade) {
      releaseTorrents([this.deferredUpgrade.src.hash.toLowerCase()]);
      this.deferredUpgrade = null;
    }
    // The finished episode is done; the next one was warmed for this moment
    releaseTitle(paramsOf(this.meta));
    this.nextWarm = null;
    this.meta = { ...this.meta, season: next.season, episode: next.episode, episode_title: undefined, runtime: undefined };
    this.startAt = undefined;
    this.given = undefined;
    this.autoRefreshes = 0;
    this.avoid = [];
    this.lengthRetries = 0;
    // Each episode may switch up once
    this.upgraded = false;
    this.resumePos = 0;
    this.set({ meta: this.meta, ended: false, position: 0 });
    this.updateNext();
    this.fillEpisodeInfo();
    this.startItem({});
  }

  /** The end credits started (skipSegments.ts): count down to the next episode while they play */
  creditsStarted() {
    if (this.state.next && !this.state.ended && !this.countdownTimer && readPlayerPrefs(this.pid).autoplayNext !== false) this.startCountdown();
  }

  private startCountdown() {
    this.stopCountdown();
    this.set({ countdown: 5 });
    this.countdownTimer = setInterval(() => {
      // Paused during the credits: the countdown waits
      if (!this.state.ended && this.video.paused) return;
      const n = (this.state.countdown ?? 1) - 1;
      if (n <= 0) {
        this.playNext({ fromCredits: true });
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
    // Nothing played from this source yet: the saved progress stays (a retry or failed start never overwrites it)
    if (!opts.finished && !this.everPlayed) return;
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
    this.plog?.note('exit');
    this.plog?.destroy();
    this.plog = null;
    this.destroyed = true;
    this.cancelResolve();
    this.stopStream();
    clearInterval(this.saveTimer);
    clearInterval(this.healthTimer);
    clearInterval(this.keepAliveTimer);
    if (this.countdownTimer) clearInterval(this.countdownTimer);
    clearTimeout(this.toastTimer);
    clearTimeout(this.realStallTimer);
    clearTimeout(this.stallConfirmTimer);
    clearTimeout(this.seekRetry);
    clearTimeout(this.cueTimer);
    this.videoListeners.forEach(([type, h]) => this.video.removeEventListener(type, h));
    this.videoListeners = [];
    document.removeEventListener('visibilitychange', this.onVisibility);
    releaseTitle(paramsOf(this.meta));
    if (this.nextWarm) releaseTitle(this.nextWarm);
    if (this.deferredUpgrade) this.played.add(this.deferredUpgrade.src.hash.toLowerCase());
    releaseTorrents(Array.from(this.played));
    this.listeners.clear();
    if ((window as any).__nxPlayer === this) delete (window as any).__nxPlayer;
  }
}

function inRanges(r: TimeRanges, t: number, endPad: number): boolean {
  for (let i = 0; i < r.length; i++) {
    if (t >= r.start(i) && t <= r.end(i) - endPad) return true;
  }
  return false;
}

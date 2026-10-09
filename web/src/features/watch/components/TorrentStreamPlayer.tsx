"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Play, Pause, ArrowLeft, Maximize, Minimize, Volume2, VolumeX, Loader2, RotateCcw, RotateCw, Film, SkipForward, AlertCircle, CalendarClock, MessageSquare, ChevronsLeft, ChevronsRight, RotateCwSquare, Clapperboard } from 'lucide-react';
import { getApiUrl } from '@/shared/lib/api';
import { getActiveProfileId } from '@/shared/lib/profile';
import { useFormat, useLocale, useT, type Locale, type MessageKey, type TFunction } from '@/shared/i18n';
import AudioSubtitlePanel from '@/features/watch/components/AudioSubtitlePanel';
import SubtitleOverlay from '@/features/watch/components/SubtitleOverlay';
import SeekThumbnail from '@/features/watch/components/SeekThumbnail';
import TouchSeekBar from '@/features/watch/components/TouchSeekBar';
import {
  Cue,
  SubtitleItem,
  bestSubtitleOf,
  fetchSubtitleFile,
  LATE_SUBTITLE_POLL_MS,
  lateSubtitles,
  nextSubtitleAfterFailure,
  type SubtitleFailure,
  LIVE_CUES_POLL_MS,
  SUBALT_RETRY_MAX,
  fetchSubtitles,
  groupSubtitles,
  languageName,
  hasSubtitleLang,
  mergeSubtitles,
  SUBTITLE_RETRY_MS,
  normLang,
  pickAudioTrack,
  pickSubtitle,
  readPlayerPrefs,
  writePlayerPrefs,
} from '@/features/watch/subtitles';
import { AUDIO_ORIGINAL, SUBTITLE_SIZE_SCALE } from '@/features/settings/playerPrefs';
import {
  StreamMeta,
  StreamSource,
  SeasonInfo,
  resolveSource,
  reuseSource,
  prefetchResolve,
  findProgress,
  getTitleProgress,
  getTvSeasons,
  getOriginalLanguage,
  getSeasonEpisodes,
  saveProgress,
  nextEpisodeOf,
  isFinished,
  episodeLabel,
  capsQuery,
  playbackCaps,
  QualityCap,
  checkUpgrade,
  checkSidegrade,
  checkSubAlt,
  runtimeCheckQuery,
  nextWarmDue,
  holdPlaying,
  releaseTitle,
  releaseTorrents,
  keepSourceAlive,
  ResolveParams,
  Availability,
  StreamApiError,
} from '@/features/watch/api';
import { SwitchPrep, type PreparedStream, type SwitchHost, type SwitchKind, type SwitchResult, type VideoInfo } from '@/features/watch/seamlessSwitch';
import { BufferMonitor, bufferedEnd, type BufferVerdict } from '@/features/watch/bufferHealth';
import { useScrollLock } from '@/features/watch/useScrollLock';
import { healthRatio, holdDone, needsStartHold, type HoldKind } from '@/features/watch/startBuffer';
import { guardedStart } from '@/features/watch/rewindGuard';
import { skipPhase, useSkipSegments } from '@/features/watch/skipSegments';
import { useMobileFullscreen, useNoBounce, useTouchUi, useViewportFitCover, useWakeLock } from '@/features/watch/mobile';
import mobileCss from '@/features/watch/components/mobilePlayer.module.css';

// PlayerCore's seamless switch, for the parent
interface CoreSwitch {
  /** Load `next` in the background and take over without a loading screen */
  prepare(next: StreamSource, kind: SwitchKind): Promise<SwitchResult>;
  /** The running preparation, if any */
  preparing(): { kind: SwitchKind; ready: boolean } | null;
  /** A preparation runs or a switch is still fading in */
  busy(): boolean;
  /** Drop the running preparation (it resolves 'cancelled') */
  cancel(): void;
  /** Why the last abandoned preparation was dropped */
  failure(): string;
  /** How the last preparation went (timings, restarts) */
  stats(): string;
}

interface StreamFile {
  id: number;
  name: string;
  path: string;
  size: number;
}

interface AudioTrack {
  index: number;
  language: string;
  title: string;
  codec: string;
  channels: number;
  default?: boolean;
}

export interface TorrentStreamPlayerProps {
  /** Legacy mode: list this torrent's files and let the user pick one */
  magnet?: string;
  /** Already resolved torrent file; skips resolving */
  source?: StreamSource;
  /** Title info: enables auto-resolve, progress saving and next episode */
  meta?: StreamMeta;
  /** Display title (defaults to meta.title) */
  title?: string;
  /** Start position in seconds */
  startAt?: number;
  /** TV only: episode counts per season (fetched when missing) */
  seasons?: SeasonInfo[];
  onClose: () => void;
}

interface PlayItem {
  meta?: StreamMeta;
  source?: StreamSource;
  magnet?: string;
  startAt?: number;
}

// Where the playing source came from: only reused ones get a fresh resolve on failure
type SourceOrigin = 'given' | 'saved' | 'cached' | 'fresh';

// A stall longer than this restarts the stream (doubles with each retry)
const STALL_MS = 12000;
// The first start may wait for peers, so it gets more time
const FIRST_START_MS = 30000;
// A torrent without a first frame this long after it opened hands over to a faster source
// (sidegrade, else a fresh resolve without it) instead of restarting the same swarm
const SLOW_START_MS = 12000;
const MAX_RECOVERIES = 3;
// A failed source is resolved again (without it) this many times per item before an error shows
const MAX_AUTO_REFRESH = 3;
// Pause keep-alive and the check after a long pause (the stream may have died meanwhile)
const KEEPALIVE_MS = 15000;
const LONG_PAUSE_MS = 10000;
const RESUME_CHECK_MS = 2500;
// A stream without new data this long counts as dead once playback stalls
const QUIET_STREAM_MS = 10000;
// How long after a long pause a stall still counts as a stream that died during it
const AFTER_PAUSE_MS = 5 * 60 * 1000;
// The loading overlay shows after this long without frames and hides once frames ran this long
const LOADING_SHOW_MS = 400;
const LOADING_HIDE_MS = 300;
// A wait mid-playback counts as buffering (and holds for a larger buffer) only once it lasted this
// long: shorter waits (a gap of a few frames, a decoder hiccup) play on by themselves
const STALL_CONFIRM_MS = 800;
// Adaptive quality: two stalls within STALL_WINDOW_MS, or one longer than LONG_STALL_MS, on a
// 1080p+ source re-resolve with a lower cap (4K -> 1080p -> 720p, never lower, never back up)
const LONG_STALL_MS = 8000;
const STALL_WINDOW_MS = 60000;
const MIN_STALL_MS = 1500;
const MAX_DOWNGRADES = 2;
const TOAST_MS = 4000;
// Upgrade (Netflix-like start low, switch up): below 4K the server is asked UPGRADE_FIRST_MS
// after the start (a TorBox 4K is often ready at once), then every UPGRADE_POLL_MS during the
// first UPGRADE_FOR_MS whether a better source keeps up now.
// At most one upgrade per session, and none once quality had to step down.
const UPGRADE_FIRST_MS = 5000;
const UPGRADE_POLL_MS = 10000;
const UPGRADE_FOR_MS = 5 * 60 * 1000;
// A dropped preparation is tried once more after this (a TorBox file needs no swarm to warm up)
const UPGRADE_RETRY_MS = 60000;
const UPGRADE_RETRY_DEBRID_MS = 15000;
// A file whose length does not fit the TMDB runtime is replaced at most this often per item
const MAX_LENGTH_RETRIES = 2;
// Sidegrade: a starving source is swapped for an alternative of the same (or higher) quality
// that keeps up. The server keeps those warm only during the first minutes of playback.
// The first look comes early: a weak swarm may not even deliver the first frames
const SIDEGRADE_FIRST_MS = 8000;
const SIDEGRADE_POLL_MS = 15000;
const SIDEGRADE_FOR_MS = 3 * 60 * 1000;
const SIDEGRADE_EVERY_MS = 2 * 60 * 1000;
// Proactive sidegrade: while the buffer drains (bufferHealth.ts) the server is asked right away,
// then every 5 s backing off to 10 s, and an offer loads in the hidden slot before the stall.
// At most one source switch per minute; a sidegrade still loading is dropped once the stream
// was healthy again for RECOVERED_MS.
const PROACTIVE_POLL_MS = 5000;
const PROACTIVE_POLL_MAX_MS = 10000;
const SWITCH_EVERY_MS = 60000;
const RECOVERED_MS = 15000;

// Resolve request of a title / episode (optionally capped after a step down)
const paramsOf = (m: StreamMeta, maxq?: QualityCap): ResolveParams => ({
  type: m.media_type, title: m.title, tmdb_id: m.tmdb_id, year: m.year, season: m.season, episode: m.episode, runtime: m.runtime, maxq,
  cinema: m.cinema,
});

const sourceOf = (r: { hash: string; index: number; magnet?: string; file_name?: string; torrent_title?: string; quality?: string; hdr?: boolean; debrid?: boolean; cinema?: boolean; health?: { rate: number; need: number; dead?: boolean } }): StreamSource => ({
  hash: r.hash, index: r.index, magnet: r.magnet, file_name: r.file_name, torrent_title: r.torrent_title, quality: r.quality, hdr: r.hdr,
  debrid: r.debrid, cinema: r.cinema,
  ratio: healthRatio(r.health),
});

const releaseHeight = (q?: string) => (q === '2160p' ? 2160 : q === '1080p' ? 1080 : q === '720p' ? 720 : 0);

/** PlayerCore's subtitle report: the file `hash` has (missing: false) or lacks a subtitle in the profile's `lang` */
interface SubtitleState {
  lang: string;
  hash: string;
  missing: boolean;
  /** a late re-query brought it */
  arrived?: boolean;
}

// Resolution class of a video (2160 / 1080 / 720 / 480); widescreen files are less tall
const videoQuality = (w: number, h: number) => (w >= 3200 || h >= 1800 ? 2160 : w >= 1800 || h >= 1000 ? 1080 : w >= 1200 || h >= 700 ? 720 : w > 0 || h > 0 ? 480 : 0);

// The cap below a source of this quality, or null when it is already 720p or lower
const lowerCap = (quality: number): QualityCap | null => (quality >= 2160 ? 1080 : quality >= 1080 ? 720 : null);

// `on` with hysteresis: turns true after it held onMs, false after it was off for offMs (no flicker
// of the loading overlay between quick restarts)
function useSteady(on: boolean, onMs: number, offMs: number): boolean {
  const [value, setValue] = useState(on);
  useEffect(() => {
    if (on === value) return;
    const timer = setTimeout(() => setValue(on), on ? onMs : offMs);
    return () => clearTimeout(timer);
  }, [on, value, onMs, offMs]);
  return value;
}

// Browser tests: with localStorage nfx-debug=1, `window.__nfxFailPlays = n` makes the next n play streams fail
const debugFailPlay = () => {
  if (typeof window === 'undefined') return false;
  const w = window as unknown as { __nfxFailPlays?: number };
  try {
    if (localStorage.getItem('nfx-debug') !== '1') return false;
  } catch {
    return false;
  }
  if (!w.__nfxFailPlays || w.__nfxFailPlays <= 0) return false;
  w.__nfxFailPlays--;
  return true;
};

const formatTime = (s: number) => {
  if (!isFinite(s) || s < 0) s = 0;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
  return `${h > 0 ? h + ':' : ''}${mm}:${String(sec).padStart(2, '0')}`;
};

const fill = (value: number, max: number) => (max > 0 ? Math.min(100, (value / max) * 100) : 0);

const formatSize = (b: number) => (b >= 1e9 ? `${(b / 1e9).toFixed(2)} GB` : `${(b / 1e6).toFixed(0)} MB`);

const subtitleOf = (meta: StreamMeta | undefined, locale: Locale) => {
  if (!meta || meta.media_type !== 'tv') return '';
  return [episodeLabel(meta.season, meta.episode, locale), meta.episode_title].filter(Boolean).join(' · ');
};

const resolveStageKey = (ms: number, refreshing: boolean, wrongLength: boolean): MessageKey => {
  if (wrongLength) return 'player.findingComplete';
  if (ms < 4000) return refreshing ? 'player.tryingAnother' : 'player.findingSource';
  if (ms < 12000) return 'player.searchingIndexers';
  return 'player.checkingSources';
};

const BADGE_CLASS = 'px-1.5 py-0.5 rounded-sm border border-white/50 text-white/90 text-[11px] font-bold leading-none tracking-wide';

const resolutionLabel = (w: number, h: number) => {
  if (w >= 3200 || h >= 1800) return '4K';
  if (w >= 1800 || h >= 1000) return '1080p';
  if (w >= 1200 || h >= 700) return '720p';
  return w > 0 || h > 0 ? 'SD' : '';
};

// Badge for what is really shown: the original when copied, at most 1080p SDR when re-encoded.
// Before the probe answers it falls back to the release tags from resolve.
const qualityBadge = (t: TFunction, v: VideoInfo | null, source?: StreamSource) => {
  if (v && v.width > 0) {
    const codec = v.codec.toUpperCase();
    const w = v.direct ? v.width : Math.min(1920, v.width);
    const h = Math.round(v.height * (w / v.width));
    const size = `${v.width}×${v.height}`;
    const tip = v.direct ? t('player.qualityOriginal', { codec, size }) : t('player.qualityConverted', { codec, size });
    return { label: resolutionLabel(w, h), hdr: v.direct && v.hdr, tip };
  }
  if (!source?.quality) return null;
  return { label: source.quality === '2160p' ? '4K' : source.quality, hdr: !!source.hdr && playbackCaps().hevc, tip: t('player.qualityRelease') };
};

// Netflix-style exit at the top left: closes the player (progress saved, torrents released)
// and shows the screen it was opened from
function BackButton({ label, onClick, onFocus, className = '' }: { label: string; onClick: () => void; onFocus?: () => void; className?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      onFocus={onFocus}
      className={`shrink-0 p-1 rounded text-white drop-shadow-[0_2px_4px_rgba(0,0,0,0.8)] transition-transform hover:scale-110 focus:outline-none focus-visible:ring-2 focus-visible:ring-white/80 ${className}`}
      title={label}
      aria-label={label}
    >
      <ArrowLeft className="w-9 h-9" strokeWidth={2.25} />
    </button>
  );
}

const inRanges = (r: TimeRanges, t: number, endPad: number) => {
  for (let i = 0; i < r.length; i++) {
    if (t >= r.start(i) && t <= r.end(i) - endPad) return true;
  }
  return false;
};

// Opens a title in the torrent player. With only `meta` it finds the best
// source itself (or reuses the saved one), then hands off to PlayerCore.
export default function TorrentStreamPlayer({ magnet, source, meta, title, startAt, seasons: seasonsProp, onClose }: TorrentStreamPlayerProps) {
  useScrollLock();
  const t = useT();
  const f = useFormat();
  const locale = useLocale();
  const containerRef = useRef<HTMLDivElement>(null);
  const [item, setItem] = useState<PlayItem>({ meta, source, magnet, startAt });
  // Resolve answered 425: the title / episode is not out yet
  const [unreleased, setUnreleased] = useState<Availability | null>(null);
  const [itemKey, setItemKey] = useState(0);
  const [attempt, setAttempt] = useState(0);
  // A saved source with title info is checked by the server first (reuseSource), so it starts resolving
  const [phase, setPhase] = useState<'resolving' | 'ready' | 'error'>((source && !meta) || (magnet && !source) ? 'ready' : 'resolving');
  // n keys PlayerCore: a new source always mounts a new PlayerCore with that source's props.
  // (Keyed by attempt, it mounted once with the previous source and start, so a hard switch
  // started the new source at the session's first start position, usually 0.)
  const [resolved, setResolvedState] = useState<{ source?: StreamSource; startAt: number; origin: SourceOrigin; n: number }>({
    source,
    startAt: startAt || 0,
    origin: 'given',
    n: 0,
  });
  const setResolved = useCallback((r: { source?: StreamSource; startAt: number; origin: SourceOrigin }) => {
    setResolvedState(prev => ({ ...r, n: prev.n + 1 }));
  }, []);
  const [resolveError, setResolveError] = useState('');
  const [resolveElapsed, setResolveElapsed] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const [wrongLength, setWrongLength] = useState(false);
  const [seasons, setSeasons] = useState<SeasonInfo[]>(seasonsProp || []);
  const [fullscreen, setFullscreen] = useState(false);
  // Set right before bumping `attempt`: resolve again skipping caches (or play switchTo), resuming at startAt.
  // wrongLength: the last file did not have the title's length. fresh: resolve (with avoid) even though
  // a saved or given source exists, without dropping the server's cached source.
  const retryRef = useRef<{ refresh: boolean; fresh?: boolean; startAt?: number; switchTo?: StreamSource; wrongLength?: boolean }>({ refresh: false });
  const autoRefreshesRef = useRef(0);
  // A recovery chain runs (restart, fallback, resolve without the failed source): one stable
  // overlay until frames run again, over the last frame when one was shown (frameRef)
  const [recovering, setRecovering] = useState(false);
  const frameRef = useRef<HTMLCanvasElement | null>(null);
  const [frameShown, setFrameShown] = useState(false);
  // Torrents of the wrong length for the current item; the server never picks them again
  const avoidRef = useRef<string[]>([]);
  const lengthRetriesRef = useRef(0);
  // Adaptive quality, for the whole session: the cap only ever goes down
  const capRef = useRef<QualityCap | undefined>(undefined);
  const triedCapsRef = useRef<QualityCap[]>([]);
  const downgradesRef = useRef(0);
  const downgradeCtrl = useRef<AbortController | null>(null);
  const upgradedRef = useRef(false);
  // An upgrade whose preparation failed is tried once more after UPGRADE_RETRY_MS; the reason
  // goes to the server with that poll
  const upgradeRetriesRef = useRef(0);
  const upgradeRetryAtRef = useRef(0);
  const upgradeFailRef = useRef('');
  // How the upgrade went; sent as why= with the release of the source it replaced (server log)
  const upgradeNoteRef = useRef('');
  // Live playback position and video quality class (0 until probed), written by PlayerCore
  const positionRef = useRef(0);
  const qualityRef = useRef(0);
  const [toast, setToast] = useState<MessageKey | ''>('');
  const [toastVars, setToastVars] = useState<Record<string, string>>({});
  // Source PlayerCore switched to without remounting (seamless upgrade / sidegrade); only valid
  // for the `resolved` it was made on, so any hard switch or new resolve drops it
  const [live, setLive] = useState<{ base: typeof resolved; source: StreamSource } | null>(null);
  const playingSource = live && live.base === resolved ? live.source : resolved.source;
  const resolvedRef = useRef(resolved);
  resolvedRef.current = resolved;
  // PlayerCore's seamless switch (null while no PlayerCore is mounted)
  const coreSwitchRef = useRef<CoreSwitch | null>(null);
  // Last source switch of any kind (seamless or not), for the proactive sidegrade's pace
  const lastSwitchRef = useRef(0);
  // Torrents left for being slow (or whose sidegrade never got ready): never switched to again
  const slowRef = useRef<Set<string>>(new Set());
  // A step down deferred while a sidegrade loads in the background
  const pendingStepDownRef = useRef<number | null>(null);

  const isTv = item.meta?.media_type === 'tv';
  const metaRef = useRef(item.meta);
  metaRef.current = item.meta;

  useEffect(() => {
    autoRefreshesRef.current = 0;
    setRecovering(false);
    setFrameShown(false);
    avoidRef.current = [];
    lengthRetriesRef.current = 0;
    pendingStepDownRef.current = null;
    // A new item starts where it was asked to; the last one's position no longer counts
    positionRef.current = 0;
  }, [itemKey]);

  // Find the source for the current item
  useEffect(() => {
    const retry = retryRef.current;
    retryRef.current = { refresh: false };
    // A retry or switch never starts over at 0 once the item played past REWIND_GUARD_S
    const startPos = guardedStart(retry.startAt ?? item.startAt ?? 0, positionRef.current);

    if (retry.switchTo) {
      setResolved({ source: retry.switchTo, startAt: startPos || 0, origin: 'fresh' });
      setPhase('ready');
      return;
    }
    // A given source without title info (legacy magnet mode) plays as it is; a saved one with title
    // info goes through the server's reuse check below
    if ((item.source || item.magnet) && !retry.refresh && !retry.fresh && (!item.meta || !item.source)) {
      setResolved({ source: item.source, startAt: startPos || 0, origin: 'given' });
      setPhase('ready');
      return;
    }
    const m = item.meta;
    if (!m) {
      setResolveError('player.nothingToPlay');
      setPhase('error');
      return;
    }
    const ctrl = new AbortController();
    let cancelled = false;
    const t0 = Date.now();
    setPhase('resolving');
    setResolveError('');
    setUnreleased(null);
    setResolveElapsed(0);
    setRefreshing(retry.refresh);
    setWrongLength(!!retry.wrongLength);
    const tick = setInterval(() => setResolveElapsed(Date.now() - t0), 250);
    (async () => {
      try {
        let start = startPos || 0;
        // Reuse the torrent saved with earlier progress instead of searching again
        let saved: StreamSource | undefined;
        let origin: SourceOrigin = 'given';
        if (!retry.refresh && !retry.fresh) {
          const pid = getActiveProfileId();
          if (item.source) {
            saved = item.source;
          } else if (pid) {
            const entries = await getTitleProgress(pid, m.media_type, m.tmdb_id);
            if (cancelled) return;
            const p = m.media_type === 'tv' ? findProgress(entries, m.season || 0, m.episode || 0) : findProgress(entries);
            if (p?.hash) {
              saved = { hash: p.hash, index: p.file_index, magnet: p.magnet || undefined };
              origin = 'saved';
              start = item.startAt ?? (isFinished(p) ? 0 : Math.max(0, p.position - 5));
            }
          }
        }
        if (saved) {
          // The same TorBox / swarm decision as resolve: TorBox when it has the release cached, the
          // torrent when its swarm keeps up at the resume position, else a fresh resolve without it
          const a = await reuseSource(paramsOf(m, capRef.current), saved, start, ctrl.signal);
          if (cancelled) return;
          if (!a || a.action === 'play') {
            const src = a?.source ? { ...sourceOf(a.source), starving: a.source.health?.keeps_up === false } : saved;
            setResolved({ source: src, startAt: start, origin });
            setPhase('ready');
            return;
          }
          const h = saved.hash.toLowerCase();
          console.info(`[nextflix] saved source ${h.slice(0, 8)} not reused (${a.reason || 'slow'}): resolving again`);
          if (!avoidRef.current.includes(h)) avoidRef.current = [...avoidRef.current, h];
        }
        const r = await resolveSource({
          type: m.media_type,
          title: m.title,
          tmdb_id: m.tmdb_id,
          year: m.year,
          season: m.season,
          episode: m.episode,
          runtime: m.runtime,
          maxq: capRef.current,
          cinema: m.cinema,
        }, ctrl.signal, { refresh: retry.refresh, avoid: avoidRef.current });
        if (cancelled) return;
        setResolved({
          source: { hash: r.hash, index: r.index, magnet: r.magnet, file_name: r.file_name, torrent_title: r.torrent_title, quality: r.quality, hdr: r.hdr, starving: r.health?.keeps_up === false, ratio: healthRatio(r.health), debrid: r.debrid, cinema: r.cinema },
          startAt: start,
          origin: r.cached ? 'cached' : 'fresh',
        });
        setPhase('ready');
      } catch (e: any) {
        if (cancelled || e?.name === 'AbortError') return;
        setRecovering(false);
        setFrameShown(false);
        if (e instanceof StreamApiError && e.status === 425) {
          setUnreleased(e.availability || { status: 'not_released', release_date: null });
          setPhase('error');
          return;
        }
        setResolveError(e?.status === 404 || !e?.message ? 'player.noSource' : e.message);
        setPhase('error');
      } finally {
        clearInterval(tick);
      }
    })();
    return () => {
      cancelled = true;
      clearInterval(tick);
      ctrl.abort();
    };
    // item is replaced together with itemKey
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [itemKey, attempt]);

  // Draws the active video's current frame into the canvas under the player (kept while it restarts)
  const freezeFrame = useCallback((v: HTMLVideoElement | null) => {
    const c = frameRef.current;
    if (!c || !v || v.readyState < HTMLMediaElement.HAVE_CURRENT_DATA || !v.videoWidth) return;
    const scale = Math.min(1, 1280 / v.videoWidth);
    c.width = Math.round(v.videoWidth * scale);
    c.height = Math.round(v.videoHeight * scale);
    try {
      c.getContext('2d')?.drawImage(v, 0, 0, c.width, c.height);
      setFrameShown(true);
    } catch {
      /* no frame to keep */
    }
  }, []);
  const onRecovering = useCallback(() => setRecovering(true), []);
  // Frames run again (or the chain ended in an error): the frozen frame and the recovery state go
  const onRecovered = useCallback(() => {
    setRecovering(false);
    setFrameShown(false);
  }, []);

  // Called by PlayerCore when the source fails (auto) or the user presses Try Again.
  // Returns false when nothing was retried, so PlayerCore shows its error.
  // A fresh source that failed even after its silent restart is resolved again once, without it.
  const retrySource = useCallback((position: number, auto: boolean) => {
    const reused = resolved.origin !== 'fresh';
    if (auto && (!item.meta || autoRefreshesRef.current >= MAX_AUTO_REFRESH)) return false;
    if (auto) {
      autoRefreshesRef.current++;
      setRecovering(true);
    }
    const failed = resolved.source?.hash?.toLowerCase();
    if (auto && !reused && failed && !avoidRef.current.includes(failed)) avoidRef.current = [...avoidRef.current, failed];
    // A source that failed before its first frame reports 0: it resumes where it was to start
    retryRef.current = { refresh: !!item.meta, startAt: position > 0 ? position : resolvedRef.current.startAt };
    setAttempt(a => a + 1);
    return true;
  }, [resolved.origin, resolved.source, item.meta]);

  // Called by PlayerCore when the probed file is clearly not the whole title (its length does
  // not fit the TMDB runtime): resolve again without it. False when it gave up (play it anyway).
  const replaceWrongLength = useCallback((hash: string) => {
    if (!item.meta || lengthRetriesRef.current >= MAX_LENGTH_RETRIES) return false;
    lengthRetriesRef.current++;
    avoidRef.current = [...avoidRef.current, hash.toLowerCase()];
    retryRef.current = { refresh: true, startAt: resolved.startAt, wrongLength: true };
    setAttempt(a => a + 1);
    return true;
  }, [item.meta, resolved.startAt]);

  // Torrents played in this session (see the release effect below)
  const playedRef = useRef<Set<string>>(new Set());
  const currentHashRef = useRef('');

  // Restart PlayerCore on another source at the current position (loading screen while it starts)
  const hardSwitch = useCallback((next: StreamSource) => {
    lastSwitchRef.current = Date.now();
    pendingStepDownRef.current = null;
    // Before the first frame of a resume the position is still the start the source opened at
    const at = positionRef.current > 0 ? positionRef.current : resolvedRef.current.startAt;
    retryRef.current = { refresh: false, startAt: at, switchTo: next };
    setAttempt(a => a + 1);
  }, []);

  // A source that was prepared for a seamless switch but never played
  const releaseUnused = useCallback((hash: string, why?: string) => {
    const h = hash.toLowerCase();
    if (h !== currentHashRef.current && !playedRef.current.has(h)) releaseTorrents([h], why);
  }, []);

  // PlayerCore took over a new source without remounting
  const onSwitched = useCallback((next: StreamSource) => {
    lastSwitchRef.current = Date.now();
    setLive({ base: resolvedRef.current, source: next });
  }, []);

  // Dev only: switch like an offer would (`__nfxDebugSwitch({ hash, index, debrid?, kind?, hard? })`):
  // seamless while the player plays, else (or with hard) the hard switch at the current position
  useEffect(() => {
    if (process.env.NODE_ENV === 'production') return;
    type Req = { hash: string; index: number; debrid?: boolean; kind?: SwitchKind; hard?: boolean };
    const w = window as unknown as { __nfxDebugSwitch?: (s: Req) => Promise<SwitchResult | 'hard'> };
    w.__nfxDebugSwitch = async s => {
      const next: StreamSource = { hash: s.hash, index: s.index, debrid: s.debrid };
      const res = s.hard ? 'unavailable' : await (coreSwitchRef.current?.prepare(next, s.kind || 'sidegrade') ?? 'unavailable');
      if (res !== 'unavailable') return res;
      hardSwitch(next);
      return 'hard';
    };
    return () => { delete w.__nfxDebugSwitch; };
  }, [hardSwitch]);

  // Called by PlayerCore when playback keeps stalling. Looks for a lower quality of the same
  // title in the background and switches to it at the current position when it keeps up.
  const stepDown = useCallback((quality: number) => {
    const m = item.meta;
    const cap = lowerCap(quality || releaseHeight(playingSource?.quality));
    if (!m || !cap || downgradeCtrl.current || downgradesRef.current >= MAX_DOWNGRADES) return;
    if (triedCapsRef.current.includes(cap) || (capRef.current && cap > capRef.current)) return;
    triedCapsRef.current.push(cap);
    const ctrl = new AbortController();
    downgradeCtrl.current = ctrl;
    const current = playingSource;
    resolveSource({ type: m.media_type, title: m.title, tmdb_id: m.tmdb_id, year: m.year, season: m.season, episode: m.episode, runtime: m.runtime, maxq: cap, cinema: m.cinema }, ctrl.signal, { avoid: avoidRef.current })
      .then(r => {
        if (ctrl.signal.aborted) return;
        capRef.current = cap;
        // A lower quality that does not keep up either is no improvement
        if (r.health && !r.health.keeps_up) return;
        if (current && r.hash.toLowerCase() === current.hash.toLowerCase() && r.index === current.index) return;
        downgradesRef.current++;
        hardSwitch(sourceOf(r));
        setToast('player.switchedHd');
      })
      .catch(() => {
        // Keep playing the current source; a cancelled search may run again later
        if (ctrl.signal.aborted) triedCapsRef.current = triedCapsRef.current.filter(c => c !== cap);
      })
      .finally(() => { if (downgradeCtrl.current === ctrl) downgradeCtrl.current = null; });
  }, [item.meta, playingSource, hardSwitch]);

  // Swap a starving source for a spare that keeps up now, at the same position. True when it switched.
  // proactive: asked while the buffer drains (playback still smooth), never next to another preparation
  const lastSidegradeRef = useRef(0);
  const sidegradeBusy = useRef(false);
  const trySidegrade = useCallback(async (quality: number, proactive = false): Promise<boolean> => {
    const m = metaRef.current;
    const src = playingSource;
    const now = Date.now();
    if (!m || !src || sidegradeBusy.current || now - lastSidegradeRef.current < SIDEGRADE_EVERY_MS) return false;
    if (proactive && (coreSwitchRef.current?.busy() || now - lastSwitchRef.current < SWITCH_EVERY_MS)) return false;
    sidegradeBusy.current = true;
    try {
      const up = await checkSidegrade(paramsOf(m, capRef.current), src.hash, quality || releaseHeight(src.quality), undefined,
        { state: proactive ? 'draining' : 'critical', avoid: Array.from(slowRef.current) });
      if (!up || up.hash.toLowerCase() === src.hash.toLowerCase() || metaRef.current !== m) return false;
      lastSidegradeRef.current = Date.now();
      const next = sourceOf(up);
      const nextHash = next.hash.toLowerCase();
      if (slowRef.current.has(nextHash)) {
        releaseUnused(nextHash);
        return false;
      }
      // While the current stream still plays, the spare loads in the background and takes over seamlessly
      const res = await (coreSwitchRef.current?.prepare(next, 'sidegrade') ?? 'unavailable');
      const deferred = pendingStepDownRef.current;
      pendingStepDownRef.current = null;
      if (res === 'switched') {
        slowRef.current.add(src.hash.toLowerCase());
        return true;
      }
      // A TorBox source that keeps up but could not be lined up seamlessly: switch with the loading
      // screen rather than stepping down a resolution
      if (res === 'abandoned' && next.debrid && up.health?.keeps_up !== false && metaRef.current === m &&
          coreSwitchRef.current?.failure() !== 'dropped by the player') {
        console.info(`[nextflix] sidegrade to ${nextHash.slice(0, 8)} (TorBox) switches with a restart: ${coreSwitchRef.current?.failure() || 'unknown'}`);
        pendingStepDownRef.current = null;
        slowRef.current.add(src.hash.toLowerCase());
        hardSwitch(next);
        setToast('player.switchedFaster');
        return true;
      }
      if (res !== 'unavailable' || metaRef.current !== m) {
        releaseUnused(nextHash, res === 'abandoned' ? `sidegrade dropped: ${coreSwitchRef.current?.failure() || 'unknown'}` : undefined);
        // Not ready in time (or broken): not offered again this session
        if (res === 'abandoned') slowRef.current.add(nextHash);
        // The stall that waited for this preparation steps down now
        if (deferred !== null && res !== 'busy' && metaRef.current === m) stepDown(deferred);
        return false;
      }
      // Stalled (or not started yet): the loading screen is up anyway
      slowRef.current.add(src.hash.toLowerCase());
      hardSwitch(next);
      setToast('player.switchedFaster');
      return true;
    } finally {
      sidegradeBusy.current = false;
    }
  }, [playingSource, hardSwitch, releaseUnused, stepDown]);

  // Stalling: a faster source of the same quality first, a lower quality otherwise. A sidegrade
  // that already loads in the background is given its chance first (it can take over the stall).
  const struggling = useCallback((quality: number) => {
    if (coreSwitchRef.current?.preparing()?.kind === 'sidegrade') {
      pendingStepDownRef.current = quality;
      return;
    }
    trySidegrade(quality).then(switched => { if (!switched) stepDown(quality); });
  }, [trySidegrade, stepDown]);

  // A torrent showed no frame SLOW_START_MS after it opened (a swarm far below the bitrate): a
  // sidegrade that keeps up, else resolve again without it at the same position (a TorBox or
  // faster release). False when out of tries (the usual restarts go on).
  const slowStart = useCallback((position: number) => {
    const m = metaRef.current;
    const src = playingSource;
    if (!m || !src || src.debrid || autoRefreshesRef.current >= MAX_AUTO_REFRESH) return false;
    const h = src.hash.toLowerCase();
    slowRef.current.add(h);
    trySidegrade(qualityRef.current).then(switched => {
      if (switched || metaRef.current !== m) return;
      autoRefreshesRef.current++;
      if (!avoidRef.current.includes(h)) avoidRef.current = [...avoidRef.current, h];
      retryRef.current = { refresh: false, fresh: true, startAt: position > 0 ? position : resolvedRef.current.startAt };
      setAttempt(a => a + 1);
    });
    return true;
  }, [playingSource, trySidegrade]);

  // Buffer health of the playing stream, once a second while it plays (PlayerCore)
  const proactiveRef = useRef({ next: 0, delay: PROACTIVE_POLL_MS });
  const onBufferHealth = useCallback((h: BufferVerdict) => {
    const core = coreSwitchRef.current;
    if (h.healthyMs >= RECOVERED_MS) {
      pendingStepDownRef.current = null;
      // Recovered: a sidegrade that is still loading is not needed any more
      const prep = core?.preparing();
      if (prep?.kind === 'sidegrade' && !prep.ready) core?.cancel();
    }
    const poll = proactiveRef.current;
    if (h.state === 'healthy') {
      poll.next = 0;
      poll.delay = PROACTIVE_POLL_MS;
      return;
    }
    const now = Date.now();
    if (now < poll.next) return;
    poll.next = now + poll.delay;
    poll.delay = Math.min(PROACTIVE_POLL_MAX_MS, poll.delay * 1.5);
    trySidegrade(qualityRef.current, true);
  }, [trySidegrade]);

  // A source the resolve already measured as starving: look for a sidegrade early in playback
  useEffect(() => {
    const src = playingSource;
    if (phase !== 'ready' || !src?.starving || !metaRef.current) return;
    const started = Date.now();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    const poll = async () => {
      if (stopped || Date.now() - started > SIDEGRADE_FOR_MS) return;
      // Also before the first frame: a switch then starts at the resume position (hardSwitch)
      if (await trySidegrade(qualityRef.current)) return;
      if (!stopped) timer = setTimeout(poll, SIDEGRADE_POLL_MS);
    };
    timer = setTimeout(poll, SIDEGRADE_FIRST_MS);
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [phase, playingSource, trySidegrade]);

  // A step down that is still searching belongs to the item it started for
  useEffect(() => () => { downgradeCtrl.current?.abort(); downgradeCtrl.current = null; }, [itemKey]);

  // Start low, switch up: while a source below 4K plays on an HEVC client, ask the server
  // whether the 4K it keeps warming in the background keeps up now. It loads in the background and
  // takes over at a frame boundary; only a stalled player switches with the loading screen.
  useEffect(() => {
    const src = playingSource;
    const steppedDown = () => downgradesRef.current > 0 || !!capRef.current || triedCapsRef.current.length > 0;
    if (phase !== 'ready' || !src || !metaRef.current || !playbackCaps().hevc) return;
    // After a step down (a slow swarm) only a TorBox source comes back up: it plays at line speed
    if (upgradedRef.current || releaseHeight(src.quality) >= 2160) return;
    const ctrl = new AbortController();
    const started = Date.now();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const secs = () => `${Math.round((Date.now() - started) / 1000)} s`;
    let waitedFor = '';
    const poll = async () => {
      const m = metaRef.current;
      // A retry after a failed preparation runs even past the polling window
      const retry = upgradeRetryAtRef.current > 0;
      if (!m || upgradedRef.current || (!retry && Date.now() - started > UPGRADE_FOR_MS)) return;
      const quality = qualityRef.current || releaseHeight(src.quality);
      if (quality >= 2160) return;
      // Wait until the probe knows the quality and playback has a position (and no sidegrade loads)
      const wait = quality <= 0 ? 'quality unknown' : positionRef.current <= 0 ? 'no position yet'
        : coreSwitchRef.current?.busy() ? 'another switch loads' : Date.now() < upgradeRetryAtRef.current ? 'retry pause' : '';
      if (wait && wait !== waitedFor) console.info(`[nextflix] upgrade poll waits (${secs()}): ${wait}`);
      waitedFor = wait;
      if (!wait) {
        const failed = upgradeFailRef.current;
        upgradeFailRef.current = '';
        const up = await checkUpgrade(
          { type: m.media_type, title: m.title, tmdb_id: m.tmdb_id, year: m.year, season: m.season, episode: m.episode, runtime: m.runtime, cinema: m.cinema },
          src.hash, quality, ctrl.signal, failed,
        );
        if (ctrl.signal.aborted) return;
        if (retry) upgradeRetryAtRef.current = 0;
        if (up && releaseHeight(up.quality) > quality && !upgradedRef.current && (!steppedDown() || up.debrid)) {
          const wasDown = steppedDown();
          upgradedRef.current = true;
          const next = sourceOf(up);
          const offered = secs();
          const res = await (coreSwitchRef.current?.prepare(next, 'upgrade') ?? 'unavailable');
          const stats = coreSwitchRef.current?.stats() || '';
          // Back up from a step down: later resolves are no longer capped
          if (wasDown && (res === 'switched' || res === 'unavailable')) {
            capRef.current = undefined;
            triedCapsRef.current = [];
          }
          if (res === 'switched') {
            upgradeNoteRef.current = `upgraded to ${next.hash.slice(0, 8)}${next.debrid ? ' (TorBox)' : ''} ${secs()} after the start (offered at ${offered}; ${stats})`;
            console.info(`[nextflix] ${upgradeNoteRef.current}`);
          }
          if (res === 'abandoned' || res === 'cancelled') {
            const why = `${res === 'cancelled' ? 'cancelled' : coreSwitchRef.current?.failure() || 'unknown'} (${stats})`;
            // One more try a little later (the server hears why on that poll); after that the
            // upgrade is not offered again this session
            if (upgradeRetriesRef.current < 1 && !ctrl.signal.aborted) {
              const pause = next.debrid ? UPGRADE_RETRY_DEBRID_MS : UPGRADE_RETRY_MS;
              upgradeRetriesRef.current++;
              upgradedRef.current = false;
              upgradeFailRef.current = why;
              upgradeRetryAtRef.current = Date.now() + pause;
              timer = setTimeout(poll, pause);
              return;
            }
            releaseUnused(next.hash, `upgrade dropped: ${why}`);
          }
          if (res === 'switched' || res === 'abandoned' || res === 'cancelled' || ctrl.signal.aborted) return;
          if (res === 'busy') {
            upgradedRef.current = false;
          } else {
            hardSwitch(next);
            setToast(up.quality === '2160p' ? 'player.switchedTo4k' : 'player.switchedHigher');
            return;
          }
        }
      }
      timer = setTimeout(poll, UPGRADE_POLL_MS);
    };
    timer = setTimeout(poll, UPGRADE_FIRST_MS);
    return () => {
      ctrl.abort();
      if (timer) clearTimeout(timer);
    };
  }, [phase, playingSource, itemKey, hardSwitch, releaseUnused]);

  // The playing file has no subtitle in the profile's language (PlayerCore reports it after its retries):
  // ask the server for a release that carries it inside, and offer it in the subtitle panel
  const [subMissing, setSubMissing] = useState<SubtitleState | null>(null);
  const [subAlt, setSubAlt] = useState<{ lang: string; source: StreamSource; forHash: string } | null>(null);
  const [subAltBusy, setSubAltBusy] = useState(false);
  // Set when the viewer switched for a subtitle: the next file's list picks that language
  const wantSubLangRef = useRef('');
  const subAltRef = useRef(subAlt);
  const subAltBusyRef = useRef(subAltBusy);
  subAltBusyRef.current = subAltBusy;
  subAltRef.current = subAlt;
  const onSubtitleState = useCallback((s: SubtitleState) => {
    if (s.arrived) {
      setToastVars({ lang: languageName(locale, s.lang) });
      setToast('player.subtitleFound');
    }
    setSubMissing(s.missing ? s : null);
  }, [locale]);
  useEffect(() => {
    const m = metaRef.current;
    const src = playingSource;
    const playing = src?.hash.toLowerCase();
    if (!subMissing || !m || !src || !playing || subMissing.hash !== playing) {
      if (subAltRef.current && !subAltBusyRef.current) setSubAlt(null); // found meanwhile, or another file plays
      return;
    }
    if (subAltRef.current?.forHash === playing) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let tries = 0;
    const ask = async () => {
      tries++;
      const quality = qualityRef.current || releaseHeight(src.quality);
      const r = await checkSubAlt(paramsOf(m, capRef.current), src.hash, quality, subMissing.lang);
      if (stopped) return;
      const h = r.source?.hash.toLowerCase();
      if (r.source && h && h !== playing && !slowRef.current.has(h)) {
        setSubAlt({ lang: subMissing.lang, source: sourceOf(r.source), forHash: playing });
        return;
      }
      // The server scans other releases' headers in the background: ask again when it says so
      if (r.retryAfter && tries < SUBALT_RETRY_MAX) timer = setTimeout(ask, r.retryAfter * 1000);
    };
    ask();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [subMissing, playingSource]);

  // The viewer chose the other release: it loads in the background and takes over (as a sidegrade does);
  // a stalled player restarts on it. Its subtitle in that language is picked once listed.
  const switchSubAlt = useCallback(async () => {
    const offer = subAltRef.current;
    if (!offer || subAltBusy) return;
    setSubAltBusy(true);
    wantSubLangRef.current = offer.lang;
    try {
      const res = await (coreSwitchRef.current?.prepare(offer.source, 'subtitles') ?? 'unavailable');
      if (res === 'switched') {
        setSubAlt(null);
        return;
      }
      if (res === 'unavailable') {
        setSubAlt(null);
        hardSwitch(offer.source);
        setToastVars({ lang: languageName(locale, offer.lang) });
        setToast('player.switchedSubtitles');
        return;
      }
      wantSubLangRef.current = '';
      if (res !== 'busy') {
        releaseUnused(offer.source.hash);
        setSubAlt(null);
      }
    } finally {
      setSubAltBusy(false);
    }
  }, [subAltBusy, hardSwitch, releaseUnused, locale]);
  const playingHash = playingSource?.hash.toLowerCase();
  const subAltOffer = subAlt && subAlt.forHash === playingHash
    ? { lang: subAlt.lang, busy: subAltBusy, onSwitch: switchSubAlt }
    : null;

  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(''), TOAST_MS);
    return () => clearTimeout(timer);
  }, [toast]);

  // TV: episode counts for "Next Episode"
  useEffect(() => {
    if (!isTv || seasons.length > 0 || !item.meta) return;
    let cancelled = false;
    getTvSeasons(item.meta.tmdb_id).then(s => { if (!cancelled) setSeasons(s); });
    return () => { cancelled = true; };
  }, [isTv, item.meta, seasons.length]);

  // Resumes (Continue Watching) carry no original language; the default audio track needs it
  useEffect(() => {
    const m = item.meta;
    if (!m || m.original_language) return;
    let cancelled = false;
    getOriginalLanguage(m.tmdb_id, m.media_type).then(lang => {
      if (cancelled || !lang) return;
      setItem(prev => (prev.meta === m ? { ...prev, meta: { ...m, original_language: lang } } : prev));
    });
    return () => { cancelled = true; };
  }, [item.meta]);

  // TV: fill in the episode title (and runtime, for later resolves) when we only know the numbers
  useEffect(() => {
    const m = item.meta;
    if (!m || m.media_type !== 'tv' || (m.episode_title && m.runtime) || !m.season || !m.episode) return;
    const season = seasons.find(s => s.season_number === m.season);
    if (!season) return;
    let cancelled = false;
    getSeasonEpisodes(m.tmdb_id, season).then(eps => {
      const ep = eps.find(e => e.episode_number === m.episode);
      if (cancelled || !ep) return;
      const placeholder = !ep.still_path && ep.name === `Episode ${ep.episode_number}`;
      const episode_title = m.episode_title || (placeholder ? undefined : ep.name);
      const runtime = m.runtime || ep.runtime || undefined;
      if (episode_title === m.episode_title && runtime === m.runtime) return;
      setItem(prev => (prev.meta === m ? { ...prev, meta: { ...m, episode_title, runtime } } : prev));
    });
    return () => { cancelled = true; };
  }, [item.meta, seasons]);

  const next = useMemo(() => {
    const m = item.meta;
    return m?.media_type === 'tv' && m.season && m.episode ? nextEpisodeOf(seasons, m.season, m.episode) : null;
  }, [item.meta, seasons]);

  // The title on screen counts as playing, so nothing else releases it (see holdPlaying)
  const playHoldRef = useRef<(() => void) | null>(null);
  useEffect(() => {
    const m = item.meta;
    if (!m) return;
    const unhold = holdPlaying(paramsOf(m));
    playHoldRef.current = unhold;
    return unhold;
  }, [item.meta]);

  // Next episode warmed near the end of this one (released when the player closes first)
  const nextWarmRef = useRef<ResolveParams | null>(null);

  const playNext = useCallback(() => {
    if (!next || !item.meta) return;
    // The episode that ended is done; the next one was warmed for this moment
    playHoldRef.current?.();
    releaseTitle(paramsOf(item.meta));
    nextWarmRef.current = null;
    setItem({ meta: { ...item.meta, season: next.season, episode: next.episode, episode_title: undefined, runtime: undefined } });
    setItemKey(k => k + 1);
  }, [next, item.meta]);

  // Resolve and warm the next episode once 10 minutes are left (85 % of short episodes), once per episode
  const prefetchNext = useCallback((position: number, duration: number) => {
    const m = item.meta;
    if (!m || m.media_type !== 'tv' || !next) return;
    const params: ResolveParams = { type: 'tv', title: m.title, tmdb_id: m.tmdb_id, season: next.season, episode: next.episode, maxq: capRef.current };
    if (nextWarmRef.current && JSON.stringify(nextWarmRef.current) === JSON.stringify(params)) return;
    nextWarmRef.current = params;
    prefetchResolve(params, { next: { position, duration } });
  }, [item.meta, next]);

  // Torrents played in this session. A replaced source (upgrade, sidegrade, step down, next
  // episode from another torrent) is dropped at once; closing the player drops the rest,
  // releases the title and cancels the next episode's warm-up.
  useEffect(() => {
    const h = playingSource?.hash?.toLowerCase();
    if (phase !== 'ready' || !h) return;
    const prev = currentHashRef.current;
    if (prev && prev !== h) {
      releaseTorrents([prev], upgradeNoteRef.current || undefined);
      upgradeNoteRef.current = '';
      playedRef.current.delete(prev);
    }
    currentHashRef.current = h;
    playedRef.current.add(h);
  }, [phase, playingSource]);

  useEffect(() => {
    const release = () => {
      const m = metaRef.current;
      // Closing: this player no longer holds the title, so its own release goes through
      playHoldRef.current?.();
      if (m) releaseTitle(paramsOf(m));
      if (nextWarmRef.current) releaseTitle(nextWarmRef.current);
      nextWarmRef.current = null;
      releaseTorrents(Array.from(playedRef.current));
    };
    window.addEventListener('pagehide', release);
    return () => {
      window.removeEventListener('pagehide', release);
      release();
    };
  }, []);

  // Phones: fullscreen on open, landscape lock, no page bounce, edge-to-edge (mobile.ts)
  const touchUi = useTouchUi();
  const mobileFs = useMobileFullscreen(containerRef, touchUi);
  const mobileFsToggle = mobileFs.toggle;
  useViewportFitCover(touchUi);
  useNoBounce(containerRef, touchUi);

  const toggleFullscreen = useCallback(() => {
    if (touchUi) {
      mobileFsToggle();
      return;
    }
    if (!document.fullscreenElement) containerRef.current?.requestFullscreen().catch(() => {});
    else document.exitFullscreen().catch(() => {});
  }, [touchUi, mobileFsToggle]);

  useEffect(() => {
    const onFs = () => setFullscreen(!!(document.fullscreenElement ?? (document as Document & { webkitFullscreenElement?: Element | null }).webkitFullscreenElement));
    document.addEventListener('fullscreenchange', onFs);
    document.addEventListener('webkitfullscreenchange', onFs);
    return () => {
      document.removeEventListener('fullscreenchange', onFs);
      document.removeEventListener('webkitfullscreenchange', onFs);
    };
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.fullscreenElement) onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const displayTitle = item.meta?.title || title || '';
  const subtitle = subtitleOf(item.meta, locale);
  // A cached resolve answers almost instantly, so wait a moment before showing the overlay
  const showResolving = phase === 'resolving' && resolveElapsed >= 300 && !recovering;
  const errorText = resolveError === 'player.noSource' || resolveError === 'player.nothingToPlay'
    ? t(resolveError === 'player.noSource' ? 'player.noSourceHint' : 'player.nothingToPlay')
    : resolveError;
  const releaseDate = unreleased?.status === 'in_theaters'
    ? unreleased.digital_date
    : unreleased?.release_date || unreleased?.digital_date;
  const releaseLine = unreleased
    ? unreleased.status === 'in_theaters'
      ? (releaseDate ? t('availability.inTheatersUntil', { date: f.date(releaseDate) }) : t('availability.inTheaters'))
      : (releaseDate ? t('availability.comingOn', { date: f.date(releaseDate) }) : t('home.comingSoon'))
    : '';

  // Portal to body so transformed ancestors can't trap the fixed overlay
  return createPortal(
    <div ref={containerRef} className={`fixed inset-0 z-[100] bg-black flex items-center justify-center select-none ${touchUi ? mobileCss.touchRoot : ''}`}>
      {/* The last frame stays (dimmed) under an empty video while a stream restarts or the source changes */}
      <canvas
        ref={frameRef}
        className={`absolute inset-0 w-full h-full object-contain pointer-events-none transition-opacity duration-150 ${frameShown ? 'opacity-50' : 'opacity-0'}`}
      />
      {phase === 'ready' && (
        <PlayerCore
          key={resolved.n}
          magnet={item.magnet}
          source={resolved.source}
          startAt={resolved.startAt}
          meta={item.meta}
          title={displayTitle}
          subtitle={subtitle}
          nextLabel={next ? episodeLabel(next.season, next.episode, locale) : ''}
          onNext={next ? playNext : undefined}
          onPrefetchNext={isTv ? prefetchNext : undefined}
          onSourceFailed={retrySource}
          recovering={recovering}
          frameShown={frameShown}
          freezeFrame={freezeFrame}
          onRecovering={onRecovering}
          onRecovered={onRecovered}
          onStruggling={item.meta ? struggling : undefined}
          onSlowStart={item.meta ? slowStart : undefined}
          onBufferHealth={item.meta ? onBufferHealth : undefined}
          onWrongLength={item.meta ? replaceWrongLength : undefined}
          switchRef={coreSwitchRef}
          onSwitched={onSwitched}
          positionRef={positionRef}
          qualityRef={qualityRef}
          onSubtitleState={item.meta ? onSubtitleState : undefined}
          subAlt={subAltOffer}
          wantSubLangRef={wantSubLangRef}
          onClose={onClose}
          fullscreen={fullscreen}
          toggleFullscreen={toggleFullscreen}
          touchUi={touchUi}
          canFullscreen={!touchUi || mobileFs.supported}
          onRotate={touchUi && mobileFs.canRotate ? mobileFs.rotate : undefined}
        />
      )}

      {!!toast && phase === 'ready' && (
        <div className="absolute top-20 left-1/2 -translate-x-1/2 z-20 px-4 py-2 rounded bg-black/80 border border-white/20 text-white text-sm shadow-lg pointer-events-none" role="status">
          {t(toast, toastVars)}
        </div>
      )}

      {(showResolving || phase === 'error') && (
        <>
          {!!item.meta?.backdrop_path && (
            <img
              src={`/tmdb-img/t/p/w1280${item.meta.backdrop_path}`}
              alt=""
              className="absolute inset-0 w-full h-full object-cover opacity-25"
            />
          )}
          <div className="absolute inset-0 bg-gradient-to-t from-black via-black/60 to-black/40" />
          <div className="relative z-10 flex flex-col items-center text-center px-6 max-w-lg">
            <p className="text-white text-2xl md:text-3xl font-bold mb-1">{displayTitle}</p>
            {!!subtitle && <p className="text-[#b3b3b3] text-sm md:text-base mb-6">{subtitle}</p>}
            {!subtitle && <div className="mb-6" />}
            {phase === 'resolving' ? (
              <>
                <Loader2 className="w-12 h-12 text-[#E50914] animate-spin mb-4" />
                <p className="text-white text-base">{t(resolveStageKey(resolveElapsed, refreshing, wrongLength))}</p>
                <p className="text-[#b3b3b3] text-sm mt-1 h-5">
                  {resolveElapsed >= 30000 ? t('player.stillSearching') : ''}
                </p>
              </>
            ) : unreleased ? (
              // Not out yet: a friendly note with the date, nothing to retry
              <>
                <CalendarClock className="w-12 h-12 text-white/80 mb-4" />
                <p className="text-white text-lg font-semibold">
                  {t(isTv ? 'availability.episodeNotReleased' : 'availability.titleNotReleased')}
                </p>
                {!!releaseLine && <p className="text-[#b3b3b3] text-sm mt-1">{releaseLine}</p>}
                <button onClick={onClose} className="mt-6 px-6 py-2.5 bg-white text-black font-bold rounded hover:bg-white/80 transition">
                  {t('common.close')}
                </button>
              </>
            ) : (
              <>
                <AlertCircle className="w-12 h-12 text-[#E50914] mb-4" />
                <p className="text-white text-lg font-semibold">{t(isTv ? 'player.couldNotStartEpisode' : 'player.couldNotStartTitle')}</p>
                <p className="text-[#b3b3b3] text-sm mt-1 mb-6">{errorText}</p>
                <div className="flex gap-3">
                  <button
                    onClick={() => {
                      retryRef.current = { refresh: true };
                      setAttempt(a => a + 1);
                    }}
                    className="flex items-center gap-2 px-6 py-2.5 bg-white text-black font-bold rounded hover:bg-white/80 transition"
                  >
                    <RotateCw className="w-4 h-4" /> {t('player.retry')}
                  </button>
                  <button onClick={onClose} className="px-6 py-2.5 bg-[#6d6d6eb3] hover:bg-[#6d6d6e66] text-white font-bold rounded transition">
                    {t('common.close')}
                  </button>
                </div>
              </>
            )}
          </div>
        </>
      )}

      {phase === 'resolving' && recovering && (
        <div className="absolute inset-0 flex flex-col items-center justify-center pointer-events-none">
          <Loader2 className={`${frameShown ? 'w-8 h-8' : 'w-12 h-12'} text-red-600 animate-spin mb-3`} />
          <p className="text-white/70 text-sm">{t('player.switchingSource')}</p>
        </div>
      )}

      {phase !== 'ready' && (
        <BackButton label={t('player.back')} onClick={onClose} className={`absolute z-10 ${touchUi ? mobileCss.safeTopLeft : 'top-4 left-4 md:top-6 md:left-6'}`} />
      )}
    </div>,
    document.body
  );
}

interface PlayerCoreProps {
  magnet?: string;
  source?: StreamSource;
  startAt: number;
  meta?: StreamMeta;
  title: string;
  subtitle: string;
  nextLabel: string;
  onNext?: () => void;
  /** Called while the next episode's warm window is open (see nextWarmDue; the parent dedupes) */
  onPrefetchNext?: (position: number, duration: number) => void;
  /** The source failed; returns true when the parent retries with another source */
  onSourceFailed: (position: number, auto: boolean) => boolean;
  /** A recovery chain runs (the parent shows one stable overlay until onRecovered) */
  recovering?: boolean;
  /** The parent shows the last frame under the (empty) video */
  frameShown?: boolean;
  /** Keep this video's current frame on screen while its stream restarts */
  freezeFrame?: (v: HTMLVideoElement | null) => void;
  onRecovering?: () => void;
  /** Frames run again, or the chain ended in an error */
  onRecovered?: () => void;
  /** Playback keeps stalling; quality is the source's resolution class (2160/1080/720/480, 0 when unknown) */
  onStruggling?: (quality: number) => void;
  /** No frame SLOW_START_MS after a torrent source opened; returns false when the parent has nothing else to try */
  onSlowStart?: (position: number) => boolean;
  /** The probe says this file is not the whole title; returns true when the parent looks for another */
  onWrongLength?: (hash: string) => boolean;
  /** Receives the live playback position */
  positionRef?: React.MutableRefObject<number>;
  /** Receives the source's resolution class once probed */
  qualityRef?: React.MutableRefObject<number>;
  /** Buffer health of the playing stream, about once a second while it plays */
  onBufferHealth?: (h: BufferVerdict) => void;
  /** Receives the seamless switch: load a source in the background and take over without a loading screen */
  switchRef?: React.MutableRefObject<CoreSwitch | null>;
  /** A seamless switch finished; the old torrent is no longer read */
  onSwitched?: (next: StreamSource, kind: SwitchKind) => void;
  /** Whether the playing file has a subtitle in the profile's language (after the list retries) */
  onSubtitleState?: (s: SubtitleState) => void;
  /** Another release carries that subtitle: a row in the subtitle panel */
  subAlt?: { lang: string; busy: boolean; onSwitch: () => void } | null;
  /** The viewer switched for this subtitle language: the next list picks it */
  wantSubLangRef?: React.MutableRefObject<string>;
  onClose: () => void;
  fullscreen: boolean;
  toggleFullscreen: () => void;
  /** Phone / tablet layout: a tap shows the controls, double-tap seeks, bottom sheet panel */
  touchUi?: boolean;
  /** The fullscreen button works here (element fullscreen is missing on iPhone) */
  canFullscreen?: boolean;
  /** Portrait <-> landscape, where the browser can lock the orientation */
  onRotate?: () => void;
}

// Double-tap seek (touch): a second tap within DOUBLE_TAP_MS on the same side seeks; more taps
// add 10 s each and the seek runs SEEK_COMMIT_MS after the last one (one restart, not one per tap)
const DOUBLE_TAP_MS = 260;
const SEEK_COMMIT_MS = 650;

// Plays a torrent while it downloads. The stream is a live fMP4 requested at `stream.t`
// that really starts at `startPoint.t`, so the shown time adds that offset. Seeks inside
// what is already buffered happen natively; anything else restarts the stream.
function PlayerCore({
  magnet, source, startAt, meta, title, subtitle, nextLabel, onNext, onPrefetchNext, onSourceFailed, recovering = false, frameShown = false,
  freezeFrame, onRecovering, onRecovered, onStruggling, onSlowStart, onWrongLength,
  onBufferHealth, positionRef, qualityRef, switchRef, onSwitched, onSubtitleState, subAlt, wantSubLangRef, onClose, fullscreen,
  toggleFullscreen, touchUi = false, canFullscreen = true, onRotate,
}: PlayerCoreProps) {
  const apiUrl = getApiUrl();
  // Two <video> slots: the active one is on screen, the other loads a seamless switch muted and
  // invisible. videoRef always points to the active element.
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const slotEls = useRef<(HTMLVideoElement | null)[]>([null, null]);
  const allEls = useRef<Set<HTMLVideoElement>>(new Set());
  const activeSlotRef = useRef(0);
  const [activeSlot, setActiveSlot] = useState(0);
  // The previous slot stays visible under the new one while it fades in
  const [fadingSlot, setFadingSlot] = useState<number | null>(null);
  const slotRefs = useMemo(() => [0, 1].map(i => (el: HTMLVideoElement | null) => {
    slotEls.current[i] = el;
    if (el) allEls.current.add(el);
    if (activeSlotRef.current === i) videoRef.current = el;
  }), []);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const startRef = useRef(startAt);
  const t = useT();
  const locale = useLocale();
  // Effects with fixed deps read the current translator through this ref
  const tRef = useRef(t);
  tRef.current = t;
  const localeRef = useRef(locale);
  localeRef.current = locale;

  const [hash, setHash] = useState('');
  const [files, setFiles] = useState<StreamFile[]>([]);
  const [file, setFile] = useState<StreamFile | null>(null);
  const [duration, setDuration] = useState(0);
  const [audioTracks, setAudioTracks] = useState<AudioTrack[]>([]);
  const [audio, setAudio] = useState(0);
  // Read by restartAt: /seekpoint depends on whether this track is copied
  const audioRef = useRef(audio);
  audioRef.current = audio;
  const [videoInfo, setVideoInfo] = useState<VideoInfo | null>(null);
  // Current stream: requested at t seconds; n forces a restart at the same t
  const [stream, setStream] = useState<{ t: number; n: number } | null>(null);
  // Where the current stream really starts. Copied video starts on the keyframe at or before
  // stream.t; until /seekpoint says which one, exact is false and t is the requested time.
  const [startPoint, setStartPoint] = useState<{ t: number; exact: boolean }>({ t: 0, exact: true });
  const [current, setCurrent] = useState(0);
  const [seekPreview, setSeekPreview] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);
  const [buffering, setBuffering] = useState(true);
  const [status, setStatus] = useState<MessageKey>('player.connecting');
  const [error, setError] = useState('');
  const [muted, setMuted] = useState(false);
  const [volume, setVolume] = useState(1);
  const [showControls, setShowControls] = useState(true);
  const [ended, setEnded] = useState(false);
  const [countdown, setCountdown] = useState<number | null>(null);
  // 'credits': the countdown runs in a corner while the end credits play; 'end': the video ended
  const [countdownAt, setCountdownAt] = useState<'credits' | 'end'>('end');
  // "Watch credits" was picked: no countdown again until the credits are left and reached again
  const creditsDismissedRef = useRef(false);
  // The source on screen: `source` until a seamless switch replaces it
  const [shownSource, setShownSource] = useState(source);
  // Short "4K" note after a seamless upgrade
  const [switchNote, setSwitchNote] = useState('');
  // Read by restartAt and the switch, which can change them before React re-renders
  const hashRef = useRef(hash);
  hashRef.current = hash;
  const fileRef = useRef(file);
  fileRef.current = file;
  const audioTracksRef = useRef(audioTracks);
  audioTracksRef.current = audioTracks;
  const bufferingRef = useRef(buffering);
  bufferingRef.current = buffering;
  // Adaptive start buffer (startBuffer.ts): the active element waits paused until enough is
  // buffered; `since` is when the wait counts from (null until the first frames are playable)
  const holdRef = useRef<{ kind: HoldKind; since: number | null; ahead: number } | null>(null);
  const [holding, setHolding] = useState(false);
  // The file whose first stream got its start hold (only the first stream of a source holds;
  // a TorBox source never does: it delivers at line speed)
  const startHeldRef = useRef('');
  const shownSourceRef = useRef(shownSource);
  shownSourceRef.current = shownSource;
  // Pause handling: when the viewer paused (0 = playing), a restart that keeps the element
  // paused, and the resume check after a long pause
  const viewerPausedAtRef = useRef(0);
  const startPausedRef = useRef(false);
  const resumeCheckRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Frames must run this far past the 'playing' position before a restart or recovery counts as done
  const advanceFromRef = useRef<number | null>(null);
  const recoveringRef = useRef(recovering);
  recoveringRef.current = recovering;
  const onRecoveredRef = useRef(onRecovered);
  onRecoveredRef.current = onRecovered;
  const freezeFrameRef = useRef(freezeFrame);
  freezeFrameRef.current = freezeFrame;
  const onSourceFailedRef = useRef(onSourceFailed);
  onSourceFailedRef.current = onSourceFailed;
  // When the viewer last resumed after a long pause (the stall watchdog is quicker for a while)
  const resumedAtRef = useRef(0);
  // When the active element last received data ('progress')
  const lastDataRef = useRef(Date.now());
  const onRecoveringRef = useRef(onRecovering);
  onRecoveringRef.current = onRecovering;
  const frameShownRef = useRef(frameShown);
  frameShownRef.current = frameShown;
  const blockedRef = useRef(false);
  blockedRef.current = !!error || ended;
  // The running seamless switch preparation, if any
  const prepRef = useRef<SwitchPrep | null>(null);
  // Set by a switch so the probe and play effects don't restart what the hidden element already plays
  const adoptRef = useRef<{ hash: string; fileId: number; url: string } | null>(null);
  // Audio & Subtitles panel, subtitle list (null while loading), the chosen file, its cues and timing
  const [panelOpen, setPanelOpen] = useState(false);
  const panelOpenRef = useRef(false);
  panelOpenRef.current = panelOpen;
  const panelButtonRef = useRef<HTMLButtonElement>(null);
  // A click outside closes the panel without also pausing the video
  const panelClosedAt = useRef(0);
  const [subs, setSubs] = useState<SubtitleItem[] | null>(null);
  const [subId, setSubId] = useState<string | null>(null);
  const [cues, setCues] = useState<Cue[]>([]);
  const [subDelay, setSubDelay] = useState(0);
  // Subtitle size from Settings (localStorage, read after mount)
  const [subScale, setSubScale] = useState(1);
  useEffect(() => {
    const size = readPlayerPrefs(getActiveProfileId()).subSize;
    setSubScale(size && SUBTITLE_SIZE_SCALE[size] ? SUBTITLE_SIZE_SCALE[size] : 1);
  }, []);
  const subDecided = useRef(false);
  // The viewer picked a subtitle (or off) themselves; the list the default was picked for
  const subUserPicked = useRef(false);
  const subPickedFor = useRef<SubtitleItem[] | null>(null);
  // Subtitles whose file could not be loaded (quota, errors): greyed in the panel, never tried again
  const [failedSubs, setFailedSubs] = useState<ReadonlySet<string>>(() => new Set());
  const failedSubsRef = useRef(failedSubs);
  const subsRef = useRef(subs);
  subsRef.current = subs;
  // Short notice above the controls (OpenSubtitles' daily limit), once per player
  const [subNotice, setSubNotice] = useState<MessageKey | ''>('');
  const quotaNoticeRef = useRef(false);
  useEffect(() => {
    if (!subNotice) return;
    const timer = setTimeout(() => setSubNotice(''), 4000);
    return () => clearTimeout(timer);
  }, [subNotice]);
  const subIdRef = useRef(subId);
  subIdRef.current = subId;
  const onSubtitleStateRef = useRef(onSubtitleState);
  onSubtitleStateRef.current = onSubtitleState;

  // 1) Resolve the torrent's video files
  useEffect(() => {
    let cancelled = false;
    (async () => {
      if (source) {
        // Make sure TorrServer has the torrent (it may have been dropped since it was saved).
        // A TorBox source plays from TorBox, so TorrServer is left out.
        if (source.magnet && !source.debrid) {
          try {
            await fetch(`${apiUrl}/api/torrent-stream/files?magnet=${encodeURIComponent(source.magnet)}`);
          } catch {
            /* probe below reports real failures */
          }
        }
        if (cancelled) return;
        setHash(source.hash.toLowerCase());
        setFile({ id: source.index, name: source.file_name || '', path: '', size: 0 });
        return;
      }
      if (!magnet) return;
      try {
        const res = await fetch(`${apiUrl}/api/torrent-stream/files?magnet=${encodeURIComponent(magnet)}`);
        const data = await res.json();
        if (cancelled) return;
        if (!res.ok) throw new Error(data.error || tRef.current('player.couldNotOpen'));
        if (!data.files?.length) throw new Error(tRef.current('player.noVideoFile'));
        setHash(data.hash);
        setFiles(data.files);
        // A single file (or one much larger than the rest) is the movie itself
        const sorted = [...data.files].sort((a: StreamFile, b: StreamFile) => b.size - a.size);
        if (data.files.length === 1 || sorted[0].size > sorted[1].size * 4) setFile(sorted[0]);
      } catch (e: any) {
        if (!cancelled) setError(e.message || tRef.current('player.couldNotOpen'));
      }
    })();
    return () => { cancelled = true; };
  }, [apiUrl, magnet, source]);

  const offset = startPoint.t;
  const offsetRef = useRef(offset);
  offsetRef.current = offset;
  const exactRef = useRef(startPoint.exact);
  exactRef.current = startPoint.exact;
  // Before the seekpoint answers this may run up to one keyframe interval ahead
  const position = offset + current;
  // The session's position (the parent's ref outlives this PlayerCore): only a running stream
  // writes it, so a PlayerCore that is still opening its file never reports 0
  const ownPosRef = useRef(startAt);
  const knownPosRef = positionRef ?? ownPosRef;
  if (stream) knownPosRef.current = position;
  const filePosRef = useRef(0);
  filePosRef.current = position;

  // Latest values for timers, callbacks and unmount. saveAt is what progress stores: the
  // requested time while the real start is unknown, so a guessed offset is never saved.
  const stateRef = useRef({ position: 0, saveAt: 0, duration: 0, hash: '', index: 0 });
  stateRef.current = { position, saveAt: startPoint.exact ? position : offset, duration, hash, index: file?.id ?? 0 };
  const videoInfoRef = useRef(videoInfo);
  videoInfoRef.current = videoInfo;
  const quality = videoInfo ? videoQuality(videoInfo.width, videoInfo.height) : 0;
  if (qualityRef) qualityRef.current = quality;
  const onWrongLengthRef = useRef(onWrongLength);
  onWrongLengthRef.current = onWrongLength;
  const metaRef = useRef(meta);
  metaRef.current = meta;
  const magnetRef = useRef(source?.magnet || magnet || '');
  const mutedRef = useRef(muted);
  mutedRef.current = muted;
  const volumeRef = useRef(volume);
  volumeRef.current = volume;

  // Stall watchdog: a stream that stops delivering data is restarted at the current position
  const stallTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const seekSeq = useRef(0);
  const seekCtrl = useRef<AbortController | null>(null);

  // Stall tracking for adaptive quality (only after this source has played once)
  const everPlayed = useRef(false);
  const stall = useRef<{ start: number; seek: boolean } | null>(null);
  const stallTimes = useRef<number[]>([]);
  const longStallTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const onStrugglingRef = useRef(onStruggling);
  onStrugglingRef.current = onStruggling;
  // Buffer health of the active stream (reset whenever it restarts or is replaced)
  const healthMon = useRef(new BufferMonitor());
  // The active element fired 'suspend' (the browser stopped downloading on its own) since the last sample
  const suspendSeenRef = useRef(false);
  const onBufferHealthRef = useRef(onBufferHealth);
  onBufferHealthRef.current = onBufferHealth;

  const struggling = useCallback(() => {
    stallTimes.current = [];
    const v = videoInfoRef.current;
    onStrugglingRef.current?.(v ? videoQuality(v.width, v.height) : 0);
  }, []);

  // seek: a restart the viewer asked for. Only its length counts, since seeking twice is normal.
  const beginStall = useCallback((seek: boolean) => {
    if (!everPlayed.current || stall.current) return;
    stall.current = { start: Date.now(), seek };
    if (longStallTimer.current) clearTimeout(longStallTimer.current);
    longStallTimer.current = setTimeout(() => {
      if (stall.current && (!videoRef.current?.paused || holdRef.current)) struggling();
    }, LONG_STALL_MS);
  }, [struggling]);

  const endStall = useCallback(() => {
    if (longStallTimer.current) clearTimeout(longStallTimer.current);
    longStallTimer.current = null;
    const s = stall.current;
    stall.current = null;
    if (!s || s.seek) return;
    const now = Date.now();
    if (now - s.start < MIN_STALL_MS) return;
    stallTimes.current = [...stallTimes.current.filter(t => now - t < STALL_WINDOW_MS), now];
    if (stallTimes.current.length >= 2) struggling();
  }, [struggling]);

  useEffect(() => () => { if (longStallTimer.current) clearTimeout(longStallTimer.current); }, []);

  // A torrent that shows no frame SLOW_START_MS after it opened hands over at once (sidegrade or a
  // fresh resolve) instead of restarting the same swarm (30 s, 60 s, 90 s: minutes of a spinner).
  // TorBox sources start at line speed and keep the usual restarts.
  const onSlowStartRef = useRef(onSlowStart);
  onSlowStartRef.current = onSlowStart;
  useEffect(() => {
    if (!source || source.debrid || !onSlowStartRef.current) return;
    const timer = setTimeout(() => {
      if (everPlayed.current || viewerPausedAtRef.current > 0) return;
      console.info(`[nextflix] restart: no frame ${SLOW_START_MS / 1000} s after the start: trying another source`);
      freezeFrameRef.current?.(videoRef.current);
      onRecoveringRef.current?.();
      if (!onSlowStartRef.current?.(stateRef.current.position)) onRecoveredRef.current?.();
    }, SLOW_START_MS);
    return () => clearTimeout(timer);
    // PlayerCore is keyed by its source: counted once from its mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Restart the stream at `target` right away. Copied video can only start on a keyframe,
  // so /seekpoint runs in parallel and its answer becomes the offset as soon as it arrives.
  const restartAt = useCallback((requested: number, userAsked = false) => {
    // Only the viewer sends the stream back to the start (rewindGuard.ts)
    const target = guardedStart(requested, knownPosRef.current, userAsked);
    const hash = hashRef.current;
    const file = fileRef.current;
    if (!hash || !file) return;
    prepRef.current?.onSeek(target);
    healthMon.current.reset();
    const seq = ++seekSeq.current;
    seekCtrl.current?.abort();
    seekCtrl.current = null;
    beginStall(true);
    setBuffering(true);
    setEnded(false);
    if (stallTimer.current) clearTimeout(stallTimer.current);
    // Drop the old stream now so its download doesn't compete with the new position; its last
    // frame stays on screen (dimmed) until the new stream's frames run
    const v = videoRef.current;
    freezeFrameRef.current?.(v);
    if (v && v.getAttribute('src')) {
      v.removeAttribute('src');
      v.load();
    }
    setCurrent(0);
    offsetRef.current = target;
    exactRef.current = target <= 0;
    setStartPoint({ t: target, exact: target <= 0 });
    setStream(s => ({ t: target, n: (s?.n ?? 0) + 1 }));
    if (target <= 0) return;

    const ctrl = new AbortController();
    seekCtrl.current = ctrl;
    const timer = setTimeout(() => ctrl.abort(), 15000);
    (async () => {
      let t = target;
      try {
        const res = await fetch(`${apiUrl}/api/torrent-stream/seekpoint?hash=${hash}&index=${file.id}&t=${target.toFixed(3)}&audio=${audioRef.current}${capsQuery()}`, { signal: ctrl.signal });
        const data = await res.json();
        if (res.ok && typeof data.t === 'number' && data.t >= 0 && data.t <= target + 1) t = data.t;
      } catch {
        /* the stream still starts near target, just without the exact offset */
      } finally {
        clearTimeout(timer);
      }
      // A newer restart owns the offset now
      if (seq === seekSeq.current) {
        exactRef.current = true;
        offsetRef.current = t;
        setStartPoint({ t, exact: true });
      }
    })();
  }, [apiUrl, beginStall, knownPosRef]);

  // Stops the resume check after a long pause
  const clearResumeCheck = useCallback(() => {
    if (resumeCheckRef.current) clearTimeout(resumeCheckRef.current);
    resumeCheckRef.current = null;
  }, []);
  useEffect(() => clearResumeCheck, [clearResumeCheck]);

  // 2) Probe the chosen file for duration, video info and audio tracks
  useEffect(() => {
    if (!file || !hash) return;
    // A seamless switch already brought the probe's results along
    const adopted = adoptRef.current;
    if (adopted && adopted.hash === hash && adopted.fileId === file.id) return;
    let cancelled = false;
    setStatus('player.readingInfo');
    setBuffering(true);
    setDuration(0);
    setAudioTracks([]);
    setAudio(0);
    setVideoInfo(null);
    setStream(null);
    setStartPoint({ t: 0, exact: true });
    setCurrent(0);
    (async () => {
      try {
        const res = await fetch(`${apiUrl}/api/torrent-stream/probe?hash=${hash}&index=${file.id}${capsQuery()}${runtimeCheckQuery(metaRef.current)}`);
        const data = await res.json();
        if (cancelled) return;
        if (!res.ok) throw new Error(data.error || tRef.current('player.couldNotRead'));
        // Not the whole title (e.g. 86 of 173 minutes): the parent finds a complete version
        if (data.duration_mismatch && onWrongLengthRef.current?.(hash)) return;
        const dur = data.duration || 0;
        // Resume position only applies to the first file that gets opened
        const start = startRef.current;
        startRef.current = 0;
        setDuration(dur);
        const tracks: AudioTrack[] = Array.isArray(data.audio_tracks) ? data.audio_tracks : [];
        setAudioTracks(tracks);
        // A resume may still be fetching the original language (waits ≤ 1.5 s, cached)
        const m = metaRef.current;
        const originalLang = m?.original_language || (m && tracks.length > 1
          ? await getOriginalLanguage(m.tmdb_id, m.media_type, 1500) : undefined);
        if (cancelled) return;
        // Default track before the first stream starts (seekpoint and play depend on it)
        const track = pickAudioTrack(tracks, {
          profileLang: localeRef.current,
          originalLang,
          prefLang: readPlayerPrefs(getActiveProfileId()).audioLang,
        });
        audioRef.current = track;
        setAudio(track);
        setVideoInfo(data.width > 0 ? { codec: data.video_codec || '', width: data.width, height: data.height || 0, hdr: !!data.hdr, direct: !!data.direct } : null);
        setStatus('player.buffering');
        restartAt(start > 0 && (dur <= 0 || start < dur - 10) ? start : 0);
      } catch (e: any) {
        if (cancelled) return;
        if (sourceFailed('probe failed', startRef.current)) return;
        setError(shownSourceRef.current?.debrid ? tRef.current('player.playbackFailedNeutral') : e.message || tRef.current('player.couldNotRead'));
        onRecoveredRef.current?.();
      }
    })();
    return () => { cancelled = true; };
    // restartAt and onSourceFailed only change together with hash/file or the parent's source
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiUrl, hash, file]);

  // 3) Subtitles for the playing file: listed once its hash/index are known (a new source or
  // episode mounts a new PlayerCore and lists again)
  const subLangs = locale === 'tr' ? ['tr', 'en'] : ['en', 'tr'];
  const subLangsKey = subLangs.join(',');
  useEffect(() => {
    const m = metaRef.current;
    if (!m?.tmdb_id || !hash || !file) return;
    let cancelled = false;
    const timers: ReturnType<typeof setTimeout>[] = [];
    const langs = subLangsKey.split(',');
    const q = {
      tmdb_id: m.tmdb_id,
      media_type: m.media_type,
      season: m.season,
      episode: m.episode,
      hash,
      index: file.id,
      langs,
      title: shownSource?.torrent_title || shownSource?.file_name || file.name || undefined,
      year: m.year,
    };
    let list: SubtitleItem[] = [];
    const show = (items: SubtitleItem[]) => {
      list = items;
      setSubs(groupSubtitles(items, langs, localeRef.current));
    };
    setSubs(null);
    const report = onSubtitleStateRef.current;
    // The profile's language is needed when the audio is in another one
    const needed = () => {
      const audioLang = normLang(audioTracksRef.current.find(a => a.index === audioRef.current)?.language) || normLang(m.original_language);
      return !hasSubtitleLang(list, langs[0]) && audioLang !== normLang(langs[0]);
    };
    // Late uploads: while it is missing, the list is asked again (caches skipped) every 10 minutes; a new
    // subtitle in that language is added and shown unless the viewer chose one
    const lateCheck = () => timers.push(setTimeout(() => {
      if (cancelled || hasSubtitleLang(list, langs[0])) return;
      fetchSubtitles(q, { refresh: true }).then(more => {
        if (cancelled) return;
        const late = lateSubtitles(list, more, langs[0], subUserPicked.current || subIdRef.current !== null);
        if (more.length > 0) show(late.items);
        if (!late.arrived) {
          report?.({ lang: langs[0], hash, missing: true });
          lateCheck();
          return;
        }
        report?.({ lang: langs[0], hash, missing: false, arrived: true });
        if (late.pick) {
          subDecided.current = true;
          setSubId(late.pick);
        }
      });
    }, LATE_SUBTITLE_POLL_MS));
    // After the retries: report a missing language (the parent looks for a release that carries it)
    const settled = () => {
      if (cancelled || !needed()) return;
      report?.({ lang: langs[0], hash, missing: true });
      lateCheck();
    };
    fetchSubtitles(q).then(items => {
      if (cancelled) return;
      show(items);
      if (hasSubtitleLang(items, langs[0])) return;
      // Slower providers may still bring the profile's language: ask again, merge, stop once it is there
      let left = SUBTITLE_RETRY_MS.length;
      SUBTITLE_RETRY_MS.forEach(ms => timers.push(setTimeout(() => {
        if (cancelled) return;
        if (hasSubtitleLang(list, langs[0])) { if (--left === 0) settled(); return; }
        fetchSubtitles(q, { fresh: true }).then(more => {
          if (!cancelled && more.length > 0) show(mergeSubtitles(list, more));
          if (--left === 0) settled();
        });
      }, ms)));
    });
    return () => {
      cancelled = true;
      timers.forEach(clearTimeout);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hash, file?.id, subLangsKey]);

  // Default subtitle once the list and the audio track are known (the viewer's choice wins later).
  // A fuller list that arrives later picks again while nothing is shown and the viewer chose nothing.
  useEffect(() => {
    if (!subs || duration <= 0 || subs === subPickedFor.current) return;
    // The viewer switched to a release for its subtitle in this language: show it as their choice
    const want = wantSubLangRef?.current;
    if (want && wantSubLangRef && hasSubtitleLang(subs, want)) {
      wantSubLangRef.current = '';
      const id = bestSubtitleOf(subs, want);
      subDecided.current = true;
      subUserPicked.current = true;
      subPickedFor.current = subs;
      setSubId(id);
      writePlayerPrefs(getActiveProfileId(), { sub: { lang: normLang(want) } });
      return;
    }
    if (subDecided.current && (subUserPicked.current || subIdRef.current !== null)) return;
    subDecided.current = true;
    subPickedFor.current = subs;
    const track = audioTracks.find(a => a.index === audio);
    setSubId(pickSubtitle(subs, {
      profileLang: locale,
      pref: readPlayerPrefs(getActiveProfileId()).sub,
      audioLang: track?.language,
    }));
  }, [subs, duration, audioTracks, audio, locale]);

  // A subtitle file failed (OpenSubtitles' daily limit, a provider error): the next one of the same
  // language from another provider is shown instead (subtitles.ts nextSubtitleAfterFailure)
  const subtitleFailed = useCallback((id: string, why: SubtitleFailure) => {
    const failed = new Set(failedSubsRef.current);
    failed.add(id);
    failedSubsRef.current = failed;
    setFailedSubs(failed);
    if (why === 'quota' && !quotaNoticeRef.current) {
      quotaNoticeRef.current = true;
      setSubNotice('player.subQuotaReached');
    }
    const next = nextSubtitleAfterFailure(subsRef.current || [], id, why, failed);
    if (next && subIdRef.current === id) setSubId(next);
  }, []);

  useEffect(() => {
    if (!subId) { setCues([]); return; }
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    // A live embedded track (big file) is read from the playing position on: ask again while it grows
    const load = () => fetchSubtitleFile(subId, filePosRef.current).then(f => {
      if (cancelled) return;
      if (f.failed) {
        setCues([]);
        subtitleFailed(subId, f.failed);
        return;
      }
      if (f.cues.length > 0 || !f.partial) setCues(f.cues);
      if (f.partial) timer = setTimeout(load, LIVE_CUES_POLL_MS);
    });
    load();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [subId, stream?.t]); // a seek asks a live track for its new position at once

  const chooseSubtitle = useCallback((id: string | null) => {
    subDecided.current = true;
    subUserPicked.current = true;
    setSubId(id);
    const s = id ? subs?.find(x => x.id === id) : null;
    writePlayerPrefs(getActiveProfileId(), { sub: s ? { lang: normLang(s.language), id: s.id } : 'off' });
  }, [subs]);

  const closePanel = useCallback((opts?: { refocus?: boolean }) => {
    setPanelOpen(false);
    panelClosedAt.current = Date.now();
    if (opts?.refocus) panelButtonRef.current?.focus();
  }, []);

  const src = file && hash && duration > 0 && stream
    ? `${apiUrl}/api/torrent-stream/play?hash=${hash}&index=${file.id}&t=${stream.t.toFixed(3)}&audio=${audio}${capsQuery()}`
    : '';

  const stallResetTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const recoveries = useRef(0);
  const hasPlayed = useRef(false);
  // Torrent whose stream already got its one silent restart after failing before the first frame
  const startRetriedRef = useRef('');

  const clearStall = useCallback(() => {
    if (stallTimer.current) clearTimeout(stallTimer.current);
    stallTimer.current = null;
  }, []);

  // The whole recovery chain failed: only now an error shows (a neutral one for TorBox, whose
  // failures have nothing to do with peers)
  const failChain = useCallback((key: 'player.connectionLost' | 'player.playbackFailed') => {
    const debrid = !!shownSourceRef.current?.debrid;
    setError(tRef.current(debrid ? (key === 'player.connectionLost' ? 'player.connectionLostNeutral' : 'player.playbackFailedNeutral') : key));
    onRecoveredRef.current?.();
  }, []);

  // The source failed for good: the parent resolves again without it (one stable overlay over
  // the last frame meanwhile); false when it is out of tries
  const sourceFailed = useCallback((why: string, position = stateRef.current.position): boolean => {
    console.info(`[nextflix] restart: ${why}: trying another source`);
    freezeFrameRef.current?.(videoRef.current);
    onRecoveringRef.current?.();
    return onSourceFailedRef.current(position, true);
  }, []);

  // A broken stream restarts silently at the position (a paused viewer stays paused, the restart
  // only gets the stream ready), up to MAX_RECOVERIES times; then the source is replaced, and only
  // when that is out of tries the error shows
  const recover = useCallback((why: string) => {
    clearStall();
    if (recoveries.current >= MAX_RECOVERIES) {
      if (!sourceFailed(`${why} (${MAX_RECOVERIES} restarts)`)) failChain('player.connectionLost');
      return;
    }
    recoveries.current++;
    console.info(`[nextflix] restart: ${why}`);
    startPausedRef.current = viewerPausedAtRef.current > 0;
    restartAt(stateRef.current.position);
  }, [clearStall, restartAt, sourceFailed, failChain]);

  const armStall = useCallback(() => {
    clearStall();
    if (stallResetTimer.current) clearTimeout(stallResetTimer.current);
    // A stall right after a long pause, or on a stream that delivered nothing for a while (the
    // browser played what it had cached), is a stream that died: restart it quickly
    const now = Date.now();
    const dead = now - resumedAtRef.current < AFTER_PAUSE_MS || now - lastDataRef.current > QUIET_STREAM_MS;
    const base = !hasPlayed.current ? FIRST_START_MS : dead ? RESUME_CHECK_MS : STALL_MS;
    stallTimer.current = setTimeout(() => {
      const v = videoRef.current;
      // A paused player isn't stalled (a buffer hold is); the watchdog is armed again on the next 'waiting'
      if (v && (!v.paused || holdRef.current)) recover('stalled');
    }, base * (1 + recoveries.current));
  }, [clearStall, recover]);

  const clearHold = useCallback(() => {
    holdRef.current = null;
    setHolding(false);
  }, []);

  // Hold the active element paused until startBuffer.ts says enough is buffered
  const startHold = useCallback((kind: HoldKind) => {
    holdRef.current = { kind, since: kind === 'stall' ? Date.now() : null, ahead: 0 };
    setHolding(true);
    const v = videoRef.current;
    if (v) {
      v.preload = 'auto'; // keep downloading while paused
      if (!v.paused) v.pause();
    }
  }, []);

  // A wait mid-playback that lasts STALL_CONFIRM_MS: buffering, and resume only with a few seconds buffered
  const stallConfirmRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const clearStallConfirm = useCallback(() => {
    if (stallConfirmRef.current) clearTimeout(stallConfirmRef.current);
    stallConfirmRef.current = null;
  }, []);
  const confirmStall = useCallback((v: HTMLVideoElement) => {
    clearStallConfirm();
    const seq = seekSeq.current;
    const from = v.currentTime;
    stallConfirmRef.current = setTimeout(() => {
      stallConfirmRef.current = null;
      if (videoRef.current !== v || seekSeq.current !== seq || !hasPlayed.current || v.ended || (v.paused && !holdRef.current)) return;
      if (v.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA && v.currentTime > from) return;
      setBuffering(true);
      if (!v.seeking && !holdRef.current) startHold('stall');
    }, STALL_CONFIRM_MS);
  }, [clearStallConfirm, startHold]);

  useEffect(() => {
    if (!holding) return;
    const timer = setInterval(() => {
      const v = videoRef.current;
      const h = holdRef.current;
      if (!v || !h) return;
      const now = Date.now();
      if (h.since === null && v.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) h.since = now;
      const end = bufferedEnd(v.buffered, v.currentTime);
      const ahead = end >= 0 ? end - v.currentTime : 0;
      // A growing buffer is progress: the stall watchdog only restarts a stream that stopped
      if (ahead > h.ahead + 0.5) {
        h.ahead = ahead;
        armStall();
      }
      const dur = stateRef.current.duration;
      const done = holdDone(h.kind, {
        ahead,
        waitedMs: h.since === null ? 0 : now - h.since,
        fetching: v.networkState === HTMLMediaElement.NETWORK_LOADING,
        atEnd: dur > 0 && end >= 0 && offsetRef.current + end >= dur - 1,
      });
      if (!done) return;
      clearHold();
      v.play().catch(e => { if (e?.name !== 'AbortError') setPlaying(false); });
    }, 250);
    return () => clearInterval(timer);
  }, [holding, armStall, clearHold]);

  useEffect(() => () => {
    clearStall();
    clearStallConfirm();
    if (stallResetTimer.current) clearTimeout(stallResetTimer.current);
    seekCtrl.current?.abort();
  }, [clearStall, clearStallConfirm]);

  // Start the new stream whenever it changes (file, seek, audio track or recovery)
  useEffect(() => {
    const v = videoRef.current;
    if (!v || !src) return;
    // The element a seamless switch made active already plays this URL
    if (adoptRef.current?.url === src) {
      adoptRef.current.url = '';
      return;
    }
    // A restart while the viewer paused only gets the stream ready; Play then starts at once
    const keepPaused = startPausedRef.current;
    startPausedRef.current = false;
    setEnded(false);
    hasPlayed.current = false;
    advanceFromRef.current = null;
    lastDataRef.current = Date.now();
    v.src = debugFailPlay() ? src.replace(/hash=[0-9a-f]+/, 'hash=debug-fail') : src;
    // A source change must never leave the element muted or at another volume
    v.muted = mutedRef.current;
    v.volume = volumeRef.current;
    clearHold();
    if (keepPaused) {
      v.preload = 'auto';
      setBuffering(false);
      return;
    }
    setBuffering(true);
    // A borderline swarm starts only with a larger buffer: the first stream of a source, never a
    // seek or restart, and never a TorBox source (it delivers at line speed)
    const s = shownSourceRef.current;
    const key = `${hashRef.current}:${fileRef.current?.id ?? ''}`;
    if (needsStartHold(s?.ratio) && !s?.debrid && startHeldRef.current !== key) {
      startHeldRef.current = key;
      startHold('start');
    } else {
      startHeldRef.current = key;
      v.play().catch(e => { if (e?.name !== 'AbortError') setPlaying(false); });
    }
    armStall();
    // stream.n restarts the same URL
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [src, stream?.n]);

  const persist = useCallback((opts: { keepalive?: boolean; finished?: boolean } = {}) => {
    const m = metaRef.current;
    const pid = getActiveProfileId();
    const s = stateRef.current;
    if (!m || !pid || !s.hash || s.duration <= 0) return;
    const pos = opts.finished ? s.duration : s.saveAt;
    if (pos < 10) return;
    saveProgress({
      profile_id: pid,
      media_type: m.media_type,
      tmdb_id: m.tmdb_id,
      title: m.title,
      poster_path: m.poster_path || '',
      backdrop_path: m.backdrop_path || '',
      season: m.media_type === 'tv' ? m.season || 0 : 0,
      episode: m.media_type === 'tv' ? m.episode || 0 : 0,
      episode_title: m.episode_title || '',
      magnet: magnetRef.current,
      hash: s.hash,
      file_index: s.index,
      position: Math.round(pos * 10) / 10,
      duration: Math.round(s.duration * 10) / 10,
    }, { keepalive: opts.keepalive });
  }, []);

  // Save every 10 s while playing
  useEffect(() => {
    if (!playing) return;
    const t = setInterval(() => persist(), 10000);
    return () => clearInterval(t);
  }, [playing, persist]);

  // Save when the tab goes away, and stop downloading + save when the player closes
  useEffect(() => {
    const onHide = () => persist({ keepalive: true });
    window.addEventListener('pagehide', onHide);
    const els = allEls.current;
    return () => {
      window.removeEventListener('pagehide', onHide);
      persist({ keepalive: true });
      prepRef.current?.cancel();
      prepRef.current = null;
      // Unmounting: the parent may already play something else, so it is not told
      finishFadeRef.current?.(false);
      els.forEach(v => {
        v.pause();
        v.removeAttribute('src');
        v.load();
      });
    };
  }, [persist]);

  // Restarts after a native seek that the browser accepted but never finished
  const nativeSeekTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => { if (nativeSeekTimer.current) clearTimeout(nativeSeekTimer.current); }, []);

  const seekTo = useCallback((t: number) => {
    const max = duration > 0 ? duration - 1 : 0;
    const target = Math.max(0, Math.min(max, t));
    const v = videoRef.current;
    const rel = target - offset;
    // Already buffered in the current stream: seek in place, no restart and no audio glitch
    if (v && stream && !ended && rel >= 0 &&
        inRanges(v.buffered, rel, 0.5) && inRanges(v.seekable, rel, 0)) {
      v.currentTime = rel;
      if (Math.abs(v.currentTime - rel) < 1) {
        prepRef.current?.onSeek(target);
        setCurrent(rel);
        if (nativeSeekTimer.current) clearTimeout(nativeSeekTimer.current);
        nativeSeekTimer.current = setTimeout(() => {
          if (videoRef.current?.seeking) restartAt(target, true);
        }, 4000);
        return;
      }
    }
    restartAt(target, true);
  }, [duration, offset, stream, ended, restartAt]);

  const togglePlay = useCallback(() => {
    const v = videoRef.current;
    if (!v) return;
    const hold = holdRef.current;
    if (hold) {
      // The viewer decides: Play during the start buffer starts now, Pause during a stall stays paused
      clearHold();
      if (hold.kind === 'start') v.play().catch(() => {});
      else setPlaying(false);
      return;
    }
    if (v.paused) v.play().catch(() => {});
    else v.pause();
  }, [clearHold]);

  const revealControls = useCallback(() => {
    setShowControls(true);
    if (hideTimer.current) clearTimeout(hideTimer.current);
    // Controls stay up while the Audio & Subtitles panel is open
    hideTimer.current = setTimeout(() => { if (!panelOpenRef.current) setShowControls(false); }, 3000);
  }, []);

  useEffect(() => { if (!panelOpen) revealControls(); }, [panelOpen, revealControls]);

  useEffect(() => () => { if (hideTimer.current) clearTimeout(hideTimer.current); }, []);

  // Touch layout: a tap shows / hides the controls (Play is the big center button); a double-tap
  // on the left / right side seeks back / forward and further taps add up (seek mode)
  const controlsVisibleRef = useRef(false);
  const tapRef = useRef<{ at: number; side: number; timer: ReturnType<typeof setTimeout> | null }>({ at: 0, side: 0, timer: null });
  const pressRef = useRef<{ id: number; x: number; y: number; at: number } | null>(null);
  const seekAccRef = useRef<{ side: number; total: number; base: number; timer: ReturnType<typeof setTimeout> | null } | null>(null);
  const [seekFx, setSeekFx] = useState<{ side: number; total: number; n: number } | null>(null);
  const seekFxTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const seekToRef = useRef(seekTo);
  seekToRef.current = seekTo;
  const posRef = useRef(position);
  posRef.current = position;
  const durationRef = useRef(duration);
  durationRef.current = duration;
  useEffect(() => () => {
    if (tapRef.current.timer) clearTimeout(tapRef.current.timer);
    if (seekAccRef.current?.timer) clearTimeout(seekAccRef.current.timer);
    if (seekFxTimer.current) clearTimeout(seekFxTimer.current);
  }, []);

  const toggleControls = useCallback(() => {
    if (!controlsVisibleRef.current) {
      revealControls();
      return;
    }
    if (hideTimer.current) clearTimeout(hideTimer.current);
    setShowControls(false);
  }, [revealControls]);

  const doubleTapSeek = useCallback((side: number) => {
    let acc = seekAccRef.current;
    if (acc && acc.side !== side) {
      // Turned around: the seek so far runs now, the other side starts over
      if (acc.timer) clearTimeout(acc.timer);
      seekToRef.current(acc.base + acc.total);
      acc = null;
    }
    const a = acc || { side, total: 0, base: posRef.current, timer: null };
    const dur = durationRef.current;
    // Never past the start or the end
    const room = side < 0 ? a.base : Math.max(0, dur - 1 - a.base);
    a.total = side * Math.min(room, Math.abs(a.total) + 10);
    if (a.timer) clearTimeout(a.timer);
    a.timer = setTimeout(() => {
      if (seekAccRef.current === a) seekAccRef.current = null;
      if (a.total !== 0) seekToRef.current(a.base + a.total);
    }, SEEK_COMMIT_MS);
    seekAccRef.current = a;
    setSeekFx(fx => ({ side, total: Math.round(a.total), n: (fx?.n ?? 0) + 1 }));
    if (seekFxTimer.current) clearTimeout(seekFxTimer.current);
    seekFxTimer.current = setTimeout(() => setSeekFx(null), SEEK_COMMIT_MS + 300);
  }, []);

  const onTouchTap = useCallback((x: number, width: number) => {
    // The tap that closed the Audio & Subtitles panel does nothing else
    if (Date.now() - panelClosedAt.current < 400) return;
    const side = x < width * 0.35 ? -1 : x > width * 0.65 ? 1 : 0;
    const now = Date.now();
    const tap = tapRef.current;
    const pending = tap.timer;
    if (pending) clearTimeout(pending);
    tap.timer = null;
    const quick = now - tap.at < DOUBLE_TAP_MS && tap.side === side;
    const seeking = !!seekAccRef.current && now - tap.at < SEEK_COMMIT_MS;
    tap.at = now;
    tap.side = side;
    if (side !== 0 && (seeking || (pending && quick))) {
      doubleTapSeek(side);
      return;
    }
    // The center has no double-tap, so it answers at once; the sides wait for a second tap
    if (side === 0) toggleControls();
    else tap.timer = setTimeout(() => { tap.timer = null; toggleControls(); }, DOUBLE_TAP_MS);
  }, [doubleTapSeek, toggleControls]);

  useWakeLock(touchUi && playing);

  // Touch: subtitles sit on the picture and scale with its height (portrait shows a small band)
  const rootRef = useRef<HTMLDivElement>(null);
  const bottomBarRef = useRef<HTMLDivElement>(null);
  const [rootBox, setRootBox] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const el = rootRef.current;
    if (!touchUi || !el) return;
    const measure = () => setRootBox({ w: el.clientWidth, h: el.clientHeight });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [touchUi]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement)?.tagName === 'SELECT') return;
      if (e.key === ' ' || e.key === 'k') { e.preventDefault(); togglePlay(); }
      else if (e.key === 'ArrowRight') seekTo(position + 10);
      else if (e.key === 'ArrowLeft') seekTo(position - 10);
      else if (e.key === 'f') toggleFullscreen();
      else if (e.key === 'm') setMuted(m => !m);
      else if (e.key === 'n' && onNext) onNext();
      else return;
      revealControls();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [togglePlay, seekTo, position, toggleFullscreen, revealControls, onNext]);

  useEffect(() => {
    const v = videoRef.current;
    if (v) { v.muted = muted; v.volume = volume; }
  }, [muted, volume]);

  // Seamless switch. The hidden slot loads the new source ahead of the visible position
  // (seamlessSwitch.ts); adopt() makes it the active stream at the frame where it starts.
  const finishFadeRef = useRef<((notify: boolean) => void) | null>(null);
  const onSwitchedRef = useRef(onSwitched);
  onSwitchedRef.current = onSwitched;
  const adoptFnRef = useRef<(p: PreparedStream) => void>(() => {});
  adoptFnRef.current = (p: PreparedStream) => {
    const oldSlot = activeSlotRef.current;
    const newSlot = 1 - oldSlot;
    const old = slotEls.current[oldSlot];
    const el = slotEls.current[newSlot];
    if (!el) return;
    // The old stream's restart, stall and seek machinery is done
    clearStall();
    clearHold();
    seekSeq.current++;
    seekCtrl.current?.abort();
    seekCtrl.current = null;
    if (nativeSeekTimer.current) clearTimeout(nativeSeekTimer.current);
    if (longStallTimer.current) clearTimeout(longStallTimer.current);
    stall.current = null;
    stallTimes.current = [];
    recoveries.current = 0;
    healthMon.current.reset();
    // Switch the active element first: the old one's pause event is then ignored
    activeSlotRef.current = newSlot;
    videoRef.current = el;
    old?.pause();
    el.muted = mutedRef.current;
    el.volume = volumeRef.current;
    hasPlayed.current = true;
    el.play().catch(e => { if (e?.name !== 'AbortError') setPlaying(false); });
    // It already plays (its 'playing' fired while hidden): only a stream that still has to start needs the watchdog
    if (el.paused || el.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) armStall();
    setPlaying(true);

    const nextFile: StreamFile = { id: p.fileId, name: p.source.file_name || '', path: '', size: 0 };
    hashRef.current = p.hash;
    fileRef.current = nextFile;
    audioRef.current = p.audio;
    offsetRef.current = p.offset;
    exactRef.current = true;
    if (p.source.magnet) magnetRef.current = p.source.magnet;
    adoptRef.current = { hash: p.hash, fileId: p.fileId, url: p.url };
    setHash(p.hash);
    setFile(nextFile);
    setDuration(p.duration);
    setAudioTracks(p.tracks);
    setAudio(p.audio);
    setVideoInfo(p.video);
    setStartPoint({ t: p.offset, exact: true });
    setStream(s => ({ t: p.t, n: (s?.n ?? 0) + 1 }));
    setCurrent(el.currentTime);
    setBuffering(false);
    setEnded(false);
    setShownSource(p.source);
    // Subtitles are listed again for the new file and the default pick runs again (as on a new source)
    subDecided.current = false;
    setSubDelay(0);
    setActiveSlot(newSlot);
    setFadingSlot(oldSlot);
    if (p.kind === 'upgrade') setSwitchNote(qualityBadge(tRef.current, p.video, p.source)?.label || '');

    // After the crossfade the old element lets go of its stream, then the parent releases the torrent
    let finished = false;
    const finish = (notify: boolean) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      finishFadeRef.current = null;
      if (old) {
        old.removeAttribute('src');
        old.load();
        old.muted = true;
      }
      if (!notify) return;
      setFadingSlot(f => (f === oldSlot ? null : f));
      onSwitchedRef.current?.(p.source, p.kind);
    };
    const timer = setTimeout(() => finish(true), 250);
    finishFadeRef.current = finish;
  };

  const switchHost = useMemo<SwitchHost>(() => ({
    apiUrl,
    meta: () => metaRef.current,
    visible: () => videoRef.current,
    hidden: () => slotEls.current[1 - activeSlotRef.current],
    offset: () => offsetRef.current,
    offsetExact: () => exactRef.current,
    duration: () => stateRef.current.duration,
    stalled: () => {
      const v = videoRef.current;
      return !!v && bufferingRef.current && (!v.paused || !!holdRef.current);
    },
    bufferedAhead: () => {
      const v = videoRef.current;
      const end = v ? bufferedEnd(v.buffered, v.currentTime) : -1;
      return v && end >= 0 ? end - v.currentTime : 0;
    },
    currentAudio: () => audioTracksRef.current.find(a => a.index === audioRef.current),
    defaultAudio: tracks => pickAudioTrack(tracks, {
      profileLang: localeRef.current,
      originalLang: metaRef.current?.original_language,
      prefLang: readPlayerPrefs(getActiveProfileId()).audioLang,
    }),
    swap: p => adoptFnRef.current(p),
  }), [apiUrl]);

  // Why the last preparation was dropped (CoreSwitch.failure)
  const lastPrepFailRef = useRef('');
  const lastPrepStatsRef = useRef('');
  // One preparation at a time; only while the visible stream plays (or is paused) smoothly
  const prepareSwitch = useCallback((next: StreamSource, kind: SwitchKind): Promise<SwitchResult> => {
    if (prepRef.current || finishFadeRef.current) return Promise.resolve('busy');
    const v = videoRef.current;
    if (!v || !hashRef.current || !fileRef.current || blockedRef.current || !everPlayed.current ||
        stateRef.current.duration <= 0 || (bufferingRef.current && (!v.paused || !!holdRef.current))) {
      return Promise.resolve('unavailable');
    }
    const prep = new SwitchPrep(switchHost, next, kind);
    prepRef.current = prep;
    return prep.promise.then(res => {
      lastPrepStatsRef.current = prep.stats();
      if (res === 'abandoned') {
        lastPrepFailRef.current = prep.reason || 'unknown';
        console.warn(`[nextflix] ${kind} to ${prep.hash.slice(0, 8)}:${next.index}${next.debrid ? ' (TorBox)' : ''} dropped: ${prep.reason} (${lastPrepStatsRef.current})`);
      }
      return res;
    }).finally(() => { if (prepRef.current === prep) prepRef.current = null; });
  }, [switchHost]);

  const coreSwitch = useMemo<CoreSwitch>(() => ({
    prepare: prepareSwitch,
    failure: () => lastPrepFailRef.current,
    stats: () => lastPrepStatsRef.current,
    preparing: () => (prepRef.current ? { kind: prepRef.current.kind, ready: prepRef.current.isReady } : null),
    busy: () => !!prepRef.current || !!finishFadeRef.current,
    cancel: () => prepRef.current?.cancel('cancelled'),
  }), [prepareSwitch]);

  useEffect(() => {
    if (!switchRef) return;
    switchRef.current = coreSwitch;
    return () => { if (switchRef.current === coreSwitch) switchRef.current = null; };
  }, [switchRef, coreSwitch]);

  // Buffer health sample once a second while the active stream plays smoothly
  useEffect(() => {
    const timer = setInterval(() => {
      const v = videoRef.current;
      // readyState: a wait not confirmed as a stall yet (confirmStall) is not smooth playback either
      if (!v || !v.getAttribute('src') || v.paused || v.seeking || bufferingRef.current || blockedRef.current ||
          v.readyState < HTMLMediaElement.HAVE_FUTURE_DATA || !hasPlayed.current || !exactRef.current) return;
      const off = offsetRef.current;
      const pos = off + v.currentTime;
      const end = bufferedEnd(v.buffered, v.currentTime);
      const fileEnd = end >= 0 ? off + end : pos;
      const dur = stateRef.current.duration;
      const suspended = suspendSeenRef.current;
      suspendSeenRef.current = false;
      const h = healthMon.current.push({
        at: Date.now(), pos, end: fileEnd,
        fetching: v.networkState === HTMLMediaElement.NETWORK_LOADING && !suspended,
        atEnd: dur > 0 && fileEnd >= dur - 1,
      });
      onBufferHealthRef.current?.(h);
    }, 1000);
    return () => clearInterval(timer);
  }, []);

  // Never left without a playing element: an active element that holds no media (readyState 0) and is
  // paused (a viewer's pause keeps its data) restarts the stream at the last position
  useEffect(() => {
    let deadChecks = 0;
    const timer = setInterval(() => {
      const v = videoRef.current;
      const dead = !!v && v.paused && v.readyState === HTMLMediaElement.HAVE_NOTHING && everPlayed.current &&
        v.networkState !== HTMLMediaElement.NETWORK_LOADING && !blockedRef.current && !holdRef.current && !finishFadeRef.current;
      deadChecks = dead ? deadChecks + 1 : 0;
      if (deadChecks >= 2) {
        deadChecks = 0;
        recover('no media');
      }
    }, 3000);
    return () => clearInterval(timer);
  }, [recover]);

  // While the viewer pauses, the server keeps the file ready (stream guard, TorBox link and connection)
  useEffect(() => {
    if (playing || !hash || !file || error || ended) return;
    const timer = setInterval(() => {
      if (viewerPausedAtRef.current) keepSourceAlive(hashRef.current, fileRef.current?.id ?? 0);
    }, KEEPALIVE_MS);
    return () => clearInterval(timer);
  }, [playing, hash, file, error, ended]);

  // Play after the viewer's pause. After a long one the stream may have died meanwhile (an idle
  // connection dropped, ffmpeg or TorrServer gone): a dead element restarts at once, and one
  // whose buffer does not grow within RESUME_CHECK_MS restarts at the position, instead of
  // waiting for the stall watchdog.
  const resumed = () => {
    const pausedAt = viewerPausedAtRef.current;
    viewerPausedAtRef.current = 0;
    clearResumeCheck();
    if (!pausedAt || Date.now() - pausedAt < LONG_PAUSE_MS) return;
    resumedAtRef.current = Date.now();
    const v = videoRef.current;
    if (!v || blockedRef.current) return;
    if (v.error || (v.readyState === HTMLMediaElement.HAVE_NOTHING && v.networkState !== HTMLMediaElement.NETWORK_LOADING)) {
      recover('dead after a long pause');
      return;
    }
    const startEnd = bufferedEnd(v.buffered, v.currentTime);
    const t0 = Date.now();
    const check = () => {
      resumeCheckRef.current = null;
      const el = videoRef.current;
      if (el !== v || el.paused || blockedRef.current) return;
      const end = bufferedEnd(el.buffered, el.currentTime);
      if (end > startEnd + 0.5) return; // data flows again
      const ahead = end >= 0 ? end - el.currentTime : 0;
      if (Date.now() - t0 >= RESUME_CHECK_MS && ahead < 1.5) {
        recover('no data after a long pause');
        return;
      }
      if (Date.now() - t0 < 20000) resumeCheckRef.current = setTimeout(check, 500);
    };
    resumeCheckRef.current = setTimeout(check, 500);
  };

  useEffect(() => {
    if (!switchNote) return;
    const timer = setTimeout(() => setSwitchNote(''), 2000);
    return () => clearTimeout(timer);
  }, [switchNote]);

  // TV: warm the next episode's source near the end of this one
  const nextDue = nextWarmDue(position, duration);
  const nextDueRef = useRef({ position, duration });
  nextDueRef.current = { position, duration };
  useEffect(() => {
    if (nextDue) onPrefetchNext?.(nextDueRef.current.position, nextDueRef.current.duration);
  }, [nextDue, onPrefetchNext]);

  // Skip intro / end credits of the episode (skipSegments.ts)
  const skipSegs = useSkipSegments(meta, hash, file?.id, duration);
  const skip = skipPhase(skipSegs, position, duration);
  // Leaving from the end credits: mark this episode watched before the next one starts
  const nextFromCredits = () => { persist({ finished: true }); onNext?.(); };

  // The end credits started: count down to the next episode while they play
  useEffect(() => {
    if (skip !== 'credits') {
      creditsDismissedRef.current = false;
      // Seeked back out of the credits (or away from the end): no next episode countdown
      if (!ended && countdown !== null) setCountdown(null);
      return;
    }
    if (!onNext || ended || countdown !== null || creditsDismissedRef.current || !playing) return;
    if (readPlayerPrefs(getActiveProfileId()).autoplayNext === false) return;
    setCountdownAt('credits');
    setCountdown(5);
  }, [skip, onNext, ended, countdown, playing]);

  // Auto-play the next episode a few seconds after this one ends (or its credits start)
  useEffect(() => {
    if (countdown === null) return;
    if (countdown <= 0) {
      if (countdownAt === 'credits') persist({ finished: true });
      onNext?.();
      return;
    }
    // Paused during the credits: the countdown waits
    if (countdownAt === 'credits' && !playing) return;
    const t = setTimeout(() => setCountdown(c => (c === null ? null : c - 1)), 1000);
    return () => clearTimeout(t);
  }, [countdown, onNext, countdownAt, playing, persist]);

  const handleEnded = () => {
    // The stream ended well before the file did: ffmpeg or TorrServer dropped it
    if (duration > 0 && stateRef.current.position < duration - 30) {
      recover('ended early');
      return;
    }
    clearStall();
    setPlaying(false);
    setEnded(true);
    persist({ finished: true });
    // With autoplay off in Settings only the Next button is shown
    if (onNext && readPlayerPrefs(getActiveProfileId()).autoplayNext !== false) {
      setCountdownAt('end');
      setCountdown(c => c ?? 5);
    }
  };

  const handleError = () => {
    const v = videoRef.current;
    if (!src || !v?.getAttribute('src')) return;
    if (hasPlayed.current) {
      recover('stream error');
      return;
    }
    clearStall();
    // The stream broke before its first frame (dropped or cut by the server, not a dead
    // swarm): restart it once at the same spot, then resolve again, and only then give up
    const h = hashRef.current;
    if (h && startRetriedRef.current !== h) {
      startRetriedRef.current = h;
      console.info('[nextflix] restart: failed before its first frame');
      startPausedRef.current = viewerPausedAtRef.current > 0;
      restartAt(stateRef.current.position);
      return;
    }
    if (!sourceFailed('failed before its first frame')) failChain('player.playbackFailed');
  };

  const retry = () => {
    setError('');
    recoveries.current = 0;
    if (!onSourceFailed(stateRef.current.position, false)) restartAt(stateRef.current.position);
  };

  // Another audio track restarts the stream at the same position; the language is remembered per profile
  const selectAudio = (n: number) => {
    if (n === audio) return;
    audioRef.current = n;
    setAudio(n);
    // A prepared switch carries the old language
    prepRef.current?.onSeek(position, true);
    restartAt(position);
    const lang = normLang(audioTracks.find(a => a.index === n)?.language);
    // Picking the original language keeps the 'original' preference from Settings
    const keepOriginal = readPlayerPrefs(getActiveProfileId()).audioLang === AUDIO_ORIGINAL &&
      lang === normLang(metaRef.current?.original_language);
    if (lang && !keepOriginal) writePlayerPrefs(getActiveProfileId(), { audioLang: lang });
  };

  const isActive = (e: React.SyntheticEvent<HTMLVideoElement>) => e.currentTarget === videoRef.current;
  const badge = qualityBadge(t, videoInfo, shownSource);
  const shownTime = seekPreview ?? position;
  // Only during the end credits (the countdown is off or was dismissed); without credits data the
  // next episode comes when the video ends
  const showNextButton = !!onNext && duration > 0 && !error && countdown === null && skip === 'credits';
  const showSkipIntro = skip === 'intro' && !!skipSegs?.intro && !error && countdown === null;
  // The loading overlay comes only after LOADING_SHOW_MS without frames and goes once they ran LOADING_HIDE_MS
  const loadingShown = useSteady(buffering, LOADING_SHOW_MS, LOADING_HIDE_MS);
  const playable = !!file && duration > 0 && !error;
  // Paused by the viewer: the controls (and the Back button) stay up
  const controlsVisible = showControls || panelOpen || (playable && !playing && !buffering && countdown === null);
  const ctl = 'p-2 text-white hover:text-red-500 focus:outline-none focus-visible:text-red-500';
  controlsVisibleRef.current = controlsVisible;
  // Touch: hidden controls take no taps (they would catch taps meant for the video)
  const hiddenCls = controlsVisible ? 'opacity-100' : `opacity-0${touchUi ? ' pointer-events-none' : ''}`;
  // Touch: center buttons step aside for a double-tap seek or a seek drag
  const centerOn = controlsVisible && !seekFx && seekPreview === null;
  const touchBtn = 'flex h-12 min-w-12 items-center justify-center gap-2 rounded-full px-2.5 text-white active:bg-white/15 focus:outline-none';

  // Touch: the picture's box decides the subtitle size and place (on the picture, above the controls)
  let subLayout: { bottom: number; fontPx: number } | undefined;
  if (touchUi && rootBox.w > 0 && rootBox.h > 0) {
    const v = videoRef.current;
    const aspect = v?.videoWidth && v.videoHeight ? v.videoWidth / v.videoHeight
      : videoInfo?.width && videoInfo.height ? videoInfo.width / videoInfo.height : 16 / 9;
    const picH = Math.min(rootBox.h, rootBox.w / aspect);
    const below = (rootBox.h - picH) / 2;
    let bottom = below + picH * 0.06;
    // Above the bar's rows (its gradient padding may cover the picture)
    const bar = bottomBarRef.current;
    const rows = bar?.firstElementChild as HTMLElement | null;
    if (controlsVisible) bottom = Math.max(bottom, (bar && rows ? bar.offsetHeight - rows.offsetTop : 100) + 8);
    subLayout = { bottom, fontPx: Math.max(14, Math.min(40, picH * 0.055)) * subScale };
  }

  return (
    <div
      ref={rootRef}
      className="absolute inset-0 flex items-center justify-center"
      onMouseMove={touchUi ? undefined : revealControls}
      onPointerDownCapture={touchUi ? () => { if (controlsVisibleRef.current) revealControls(); } : undefined}
      style={{ cursor: controlsVisible ? 'default' : 'none' }}
    >
      {/* Both slots stack in their own layer under the overlays; the hidden one never touches player state */}
      <div
        className={`absolute inset-0 isolate ${touchUi ? 'touch-none' : ''}`}
        onClick={touchUi ? undefined : () => {
          // The click that closed the panel should not also pause
          if (Date.now() - panelClosedAt.current < 400) return;
          togglePlay();
        }}
        onPointerDown={touchUi ? e => { pressRef.current = { id: e.pointerId, x: e.clientX, y: e.clientY, at: Date.now() }; } : undefined}
        onPointerUp={touchUi ? e => {
          const p = pressRef.current;
          pressRef.current = null;
          // Only a short, still touch is a tap
          if (!p || p.id !== e.pointerId || Date.now() - p.at > 600 || Math.hypot(e.clientX - p.x, e.clientY - p.y) > 12) return;
          const r = e.currentTarget.getBoundingClientRect();
          onTouchTap(e.clientX - r.left, r.width);
        } : undefined}
        onPointerCancel={touchUi ? () => { pressRef.current = null; } : undefined}
      >
        {[0, 1].map(i => (
          <video
            key={i}
            ref={slotRefs[i]}
            className={`absolute inset-0 w-full h-full object-contain transition-opacity duration-150 ${
              i === activeSlot ? 'opacity-100 z-[1]' : i === fadingSlot ? 'opacity-100 z-0' : 'opacity-0 z-0 pointer-events-none'
            }`}
            playsInline
            onPlay={e => { if (isActive(e)) { setPlaying(true); resumed(); } }}
            onPause={e => {
              if (!isActive(e) || holdRef.current) return;
              setPlaying(false);
              persist();
              // A viewer's pause (not the end, not a restart): keep-alives run until Play
              if (!e.currentTarget.ended) viewerPausedAtRef.current = Date.now();
            }}
            onWaiting={e => {
              if (!isActive(e)) return;
              // The browser played what it had cached since a long pause and nothing arrived since:
              // the old stream is gone, restart at once
              const since = resumedAtRef.current;
              if (hasPlayed.current && !e.currentTarget.seeking && since > 0 && Date.now() - since < AFTER_PAUSE_MS &&
                  lastDataRef.current < since) {
                setBuffering(true);
                resumedAtRef.current = 0;
                recover('no data since the pause');
                return;
              }
              if (!hasPlayed.current) setBuffering(true);
              armStall();
              if (hasPlayed.current) beginStall(false);
              if (hasPlayed.current) confirmStall(e.currentTarget);
            }}
            onPlaying={e => {
              if (!isActive(e)) return;
              clearStallConfirm();
              setBuffering(false);
              clearStall();
              advanceFromRef.current = e.currentTarget.currentTime;
              endStall();
              hasPlayed.current = true;
              everPlayed.current = true;
              // A minute of smooth playback forgets earlier recoveries
              if (stallResetTimer.current) clearTimeout(stallResetTimer.current);
              stallResetTimer.current = setTimeout(() => { recoveries.current = 0; }, 60000);
            }}
            onLoadedMetadata={e => {
              if (!isActive(e)) return;
              const v = e.currentTarget;
              v.muted = mutedRef.current;
              v.volume = volumeRef.current;
            }}
            onSeeked={e => { if (isActive(e) && nativeSeekTimer.current) clearTimeout(nativeSeekTimer.current); }}
            onTimeUpdate={e => {
              if (!isActive(e)) return;
              const now = e.currentTarget.currentTime;
              setCurrent(now);
              // Frames run again: the kept frame and a recovery chain are done
              const from = advanceFromRef.current;
              if (from !== null && now - from >= LOADING_HIDE_MS / 1000) {
                advanceFromRef.current = null;
                if (frameShownRef.current || recoveringRef.current) onRecoveredRef.current?.();
              }
            }}
            onEnded={e => { if (isActive(e)) handleEnded(); }}
            onError={e => { if (isActive(e)) handleError(); }}
            onSuspend={e => { if (isActive(e)) suspendSeenRef.current = true; }}
            onProgress={e => { if (isActive(e)) lastDataRef.current = Date.now(); }}
          />
        ))}
      </div>

      {/* Subtitles are drawn here (the stream restarts with offsets, so native tracks would drift) */}
      {playable && !!subId && cues.length > 0 && (
        <SubtitleOverlay videoRef={videoRef} offsetRef={offsetRef} cues={cues} delay={subDelay} raised={controlsVisible} scale={subScale} layout={subLayout} />
      )}

      {/* Episode / file picker (legacy magnet mode) */}
      {!file && files.length > 1 && !error && (
        <div data-scroll className="absolute inset-0 overflow-y-auto overscroll-contain bg-black/95 p-6 md:p-12">
          <h2 className="text-white text-xl md:text-2xl font-bold mb-1">{title}</h2>
          <p className="text-white/50 text-sm mb-6">{t('player.chooseFile')}</p>
          <div className="grid gap-2 max-w-3xl">
            {files.map(f => (
              <button
                key={f.id}
                onClick={() => setFile(f)}
                className="flex items-center gap-3 text-left px-4 py-3 rounded-lg bg-white/5 hover:bg-white/15 border border-white/10 transition"
              >
                <Film className="w-4 h-4 text-red-500 shrink-0" />
                <span className="text-white text-sm flex-1 truncate">{f.name}</span>
                <span className="text-white/40 text-xs shrink-0">{formatSize(f.size)}</span>
              </button>
            ))}
          </div>
        </div>
      )}

      {/* Loading / buffering / error overlay */}
      {(error || ((loadingShown || recovering) && !ended && (file || files.length <= 1))) && (
        <div className="absolute inset-0 flex flex-col items-center justify-center pointer-events-none">
          {error ? (
            <div className="pointer-events-auto text-center px-6">
              <p className="text-red-400 text-lg mb-4">{error}</p>
              <div className="flex gap-3 justify-center">
                {!!hash && !!file && (
                  <button onClick={retry} className="flex items-center gap-2 px-5 py-2 bg-white text-black font-semibold rounded-lg hover:bg-white/80">
                    <RotateCw className="w-4 h-4" /> {t('player.retry')}
                  </button>
                )}
                <button onClick={onClose} className="px-5 py-2 bg-white/10 hover:bg-white/20 text-white rounded-lg">{t('common.close')}</button>
              </div>
            </div>
          ) : (
            <>
              <Loader2 className={`${frameShown ? 'w-8 h-8' : 'w-12 h-12'} text-red-600 animate-spin mb-3`} />
              <p className="text-white/70 text-sm">{t(recovering ? 'player.switchingSource' : duration > 0 ? 'player.buffering' : status)}</p>
              {holding && !recovering && <p className="text-white/40 text-xs mt-1">{t('player.smoothBuffering')}</p>}
            </>
          )}
        </div>
      )}

      {/* Up next countdown */}
      {countdown !== null && !!onNext && countdownAt === 'end' && (
        <div className="absolute inset-0 flex flex-col items-center justify-center bg-black/70">
          <p className="text-[#b3b3b3] text-sm uppercase tracking-widest mb-2">{t('player.nextEpisode')}</p>
          <p className="text-white text-2xl font-bold mb-6">{t('player.nextStartsIn', { label: nextLabel, n: Math.max(0, countdown) })}</p>
          <div className="flex gap-3">
            <button
              onClick={() => onNext()}
              className="flex items-center gap-2 px-6 py-2.5 bg-white text-black font-bold rounded hover:bg-white/80 transition"
            >
              <Play className="w-5 h-5 fill-current" /> {t('player.playNow')}
            </button>
            <button
              onClick={() => setCountdown(null)}
              className="px-6 py-2.5 bg-[#6d6d6eb3] hover:bg-[#6d6d6e66] text-white font-bold rounded transition"
            >
              {t('common.cancel')}
            </button>
          </div>
        </div>
      )}

      {/* Top bar */}
      <div className={`absolute top-0 inset-x-0 bg-gradient-to-b from-black/80 to-transparent flex items-center transition-opacity duration-300 ${touchUi ? `${mobileCss.topBar} z-[8] gap-2` : 'p-4 md:p-6 gap-4'} ${hiddenCls}`}>
        <BackButton label={t('player.back')} onClick={onClose} onFocus={revealControls} />
        <div className="flex-1 min-w-0">
          <p className="text-white font-semibold truncate">{title}</p>
          {subtitle ? (
            <p className="text-white/60 text-xs truncate">{subtitle}</p>
          ) : (
            !!file?.name && <p className="text-white/50 text-xs truncate">{file.name}</p>
          )}
        </div>
        {!!badge && (
          <div className="flex items-center gap-1.5 shrink-0" title={badge.tip}>
            <span className={BADGE_CLASS}>{badge.label}</span>
            {badge.hdr && <span className={BADGE_CLASS}>HDR</span>}
            {shownSource?.debrid && (
              <span className={`${BADGE_CLASS} inline-flex items-center gap-1 !text-[#e5e5e5]`} title={t('player.debridBadgeTitle')}>
                <svg viewBox="0 0 24 24" aria-hidden className="fill-current h-[1.1em] w-[1.1em]"><path d="M13.5 2 4 13.5h6.5L9 22l10-12h-6.5z" /></svg>
                {t('player.debridBadge')}
              </span>
            )}
            {shownSource?.cinema && (
              <span className={`${BADGE_CLASS} inline-flex items-center gap-1 !text-[#e5e5e5]`} title={t('cinema.badgeTitle')}>
                <Clapperboard aria-hidden className="h-[1.1em] w-[1.1em]" />
                {t('cinema.badge')}
              </span>
            )}
          </div>
        )}
        {files.length > 1 && !!file && (
          <button onClick={() => { setFile(null); videoRef.current?.pause(); }} className="text-white/70 hover:text-white text-sm px-3 py-1.5 rounded bg-white/10">
            {t('player.episodes')}
          </button>
        )}
      </div>

      {!!subNotice && (
        <div className="absolute top-20 left-1/2 -translate-x-1/2 z-20 px-4 py-2 rounded bg-black/80 border border-white/20 text-white text-sm shadow-lg pointer-events-none" role="status">
          {t(subNotice)}
        </div>
      )}

      {/* Quiet note after a seamless upgrade, where the quality badge sits */}
      {!!switchNote && !controlsVisible && (
        <div className="absolute top-5 right-5 md:top-7 md:right-7 pointer-events-none" role="status">
          <span className={`${BADGE_CLASS} bg-black/40`}>{t('player.nowQuality', { label: switchNote })}</span>
        </div>
      )}

      {/* End credits: the next episode starts when the button has filled up */}
      {countdown !== null && !!onNext && countdownAt === 'credits' && (
        <div className={`absolute ${touchUi ? mobileCss.nextUp : 'right-4 md:right-8 bottom-28'} z-[9] flex items-center gap-2`}>
          <button
            onClick={() => { creditsDismissedRef.current = true; setCountdown(null); }}
            className="px-5 py-2.5 bg-[#6d6d6eb3] hover:bg-[#6d6d6e66] text-white font-bold rounded shadow-lg transition"
          >
            {t('player.watchCredits')}
          </button>
          <button
            onClick={nextFromCredits}
            aria-label={t('player.nextStartsIn', { label: nextLabel, n: Math.max(0, countdown) })}
            className="relative overflow-hidden px-5 py-2.5 bg-white/60 text-black font-bold rounded shadow-lg"
          >
            <span className="absolute inset-y-0 left-0 bg-white transition-[width] duration-1000 ease-linear" style={{ width: `${((5 - countdown) / 5) * 100}%` }} />
            <span className="relative flex items-center gap-2">
              <SkipForward className="w-5 h-5 fill-current" /> {t('player.nextEpisode')}
            </span>
          </button>
        </div>
      )}

      {/* Skip intro */}
      {showSkipIntro && !panelOpen && (
        <button
          onClick={() => { const i = skipSegs?.intro; if (i) seekTo(i.end); }}
          className={`absolute ${touchUi ? mobileCss.nextUp : 'right-4 md:right-8 bottom-28'} z-[9] flex items-center gap-2 px-5 py-2.5 bg-black/60 border-2 border-white/80 text-white font-bold rounded shadow-lg hover:bg-white hover:text-black transition`}
        >
          <ChevronsRight className="w-5 h-5" /> {t('player.skipIntro')}
        </button>
      )}

      {/* Next episode button during the end credits */}
      {showNextButton && !panelOpen && (
        <button
          onClick={nextFromCredits}
          className={`absolute ${touchUi ? mobileCss.nextUp : 'right-4 md:right-8 bottom-28'} flex items-center gap-2 px-5 py-2.5 bg-white text-black font-bold rounded shadow-lg hover:bg-white/80 transition`}
        >
          <SkipForward className="w-5 h-5 fill-current" /> {t('player.nextEpisode')}
        </button>
      )}

      {/* Bottom controls */}
      {playable && !touchUi && (
        <div className={`absolute bottom-0 inset-x-0 px-4 md:px-6 pb-4 pt-10 bg-gradient-to-t from-black/90 to-transparent transition-opacity duration-300 ${controlsVisible ? 'opacity-100' : 'opacity-0'}`}>
          <SeekThumbnail apiUrl={apiUrl} hash={hash} index={file?.id} duration={duration} dragTime={seekPreview} position={position} playing={playing && !buffering}>
          <input
            type="range"
            min={0}
            max={duration}
            step={1}
            value={shownTime}
            onChange={e => setSeekPreview(Number(e.target.value))}
            onMouseUp={() => { if (seekPreview !== null) { seekTo(seekPreview); setSeekPreview(null); } }}
            onTouchEnd={() => { if (seekPreview !== null) { seekTo(seekPreview); setSeekPreview(null); } }}
            onKeyUp={() => { if (seekPreview !== null) { seekTo(seekPreview); setSeekPreview(null); } }}
            className="ts-range w-full"
            aria-label={t('player.seek')}
            style={{ background: `linear-gradient(to right, #E50914 ${fill(shownTime, duration)}%, rgba(255,255,255,0.3) ${fill(shownTime, duration)}%)` }}
          />
          </SeekThumbnail>
          <div className="flex items-center gap-3 mt-2">
            <button onClick={togglePlay} className={ctl} title={t('player.playPause')} aria-label={t(playing ? 'player.pause' : 'player.play')}>
              {playing ? <Pause className="w-6 h-6" /> : <Play className="w-6 h-6" />}
            </button>
            <button onClick={() => seekTo(position - 10)} className={ctl} title={t('player.back10')} aria-label={t('player.back10')}>
              <RotateCcw className="w-5 h-5" />
            </button>
            <button onClick={() => seekTo(position + 10)} className={ctl} title={t('player.forward10')} aria-label={t('player.forward10')}>
              <RotateCw className="w-5 h-5" />
            </button>
            <button onClick={() => setMuted(m => !m)} className={ctl} title={t(muted ? 'player.unmute' : 'player.mute')} aria-label={t(muted ? 'player.unmute' : 'player.mute')}>
              {muted || volume === 0 ? <VolumeX className="w-5 h-5" /> : <Volume2 className="w-5 h-5" />}
            </button>
            <input
              type="range" min={0} max={1} step={0.05} value={muted ? 0 : volume}
              onChange={e => { setVolume(Number(e.target.value)); setMuted(false); }}
              className="ts-range w-20 hidden sm:block"
              aria-label={t('player.volume')}
              style={{ background: `linear-gradient(to right, #fff ${fill(muted ? 0 : volume, 1)}%, rgba(255,255,255,0.3) ${fill(muted ? 0 : volume, 1)}%)` }}
            />
            <span className="text-white/80 text-sm tabular-nums ml-1">
              {formatTime(shownTime)} / {formatTime(duration)}
            </span>
            <div className="flex-1" />
            {!!onNext && (
              <button onClick={() => onNext()} className={ctl} title={`${t('player.nextEpisode')} ${nextLabel} (N)`} aria-label={t('player.nextEpisode')}>
                <SkipForward className="w-5 h-5" />
              </button>
            )}
            <button
              ref={panelButtonRef}
              onClick={() => (panelOpen ? closePanel() : setPanelOpen(true))}
              className={`${ctl} ${panelOpen ? 'text-red-500' : ''}`}
              title={t('player.audioSubtitles')}
              aria-label={t('player.audioSubtitles')}
              aria-haspopup="dialog"
              aria-expanded={panelOpen}
            >
              <MessageSquare className="w-5 h-5" />
            </button>
            <button onClick={toggleFullscreen} className={ctl} title={t(fullscreen ? 'player.exitFullscreen' : 'player.fullscreen')} aria-label={t(fullscreen ? 'player.exitFullscreen' : 'player.fullscreen')}>
              {fullscreen ? <Minimize className="w-5 h-5" /> : <Maximize className="w-5 h-5" />}
            </button>
          </div>
        </div>
      )}

      {/* Touch: double-tap seek feedback on the tapped side */}
      {touchUi && !!seekFx && (
        <div
          className={`pointer-events-none absolute inset-y-0 z-[7] flex w-[38%] items-center justify-center overflow-hidden bg-white/10 ${seekFx.side < 0 ? 'left-0 rounded-r-[50%]' : 'right-0 rounded-l-[50%]'}`}
          role="status"
        >
          <span key={seekFx.n} className={mobileCss.ripple} />
          <div className="relative flex flex-col items-center text-white drop-shadow-[0_1px_3px_rgba(0,0,0,0.8)]">
            {seekFx.side < 0 ? <ChevronsLeft className="h-9 w-9" /> : <ChevronsRight className="h-9 w-9" />}
            <span className="text-sm font-semibold tabular-nums">
              {t('player.seekDelta', { delta: `${seekFx.total < 0 ? '−' : '+'}${Math.abs(seekFx.total)}` })}
            </span>
          </div>
        </div>
      )}

      {/* Touch: big center controls (the spinner takes Play's place while loading) */}
      {playable && touchUi && countdown === null && (
        <div className={`absolute inset-0 z-[6] flex items-center justify-center gap-10 pointer-events-none transition-opacity duration-200 ${mobileCss.center} ${centerOn ? 'opacity-100' : 'opacity-0'}`}>
          {[-10, 0, 10].map(step => {
            const live = centerOn ? 'pointer-events-auto' : '';
            if (step === 0) {
              return loadingShown || recovering ? <div key={step} className="h-[72px] w-[72px]" /> : (
                <button key={step} type="button" onClick={() => { togglePlay(); revealControls(); }} className={`${live} flex h-[72px] w-[72px] items-center justify-center rounded-full bg-black/40 text-white active:scale-95 transition-transform`} aria-label={t(playing ? 'player.pause' : 'player.play')}>
                  {playing ? <Pause className="h-10 w-10 fill-current" /> : <Play className="h-10 w-10 fill-current translate-x-0.5" />}
                </button>
              );
            }
            const Icon = step < 0 ? RotateCcw : RotateCw;
            return (
              <button key={step} type="button" onClick={() => { seekTo(position + step); revealControls(); }} className={`${live} relative flex h-14 w-14 items-center justify-center rounded-full bg-black/30 text-white active:scale-95 transition-transform`} aria-label={t(step < 0 ? 'player.back10Touch' : 'player.forward10Touch')}>
                <Icon className="h-10 w-10" strokeWidth={1.5} />
                <span className="absolute text-[11px] font-bold">10</span>
              </button>
            );
          })}
        </div>
      )}

      {/* Touch: bottom bar (seek bar with the time left, then panel / episode / screen buttons) */}
      {playable && touchUi && (
        <div ref={bottomBarRef} className={`absolute bottom-0 inset-x-0 z-[8] bg-gradient-to-t from-black/90 via-black/50 to-transparent transition-opacity duration-300 ${mobileCss.bottomBar} ${hiddenCls}`}>
          <div className="flex items-center gap-3">
            <SeekThumbnail apiUrl={apiUrl} hash={hash} index={file?.id} duration={duration} dragTime={seekPreview} position={position} playing={playing && !buffering} touch className="flex-1">
              <TouchSeekBar
                duration={duration}
                value={shownTime}
                label={t('player.seek')}
                onPreview={setSeekPreview}
                onCommit={time => { setSeekPreview(null); seekTo(time); }}
                onActivity={revealControls}
              />
            </SeekThumbnail>
            <span className="shrink-0 text-[13px] font-medium text-white/90 tabular-nums">
              {seekPreview !== null ? formatTime(shownTime) : `-${formatTime(Math.max(0, duration - shownTime))}`}
            </span>
          </div>
          <div className="flex items-center gap-1">
            <div className="flex-1" />
            {!!onNext && (
              <button type="button" onClick={() => onNext()} className={touchBtn} aria-label={t('player.nextEpisode')}>
                <SkipForward className="h-5 w-5" />
                <span className={`text-sm font-semibold ${mobileCss.wideOnly}`}>{t('player.nextEpisode')}</span>
              </button>
            )}
            <button
              ref={panelButtonRef}
              type="button"
              onClick={() => (panelOpen ? closePanel() : setPanelOpen(true))}
              className={`${touchBtn} ${panelOpen ? 'text-red-500' : ''}`}
              aria-label={t('player.audioSubtitles')}
              aria-haspopup="dialog"
              aria-expanded={panelOpen}
            >
              <MessageSquare className="h-5 w-5" />
              <span className={`text-sm font-semibold ${mobileCss.wideOnly}`}>{t('player.audioSubtitles')}</span>
            </button>
            {!!onRotate && (
              <button type="button" onClick={onRotate} className={touchBtn} aria-label={t('player.rotate')}>
                <RotateCwSquare className="h-5 w-5" />
              </button>
            )}
            {canFullscreen && (
              <button type="button" onClick={toggleFullscreen} className={touchBtn} aria-label={t(fullscreen ? 'player.exitFullscreenTouch' : 'player.fullscreenTouch')}>
                {fullscreen ? <Minimize className="h-5 w-5" /> : <Maximize className="h-5 w-5" />}
              </button>
            )}
          </div>
        </div>
      )}

      {playable && (
        <AudioSubtitlePanel
          sheet={touchUi}
          open={panelOpen}
          onClose={closePanel}
          anchorRef={panelButtonRef}
          audioTracks={audioTracks}
          audio={audio}
          onAudio={selectAudio}
          originalLang={meta?.original_language}
          subtitles={meta?.tmdb_id ? subs : []}
          subtitleId={subId}
          failedSubtitles={failedSubs}
          onSubtitle={chooseSubtitle}
          delay={subDelay}
          onDelay={setSubDelay}
          subAlt={subAlt}
        />
      )}
    </div>
  );
}

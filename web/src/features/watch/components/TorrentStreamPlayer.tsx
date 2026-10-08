"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Play, Pause, X, Maximize, Minimize, Volume2, VolumeX, Loader2, RotateCcw, RotateCw, Film, SkipForward, AlertCircle } from 'lucide-react';
import { getApiUrl } from '@/shared/lib/api';
import { getActiveProfileId } from '@/shared/lib/profile';
import {
  StreamMeta,
  StreamSource,
  SeasonInfo,
  resolveSource,
  findProgress,
  getTitleProgress,
  getTvSeasons,
  getSeasonEpisodes,
  saveProgress,
  nextEpisodeOf,
  isFinished,
  episodeLabel,
} from '@/features/watch/api';

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

const subtitleOf = (meta?: StreamMeta) => {
  if (!meta || meta.media_type !== 'tv') return '';
  return [episodeLabel(meta.season, meta.episode), meta.episode_title].filter(Boolean).join(' · ');
};

// Opens a title in the torrent player. With only `meta` it finds the best
// source itself (or reuses the saved one), then hands off to PlayerCore.
export default function TorrentStreamPlayer({ magnet, source, meta, title, startAt, seasons: seasonsProp, onClose }: TorrentStreamPlayerProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [item, setItem] = useState<PlayItem>({ meta, source, magnet, startAt });
  const [itemKey, setItemKey] = useState(0);
  const [attempt, setAttempt] = useState(0);
  const [phase, setPhase] = useState<'resolving' | 'ready' | 'error'>(source || magnet ? 'ready' : 'resolving');
  const [resolved, setResolved] = useState<{ source?: StreamSource; startAt: number }>({ source, startAt: startAt || 0 });
  const [resolveError, setResolveError] = useState('');
  const [slowResolve, setSlowResolve] = useState(false);
  const [seasons, setSeasons] = useState<SeasonInfo[]>(seasonsProp || []);
  const [fullscreen, setFullscreen] = useState(false);

  const isTv = item.meta?.media_type === 'tv';

  // Find the source for the current item
  useEffect(() => {
    if (item.source || item.magnet) {
      setResolved({ source: item.source, startAt: item.startAt || 0 });
      setPhase('ready');
      return;
    }
    const m = item.meta;
    if (!m) {
      setResolveError('Nothing to play');
      setPhase('error');
      return;
    }
    const ctrl = new AbortController();
    let cancelled = false;
    setPhase('resolving');
    setResolveError('');
    setSlowResolve(false);
    const slowTimer = setTimeout(() => setSlowResolve(true), 12000);
    (async () => {
      try {
        // Reuse the torrent saved with earlier progress instead of searching again
        const pid = getActiveProfileId();
        if (pid) {
          const entries = await getTitleProgress(pid, m.media_type, m.tmdb_id);
          if (cancelled) return;
          const saved = m.media_type === 'tv' ? findProgress(entries, m.season || 0, m.episode || 0) : findProgress(entries);
          if (saved?.hash) {
            const start = item.startAt ?? (isFinished(saved) ? 0 : Math.max(0, saved.position - 5));
            setResolved({ source: { hash: saved.hash, index: saved.file_index, magnet: saved.magnet || undefined }, startAt: start });
            setPhase('ready');
            return;
          }
        }
        const r = await resolveSource({
          type: m.media_type,
          title: m.title,
          year: m.year,
          season: m.season,
          episode: m.episode,
        }, ctrl.signal);
        if (cancelled) return;
        setResolved({ source: { hash: r.hash, index: r.index, magnet: r.magnet, file_name: r.file_name }, startAt: item.startAt || 0 });
        setPhase('ready');
      } catch (e: any) {
        if (cancelled || e?.name === 'AbortError') return;
        setResolveError(e?.status === 404 || !e?.message ? 'No playable source found' : e.message);
        setPhase('error');
      } finally {
        clearTimeout(slowTimer);
      }
    })();
    return () => {
      cancelled = true;
      clearTimeout(slowTimer);
      ctrl.abort();
    };
    // item is replaced together with itemKey
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [itemKey, attempt]);

  // TV: episode counts for "Next Episode"
  useEffect(() => {
    if (!isTv || seasons.length > 0 || !item.meta) return;
    let cancelled = false;
    getTvSeasons(item.meta.tmdb_id).then(s => { if (!cancelled) setSeasons(s); });
    return () => { cancelled = true; };
  }, [isTv, item.meta, seasons.length]);

  // TV: fill in the episode title when we only know the numbers
  useEffect(() => {
    const m = item.meta;
    if (!m || m.media_type !== 'tv' || m.episode_title || !m.season || !m.episode) return;
    const season = seasons.find(s => s.season_number === m.season);
    if (!season) return;
    let cancelled = false;
    getSeasonEpisodes(m.tmdb_id, season).then(eps => {
      const ep = eps.find(e => e.episode_number === m.episode);
      if (cancelled || !ep || (!ep.still_path && ep.name === `Episode ${ep.episode_number}`)) return;
      setItem(prev => (prev.meta === m ? { ...prev, meta: { ...m, episode_title: ep.name } } : prev));
    });
    return () => { cancelled = true; };
  }, [item.meta, seasons]);

  const next = useMemo(() => {
    const m = item.meta;
    return m?.media_type === 'tv' && m.season && m.episode ? nextEpisodeOf(seasons, m.season, m.episode) : null;
  }, [item.meta, seasons]);

  const playNext = useCallback(() => {
    if (!next || !item.meta) return;
    setItem({ meta: { ...item.meta, season: next.season, episode: next.episode, episode_title: undefined } });
    setItemKey(k => k + 1);
  }, [next, item.meta]);

  const toggleFullscreen = useCallback(() => {
    if (!document.fullscreenElement) containerRef.current?.requestFullscreen().catch(() => {});
    else document.exitFullscreen().catch(() => {});
  }, []);

  useEffect(() => {
    const onFs = () => setFullscreen(!!document.fullscreenElement);
    document.addEventListener('fullscreenchange', onFs);
    return () => document.removeEventListener('fullscreenchange', onFs);
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.fullscreenElement) onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const displayTitle = item.meta?.title || title || '';
  const subtitle = subtitleOf(item.meta);

  // Portal to body so transformed ancestors can't trap the fixed overlay
  return createPortal(
    <div ref={containerRef} className="fixed inset-0 z-[100] bg-black flex items-center justify-center select-none">
      {phase === 'ready' && (
        <PlayerCore
          key={itemKey}
          magnet={item.magnet}
          source={resolved.source}
          startAt={resolved.startAt}
          meta={item.meta}
          title={displayTitle}
          subtitle={subtitle}
          nextLabel={next ? `S${next.season}:E${next.episode}` : ''}
          onNext={next ? playNext : undefined}
          onClose={onClose}
          fullscreen={fullscreen}
          toggleFullscreen={toggleFullscreen}
        />
      )}

      {phase !== 'ready' && (
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
                <p className="text-white text-base">Finding the best source...</p>
                <p className="text-[#b3b3b3] text-sm mt-1 h-5">
                  {slowResolve ? 'Still searching, this can take up to a minute.' : ''}
                </p>
              </>
            ) : (
              <>
                <AlertCircle className="w-12 h-12 text-[#E50914] mb-4" />
                <p className="text-white text-lg font-semibold">We couldn&apos;t start this {isTv ? 'episode' : 'title'}</p>
                <p className="text-[#b3b3b3] text-sm mt-1 mb-6">
                  {resolveError === 'No playable source found'
                    ? 'No playable source was found right now. Try again in a moment.'
                    : resolveError}
                </p>
                <div className="flex gap-3">
                  <button
                    onClick={() => setAttempt(a => a + 1)}
                    className="flex items-center gap-2 px-6 py-2.5 bg-white text-black font-bold rounded hover:bg-white/80 transition"
                  >
                    <RotateCw className="w-4 h-4" /> Try Again
                  </button>
                  <button onClick={onClose} className="px-6 py-2.5 bg-[#6d6d6eb3] hover:bg-[#6d6d6e66] text-white font-bold rounded transition">
                    Close
                  </button>
                </div>
              </>
            )}
          </div>
          <button onClick={onClose} className="absolute top-4 right-4 md:top-6 md:right-6 z-10 p-2 rounded-full bg-white/10 hover:bg-white/20" title="Close (Esc)">
            <X className="w-5 h-5 text-white" />
          </button>
        </>
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
  onClose: () => void;
  fullscreen: boolean;
  toggleFullscreen: () => void;
}

// Plays a torrent while it downloads. The stream is a live fMP4, so seeking
// restarts it at the new position (?t=) and the shown time adds that offset.
function PlayerCore({ magnet, source, startAt, meta, title, subtitle, nextLabel, onNext, onClose, fullscreen, toggleFullscreen }: PlayerCoreProps) {
  const apiUrl = getApiUrl();
  const videoRef = useRef<HTMLVideoElement>(null);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const startRef = useRef(startAt);

  const [hash, setHash] = useState('');
  const [files, setFiles] = useState<StreamFile[]>([]);
  const [file, setFile] = useState<StreamFile | null>(null);
  const [duration, setDuration] = useState(0);
  const [audioTracks, setAudioTracks] = useState<AudioTrack[]>([]);
  const [audio, setAudio] = useState(0);
  const [offset, setOffset] = useState(0);
  const [current, setCurrent] = useState(0);
  const [seekPreview, setSeekPreview] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);
  const [buffering, setBuffering] = useState(true);
  const [status, setStatus] = useState('Connecting to peers...');
  const [error, setError] = useState('');
  const [muted, setMuted] = useState(false);
  const [volume, setVolume] = useState(1);
  const [showControls, setShowControls] = useState(true);
  const [ended, setEnded] = useState(false);
  const [countdown, setCountdown] = useState<number | null>(null);

  // 1) Resolve the torrent's video files
  useEffect(() => {
    let cancelled = false;
    (async () => {
      if (source) {
        // Make sure TorrServer has the torrent (it may have been dropped since it was saved)
        if (source.magnet) {
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
        if (!res.ok) throw new Error(data.error || 'Could not open torrent');
        if (!data.files?.length) throw new Error('No playable video file in this torrent');
        setHash(data.hash);
        setFiles(data.files);
        // A single file (or one much larger than the rest) is the movie itself
        const sorted = [...data.files].sort((a: StreamFile, b: StreamFile) => b.size - a.size);
        if (data.files.length === 1 || sorted[0].size > sorted[1].size * 4) setFile(sorted[0]);
      } catch (e: any) {
        if (!cancelled) setError(e.message || 'Could not open torrent');
      }
    })();
    return () => { cancelled = true; };
  }, [apiUrl, magnet, source]);

  // 2) Probe the chosen file for duration and audio tracks
  useEffect(() => {
    if (!file || !hash) return;
    let cancelled = false;
    setStatus('Reading video info...');
    setBuffering(true);
    setDuration(0);
    setAudioTracks([]);
    setAudio(0);
    setOffset(0);
    setCurrent(0);
    (async () => {
      try {
        const res = await fetch(`${apiUrl}/api/torrent-stream/probe?hash=${hash}&index=${file.id}`);
        const data = await res.json();
        if (cancelled) return;
        if (!res.ok) throw new Error(data.error || 'Could not read video');
        const dur = data.duration || 0;
        // Resume position only applies to the first file that gets opened
        const start = startRef.current;
        startRef.current = 0;
        setDuration(dur);
        setOffset(start > 0 && (dur <= 0 || start < dur - 10) ? start : 0);
        setAudioTracks(data.audio_tracks || []);
        setStatus('Buffering...');
      } catch (e: any) {
        if (!cancelled) setError(e.message || 'Could not read video');
      }
    })();
    return () => { cancelled = true; };
  }, [apiUrl, hash, file]);

  const src = file && hash && duration > 0
    ? `${apiUrl}/api/torrent-stream/play?hash=${hash}&index=${file.id}&t=${offset.toFixed(1)}&audio=${audio}`
    : '';

  // Restart the stream whenever the source changes (file, seek or audio track)
  useEffect(() => {
    const v = videoRef.current;
    if (!v || !src) return;
    setBuffering(true);
    setEnded(false);
    v.src = src;
    v.play().catch(() => setPlaying(false));
  }, [src]);

  const position = offset + current;

  // Latest values for progress saving from timers and unmount
  const stateRef = useRef({ position: 0, duration: 0, hash: '', index: 0 });
  stateRef.current = { position, duration, hash, index: file?.id ?? 0 };
  const metaRef = useRef(meta);
  metaRef.current = meta;
  const magnetRef = useRef(source?.magnet || magnet || '');

  const persist = useCallback((opts: { keepalive?: boolean; finished?: boolean } = {}) => {
    const m = metaRef.current;
    const pid = getActiveProfileId();
    const s = stateRef.current;
    if (!m || !pid || !s.hash || s.duration <= 0) return;
    const pos = opts.finished ? s.duration : s.position;
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
    const v = videoRef.current;
    return () => {
      window.removeEventListener('pagehide', onHide);
      persist({ keepalive: true });
      if (v) {
        v.pause();
        v.removeAttribute('src');
        v.load();
      }
    };
  }, [persist]);

  const seekTo = useCallback((t: number) => {
    const max = duration > 0 ? duration - 1 : 0;
    setOffset(Math.max(0, Math.min(max, t)));
    setCurrent(0);
  }, [duration]);

  const togglePlay = useCallback(() => {
    const v = videoRef.current;
    if (!v) return;
    if (v.paused) v.play().catch(() => {});
    else v.pause();
  }, []);

  const revealControls = useCallback(() => {
    setShowControls(true);
    if (hideTimer.current) clearTimeout(hideTimer.current);
    hideTimer.current = setTimeout(() => setShowControls(false), 3000);
  }, []);

  useEffect(() => () => { if (hideTimer.current) clearTimeout(hideTimer.current); }, []);

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

  // Auto-play the next episode a few seconds after this one ends
  useEffect(() => {
    if (countdown === null) return;
    if (countdown <= 0) { onNext?.(); return; }
    const t = setTimeout(() => setCountdown(c => (c === null ? null : c - 1)), 1000);
    return () => clearTimeout(t);
  }, [countdown, onNext]);

  const handleEnded = () => {
    setPlaying(false);
    setEnded(true);
    persist({ finished: true });
    if (onNext) setCountdown(5);
  };

  const trackLabel = (t: AudioTrack) => {
    const lang = t.language && t.language !== 'und' ? t.language.toUpperCase() : `Track ${t.index + 1}`;
    const extra = [t.title, t.channels > 2 ? `${t.channels}ch` : ''].filter(Boolean).join(' · ');
    return extra ? `${lang} (${extra})` : lang;
  };

  const shownTime = seekPreview ?? position;
  const remaining = duration - position;
  const showNextButton = !!onNext && duration > 0 && !error && countdown === null &&
    remaining <= Math.min(120, Math.max(40, duration * 0.04));

  return (
    <div
      className="absolute inset-0 flex items-center justify-center"
      onMouseMove={revealControls}
      style={{ cursor: showControls ? 'default' : 'none' }}
    >
      <video
        ref={videoRef}
        className="w-full h-full object-contain"
        playsInline
        onClick={togglePlay}
        onPlay={() => setPlaying(true)}
        onPause={() => { setPlaying(false); persist(); }}
        onWaiting={() => setBuffering(true)}
        onPlaying={() => setBuffering(false)}
        onTimeUpdate={e => setCurrent((e.target as HTMLVideoElement).currentTime)}
        onEnded={handleEnded}
        onError={() => { if (src) setError('Playback failed. The torrent may not have enough peers.'); }}
      />

      {/* Episode / file picker (legacy magnet mode) */}
      {!file && files.length > 1 && !error && (
        <div className="absolute inset-0 overflow-y-auto bg-black/95 p-6 md:p-12">
          <h2 className="text-white text-xl md:text-2xl font-bold mb-1">{title}</h2>
          <p className="text-white/50 text-sm mb-6">Choose a file to watch</p>
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
      {(error || (buffering && !ended && (file || files.length <= 1))) && (
        <div className="absolute inset-0 flex flex-col items-center justify-center pointer-events-none">
          {error ? (
            <div className="pointer-events-auto text-center px-6">
              <p className="text-red-400 text-lg mb-4">{error}</p>
              <button onClick={onClose} className="px-5 py-2 bg-white/10 hover:bg-white/20 text-white rounded-lg">Close</button>
            </div>
          ) : (
            <>
              <Loader2 className="w-12 h-12 text-red-600 animate-spin mb-3" />
              <p className="text-white/70 text-sm">{duration > 0 ? 'Buffering...' : status}</p>
            </>
          )}
        </div>
      )}

      {/* Up next countdown */}
      {countdown !== null && !!onNext && (
        <div className="absolute inset-0 flex flex-col items-center justify-center bg-black/70">
          <p className="text-[#b3b3b3] text-sm uppercase tracking-widest mb-2">Next episode</p>
          <p className="text-white text-2xl font-bold mb-6">{nextLabel} starts in {Math.max(0, countdown)}</p>
          <div className="flex gap-3">
            <button
              onClick={() => onNext()}
              className="flex items-center gap-2 px-6 py-2.5 bg-white text-black font-bold rounded hover:bg-white/80 transition"
            >
              <Play className="w-5 h-5 fill-current" /> Play Now
            </button>
            <button
              onClick={() => setCountdown(null)}
              className="px-6 py-2.5 bg-[#6d6d6eb3] hover:bg-[#6d6d6e66] text-white font-bold rounded transition"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {/* Top bar */}
      <div className={`absolute top-0 inset-x-0 p-4 md:p-6 bg-gradient-to-b from-black/80 to-transparent flex items-center gap-4 transition-opacity duration-300 ${showControls ? 'opacity-100' : 'opacity-0'}`}>
        <div className="flex-1 min-w-0">
          <p className="text-white font-semibold truncate">{title}</p>
          {subtitle ? (
            <p className="text-white/60 text-xs truncate">{subtitle}</p>
          ) : (
            !!file?.name && <p className="text-white/50 text-xs truncate">{file.name}</p>
          )}
        </div>
        {files.length > 1 && !!file && (
          <button onClick={() => { setFile(null); videoRef.current?.pause(); }} className="text-white/70 hover:text-white text-sm px-3 py-1.5 rounded bg-white/10">
            Episodes
          </button>
        )}
        <button onClick={onClose} className="p-2 rounded-full bg-white/10 hover:bg-white/20" title="Close (Esc)">
          <X className="w-5 h-5 text-white" />
        </button>
      </div>

      {/* Next episode button near the end */}
      {showNextButton && (
        <button
          onClick={() => onNext?.()}
          className="absolute right-4 md:right-8 bottom-28 flex items-center gap-2 px-5 py-2.5 bg-white text-black font-bold rounded shadow-lg hover:bg-white/80 transition"
        >
          <SkipForward className="w-5 h-5 fill-current" /> Next Episode
        </button>
      )}

      {/* Bottom controls */}
      {!!file && duration > 0 && !error && (
        <div className={`absolute bottom-0 inset-x-0 px-4 md:px-6 pb-4 pt-10 bg-gradient-to-t from-black/90 to-transparent transition-opacity duration-300 ${showControls ? 'opacity-100' : 'opacity-0'}`}>
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
            style={{ background: `linear-gradient(to right, #E50914 ${fill(shownTime, duration)}%, rgba(255,255,255,0.3) ${fill(shownTime, duration)}%)` }}
          />
          <div className="flex items-center gap-3 mt-2">
            <button onClick={togglePlay} className="p-2 text-white hover:text-red-500" title="Play/Pause (Space)">
              {playing ? <Pause className="w-6 h-6" /> : <Play className="w-6 h-6" />}
            </button>
            <button onClick={() => seekTo(position - 10)} className="p-2 text-white hover:text-red-500" title="Back 10s (←)">
              <RotateCcw className="w-5 h-5" />
            </button>
            <button onClick={() => seekTo(position + 10)} className="p-2 text-white hover:text-red-500" title="Forward 10s (→)">
              <RotateCw className="w-5 h-5" />
            </button>
            <button onClick={() => setMuted(m => !m)} className="p-2 text-white hover:text-red-500" title="Mute (M)">
              {muted || volume === 0 ? <VolumeX className="w-5 h-5" /> : <Volume2 className="w-5 h-5" />}
            </button>
            <input
              type="range" min={0} max={1} step={0.05} value={muted ? 0 : volume}
              onChange={e => { setVolume(Number(e.target.value)); setMuted(false); }}
              className="ts-range w-20 hidden sm:block"
              style={{ background: `linear-gradient(to right, #fff ${fill(muted ? 0 : volume, 1)}%, rgba(255,255,255,0.3) ${fill(muted ? 0 : volume, 1)}%)` }}
            />
            <span className="text-white/80 text-sm tabular-nums ml-1">
              {formatTime(shownTime)} / {formatTime(duration)}
            </span>
            <div className="flex-1" />
            {!!onNext && (
              <button onClick={() => onNext()} className="p-2 text-white hover:text-red-500" title={`Next episode ${nextLabel} (N)`}>
                <SkipForward className="w-5 h-5" />
              </button>
            )}
            {audioTracks.length > 1 && (
              <select
                value={audio}
                onChange={e => { setAudio(Number(e.target.value)); setOffset(position); setCurrent(0); }}
                className="bg-white/10 text-white text-sm rounded px-2 py-1.5 border border-white/20"
                title="Audio track"
              >
                {audioTracks.map(t => (
                  <option key={t.index} value={t.index} className="bg-black">{trackLabel(t)}</option>
                ))}
              </select>
            )}
            <button onClick={toggleFullscreen} className="p-2 text-white hover:text-red-500" title="Fullscreen (F)">
              {fullscreen ? <Minimize className="w-5 h-5" /> : <Maximize className="w-5 h-5" />}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

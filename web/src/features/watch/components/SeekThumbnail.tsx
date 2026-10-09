'use client';

// Thumbnail above the seek bar on hover / drag (GET /api/torrent-stream/thumb, the frame at or before a
// time on the server's 10 s grid). Requests are snapped to that grid, never aborted (the server would
// stop making the frame and nothing would be cached), at most MAX_INFLIGHT at a time; while the cursor
// moves only the newest waiting one is kept. Frames stay in memory as object URLs; until the hovered
// one arrives the nearest loaded frame shows dimmed (or a spinner).
import React, { useEffect, useReducer, useRef, useState } from 'react';
import { Loader2 } from 'lucide-react';

const GRID = 10;
const WIDTH = 320;
const MAX_CACHED = 120;
const DEBOUNCE_MS = 80;
const MAX_INFLIGHT = 3;
// A nearby frame stands in while the hovered one loads
const NEAR_S = 60;
// While playing, the frames around the playhead are made in the background
const PREFETCH_EVERY_MS = 20000;
const PREFETCH_AROUND_S = 30;

const frames = new Map<string, string>(); // insertion order = least recently used first
const inflight = new Set<string>();
// Frames the server has none for (404, errors): no spinner, not asked again this session
const missing = new Set<string>();
let waiting: { key: string; url: string } | null = null;
// The frame the viewer looks at now: only it is asked again after a 204 / 504
let wanted = '';
const listeners = new Set<() => void>();

function remember(key: string, url: string) {
  frames.set(key, url);
  while (frames.size > MAX_CACHED) {
    const oldest = frames.keys().next().value as string;
    URL.revokeObjectURL(frames.get(oldest)!);
    frames.delete(oldest);
  }
}

function request(key: string, url: string) {
  if (frames.has(key) || inflight.has(key) || missing.has(key)) return;
  if (inflight.size >= MAX_INFLIGHT) {
    waiting = { key, url };
    return;
  }
  inflight.add(key);
  fetch(url)
    .then(async res => {
      if (res.status === 200) {
        const blob = await res.blob();
        if (blob.size) remember(key, URL.createObjectURL(blob));
        return;
      }
      // 204: the stream is starting, 504: the frame is still being made (the retry is usually a hit)
      if ((res.status === 204 || res.status === 504) && key === wanted) {
        const after = Math.min(5, Math.max(1, Number(res.headers.get('Retry-After')) || 2));
        setTimeout(() => { if (key === wanted) request(key, url); }, after * 1000);
        return;
      }
      if (res.status === 404 || res.status === 400) missing.add(key);
    })
    .catch(() => {})
    .finally(() => {
      inflight.delete(key);
      listeners.forEach(l => l());
      const next = waiting;
      waiting = null;
      if (next) request(next.key, next.url);
    });
}

/** The loaded frame of this file closest to grid (within NEAR_S), for while the right one loads. */
function nearestFrame(prefix: string, grid: number): string | null {
  let best: string | null = null;
  let bestD = NEAR_S + 1;
  frames.forEach((url, key) => {
    if (!key.startsWith(prefix)) return;
    const d = Math.abs(Number(key.slice(prefix.length)) - grid);
    if (d < bestD) {
      bestD = d;
      best = url;
    }
  });
  return best;
}

function formatTime(s: number) {
  if (!isFinite(s) || s < 0) s = 0;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  return `${h > 0 ? `${h}:${String(m).padStart(2, '0')}` : m}:${String(sec).padStart(2, '0')}`;
}

interface Props {
  apiUrl: string;
  hash: string;
  index: number | undefined;
  duration: number;
  /** Time the viewer drags the seek bar to (null while not dragging) */
  dragTime: number | null;
  /** Playback position and state: the frames around it are prefetched while it plays */
  position?: number;
  playing?: boolean;
  /** Touch layout: no hover, a smaller frame held higher (above the finger) */
  touch?: boolean;
  className?: string;
  children: React.ReactNode;
}

/** Wraps the seek bar and shows the frame at the hovered or dragged time above it. */
export default function SeekThumbnail({ apiUrl, hash, index, duration, dragTime, position, playing, touch = false, className = '', children }: Props) {
  const box = useRef<HTMLDivElement>(null);
  const [hoverT, setHoverT] = useState<number | null>(null);
  // Re-render when any frame request ends (the hovered one, or a nearer stand-in)
  const [, bump] = useReducer((n: number) => n + 1, 0);
  useEffect(() => {
    listeners.add(bump);
    return () => { listeners.delete(bump); };
  }, []);
  const t = dragTime ?? hoverT;
  const grid = t === null ? null : Math.max(0, Math.floor(t / GRID) * GRID);
  const ready = !!hash && index !== undefined && duration > 0;
  const prefix = `${hash}:${index}:`;

  useEffect(() => {
    if (!ready || grid === null) {
      wanted = '';
      return;
    }
    const key = prefix + grid;
    wanted = key;
    const hit = frames.get(key);
    if (hit) {
      frames.delete(key);
      frames.set(key, hit);
      return;
    }
    const timer = setTimeout(() => request(key, `${apiUrl}/api/torrent-stream/thumb?hash=${hash}&index=${index}&t=${grid}&w=${WIDTH}`), DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [apiUrl, hash, index, grid, ready, prefix]);

  // Frames around the playhead, made by the server while the stream runs steadily
  const lastPrefetch = useRef(0);
  const prefetchSlot = position === undefined ? -1 : Math.floor(position / GRID);
  useEffect(() => {
    if (!ready || !playing || position === undefined || Date.now() - lastPrefetch.current < PREFETCH_EVERY_MS) return;
    lastPrefetch.current = Date.now();
    const times: number[] = [];
    const from = Math.max(0, Math.floor((position - PREFETCH_AROUND_S) / GRID) * GRID);
    for (let g = from; g <= position + PREFETCH_AROUND_S && g < duration && times.length < 12; g += GRID) {
      if (!frames.has(prefix + g)) times.push(g);
    }
    if (times.length === 0) return;
    fetch(`${apiUrl}/api/torrent-stream/thumb/prefetch?hash=${hash}&index=${index}&t=${times.join(',')}&w=${WIDTH}`, { method: 'POST' })
      .catch(() => {});
    // position is read through prefetchSlot: once per grid cell at most
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiUrl, hash, index, ready, playing, prefetchSlot, duration, prefix]);

  const width = box.current?.clientWidth || 0;
  // Half the frame plus a margin: the frame never leaves the bar
  const half = touch ? 92 : 110;
  const x = t === null || duration <= 0 ? 0 : Math.max(half, Math.min(width - half, (t / duration) * width));
  const exact = grid === null ? null : frames.get(prefix + grid) ?? null;
  const shown = exact ?? (grid === null ? null : nearestFrame(prefix, grid));

  return (
    <div
      ref={box}
      className={`relative ${className}`}
      onMouseMove={touch ? undefined : e => {
        const r = e.currentTarget.getBoundingClientRect();
        if (r.width > 0 && duration > 0) setHoverT(Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)) * duration);
      }}
      onMouseLeave={touch ? undefined : () => setHoverT(null)}
    >
      {children}
      {ready && t !== null && width > 0 && (
        <div className={`absolute bottom-full -translate-x-1/2 pointer-events-none ${touch ? 'mb-5' : 'mb-3'}`} style={{ left: x }} aria-hidden="true">
          <div className={`relative ${touch ? 'w-[164px]' : 'w-[200px]'} aspect-video rounded-md overflow-hidden border-2 border-white/90 bg-neutral-800 shadow-lg shadow-black/60`}>
            {!!shown && (
              <img src={shown} alt="" className={`w-full h-full object-contain bg-black transition-opacity duration-100 ${exact ? 'opacity-100' : 'opacity-50'}`} />
            )}
            {!exact && grid !== null && !missing.has(prefix + grid) && (
              <span className="absolute inset-0 flex items-center justify-center">
                <Loader2 className="h-6 w-6 animate-spin text-white/80" />
              </span>
            )}
          </div>
          <div className="mt-1 text-center text-sm font-semibold text-white tabular-nums drop-shadow-[0_1px_3px_rgba(0,0,0,0.9)]">{formatTime(t)}</div>
        </div>
      )}
    </div>
  );
}

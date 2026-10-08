import { useEffect, useReducer, useRef } from 'react';
import { THUMB_GRID, ThumbLoader, gridT, prefetchThumbs, thumbUrl } from './thumbs';

const SLOTS = [-2, -1, 0, 1, 2];
// Frame heights (rem) of the center, near and far slots; widths follow the video's aspect ratio
const HEIGHTS = [18, 11.8, 9];
const PREFETCH_MS = 20000;
const PREFETCH_BEHIND = 3; // grid steps
const PREFETCH_AHEAD = 3;

function formatTime(s: number): string {
  if (!isFinite(s) || s < 0) s = 0;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  return `${h > 0 ? `${h}:` : ''}${h > 0 ? String(m).padStart(2, '0') : m}:${String(sec).padStart(2, '0')}`;
}

/**
 * Netflix-style scrub strip: the frame at the target time large in the middle, the frames one and two
 * steps away (the current scrub step) smaller at the sides, the target time under it. Frames that are
 * not loaded yet show their time; seeking never waits for them.
 */
export default function TrickPlay({ hash, index, target, duration, aspect }: {
  hash: string; index: number; target: number; duration: number; aspect: number;
}) {
  const [, redraw] = useReducer((n: number) => n + 1, 0);
  const loader = useRef<ThumbLoader | null>(null);
  const last = useRef(target);
  const step = useRef(THUMB_GRID);
  const dir = useRef(1);

  if (target !== last.current) {
    const d = target - last.current;
    // Steps are 10, 30 or 60 s; a step shortened at either end of the file keeps the last one
    if (Math.abs(d) >= THUMB_GRID) step.current = Math.round(Math.abs(d) / THUMB_GRID) * THUMB_GRID;
    dir.current = d > 0 ? 1 : -1;
    last.current = target;
  }

  useEffect(() => {
    const l = new ThumbLoader(hash, index, redraw);
    loader.current = l;
    return () => {
      l.destroy();
      loader.current = null;
    };
  }, [hash, index]);

  const times = SLOTS.map(o => target + o * step.current);
  const inside = (t: number) => t >= 0 && t <= duration;
  const ratio = Math.max(4 / 3, Math.min(2.2, aspect || 16 / 9));

  useEffect(() => {
    // The center first, then the next frame in the scrub direction, then the rest
    const d = dir.current;
    const order = [0, d, -d, 2 * d, -2 * d].map(o => target + o * step.current).filter(inside);
    loader.current?.want(order);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target, hash, index, duration]);

  return (
    <div className="trickplay" aria-hidden="true">
      {SLOTS.map((o, i) => {
        const t = times[i];
        const ok = inside(t);
        const url = ok ? thumbUrl(hash, index, t) : undefined;
        const cls = o === 0 ? 'is-center' : Math.abs(o) === 1 ? 'is-near' : 'is-far';
        return (
          <div key={o} className={`trickplay-slot ${cls} ${ok ? '' : 'is-empty'}`}>
            <div className="trickplay-frame" style={{ width: `${HEIGHTS[Math.abs(o)] * ratio}rem` }}>
              {ok && <span className="trickplay-placeholder">{formatTime(t)}</span>}
              {!!url && <img className="trickplay-img" src={url} alt="" />}
            </div>
            {o === 0 && <div className="trickplay-time">{formatTime(target)}</div>}
          </div>
        );
      })}
    </div>
  );
}

/**
 * While the stream plays, asks the server every 20 s to make the frames around the position in the
 * background: behind it (still in TorrServer's cache) and ahead inside what the TV has buffered.
 */
export function useTrickPlayPrefetch(
  file: () => { hash: string; index: number } | null,
  video: () => HTMLVideoElement | null,
  position: () => number,
  duration: number,
  playing: boolean,
) {
  const lastAt = useRef('');
  useEffect(() => {
    if (!playing || duration <= 0) return;
    const tick = () => {
      const f = file();
      const v = video();
      if (!f || !v) return;
      const pos = position();
      let ahead = 0;
      const b = v.buffered;
      for (let i = 0; i < b.length; i++) {
        if (b.start(i) <= v.currentTime + 0.5 && b.end(i) >= v.currentTime) ahead = b.end(i) - v.currentTime;
      }
      const g = gridT(pos);
      const key = `${f.hash}:${f.index}:${g}:${Math.floor(ahead / THUMB_GRID)}`;
      if (key === lastAt.current) return;
      lastAt.current = key;
      const times: number[] = [];
      for (let s = -PREFETCH_BEHIND; s <= PREFETCH_AHEAD; s++) {
        const t = g + s * THUMB_GRID;
        if (t >= 0 && t <= duration && t <= pos + ahead) times.push(t);
      }
      prefetchThumbs(f.hash, f.index, times);
    };
    const first = setTimeout(tick, 5000);
    const every = setInterval(tick, PREFETCH_MS);
    return () => {
      clearTimeout(first);
      clearInterval(every);
    };
    // the getters read refs
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playing, duration]);
}

// Trick play thumbnails (GET /api/torrent-stream/thumb): small JPEGs of the keyframe at or before a time,
// on the server's 10 s grid. Loaded through fetch so 204 / 504 (swarm busy or slow) can be retried
// later, kept in memory as object URLs.
import { base } from '../lib/http';

export const THUMB_GRID = 10;
export const THUMB_W = 480;
const MAX_CACHED = 150;
const PARALLEL = 2;
const RETRY_MS = 2000;
const GIVE_UP_MS = 60000;

export const gridT = (t: number) => Math.max(0, Math.floor(t / THUMB_GRID) * THUMB_GRID);

const keyOf = (hash: string, index: number, t: number) => `${hash}:${index}:${gridT(t)}`;
const urls = new Map<string, string>(); // insertion order = least recently used first
const blocked = new Map<string, number>(); // key -> time before which it is not asked again

function remember(key: string, url: string) {
  urls.set(key, url);
  while (urls.size > MAX_CACHED) {
    const oldest = urls.keys().next().value as string;
    URL.revokeObjectURL(urls.get(oldest)!);
    urls.delete(oldest);
  }
}

export function thumbUrl(hash: string, index: number, t: number): string | undefined {
  const k = keyOf(hash, index, t);
  const u = urls.get(k);
  if (u) {
    urls.delete(k);
    urls.set(k, u);
  }
  return u;
}

/** Loads the wanted frames of one file, in order, two at a time. */
export class ThumbLoader {
  private wanted: number[] = [];
  private inflight = new Map<number, AbortController>();
  private retry?: ReturnType<typeof setTimeout>;
  private dead = false;

  constructor(private hash: string, private index: number, private onLoad: () => void) {}

  /** Grid times to load, most important first. Requests already running are kept: their frames are cached either way. */
  want(times: number[]) {
    const seen: Record<number, true> = {};
    this.wanted = times.map(gridT).filter(t => (seen[t] ? false : (seen[t] = true)));
    this.pump();
  }

  destroy() {
    this.dead = true;
    clearTimeout(this.retry);
    this.inflight.forEach(c => c.abort());
    this.inflight.clear();
  }

  private pump() {
    if (this.dead) return;
    const now = Date.now();
    let wake = 0;
    for (const t of this.wanted) {
      if (this.inflight.size >= PARALLEL) break;
      const k = keyOf(this.hash, this.index, t);
      if (urls.has(k) || this.inflight.has(t)) continue;
      const until = blocked.get(k) || 0;
      if (until > now) {
        wake = wake ? Math.min(wake, until) : until;
        continue;
      }
      this.load(t, k);
    }
    clearTimeout(this.retry);
    if (wake) this.retry = setTimeout(() => this.pump(), Math.max(200, wake - now));
  }

  private load(t: number, k: string) {
    const ctrl = new AbortController();
    this.inflight.set(t, ctrl);
    fetch(`${base()}/api/torrent-stream/thumb?hash=${this.hash}&index=${this.index}&t=${t}&w=${THUMB_W}`, { signal: ctrl.signal })
      .then(res => {
        if (res.status === 200) return res.blob();
        // 204: the stream starts or another torrent plays; 504: the swarm is slow. Others will not change soon.
        const after = Number(res.headers.get('Retry-After')) * 1000 || RETRY_MS;
        blocked.set(k, Date.now() + (res.status === 204 || res.status === 504 ? after : GIVE_UP_MS));
        return null;
      })
      .then(blob => {
        if (blob && blob.size > 0) {
          remember(k, URL.createObjectURL(blob));
          if (!this.dead) this.onLoad();
        }
      })
      .catch(() => {
        if (!ctrl.signal.aborted) blocked.set(k, Date.now() + RETRY_MS);
      })
      .then(() => {
        this.inflight.delete(t);
        this.pump();
      });
  }
}

/** Asks the server to make the frames around the position in the background (only while the stream runs steadily). */
export function prefetchThumbs(hash: string, index: number, times: number[]) {
  const ts = times.map(gridT).filter(t => !urls.has(keyOf(hash, index, t)));
  if (!ts.length) return;
  fetch(`${base()}/api/torrent-stream/thumb/prefetch?hash=${hash}&index=${index}&t=${ts.join(',')}&w=${THUMB_W}`, { method: 'POST' })
    .catch(() => {});
}

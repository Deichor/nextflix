// Puts a play stream on a <video>. Safari (every iOS browser too) only plays a URL whose server
// answers byte-range requests, which a live fragmented MP4 stream can't: there the stream is
// fetched here and fed through (Managed)MediaSource. Other browsers get the URL as src.

type MSCtor = typeof MediaSource;

// iOS evicts buffered media under memory pressure, also ahead of playback: keep the buffer small.
// WebKit downloads on while reading waits, so the server paces the stream (pace=1: playback speed
// after a 30 s burst) and these limits are only a guard.
const MAX_AHEAD_S = 45;
// While ManagedMediaSource says it has enough (not streaming), only this much is kept ahead
const MIN_AHEAD_S = 10;
const BACK_KEEP_S = 10;
// A paced stream keeps coming while the video is paused: after this long the feed stops
const PAUSE_STOP_MS = 20000;
const APPEND_MAX = 4 << 20;

const feeders = new WeakMap<HTMLVideoElement, () => void>();
// Fed elements whose reading pauses because enough is buffered
const holding = new WeakSet<HTMLVideoElement>();
// Fed elements whose feed stopped during a pause: playing on needs a new stream at the position
const pausedOut = new WeakSet<HTMLVideoElement>();

/** The feed stopped during a long pause (restart the stream at the position to play on) */
export function fedStoppedByPause(el: HTMLVideoElement): boolean {
  return pausedOut.has(el);
}

type FeedNote = (k: string, x?: Record<string, number | string | boolean | undefined>) => void;
let feedNote: FeedNote | null = null;

/** Where feed events go (the player's playback log) */
export function setFeedNote(fn: FeedNote | null): void {
  feedNote = fn;
}

/** For a fed element: whether it downloads now (false while it holds a full buffer); null for a plain src */
export function fedFetching(el: HTMLVideoElement): boolean | null {
  return feeders.has(el) ? !holding.has(el) : null;
}

function mediaSourceCtor(): MSCtor | null {
  if (typeof window === 'undefined') return null;
  const w = window as Window & { ManagedMediaSource?: MSCtor; MediaSource?: MSCtor };
  return w.ManagedMediaSource ?? w.MediaSource ?? null;
}

let fed: boolean | undefined;

/** WebKit-only browsers (Safari, any browser on iOS) that have a MediaSource */
export function usesFedStream(): boolean {
  if (fed === undefined) {
    const ua = typeof navigator !== 'undefined' ? navigator.userAgent : '';
    const webkitOnly = /AppleWebKit/.test(ua) && !/Chrome|Chromium|Android|Firefox/.test(ua);
    fed = webkitOnly && !!mediaSourceCtor();
  }
  return fed;
}

export function attachStream(el: HTMLVideoElement, url: string): void {
  feeders.get(el)?.();
  feeders.delete(el);
  pausedOut.delete(el);
  const MS = usesFedStream() ? mediaSourceCtor() : null;
  if (!MS) {
    el.src = url;
    return;
  }
  url += '&pace=1';
  const ms = new MS();
  const ctrl = new AbortController();
  const objectUrl = URL.createObjectURL(ms);
  feeders.set(el, () => {
    ctrl.abort();
    URL.revokeObjectURL(objectUrl);
  });
  // ManagedMediaSource plays only with AirPlay off or an AirPlay source next to it
  el.disableRemotePlayback = true;
  ms.addEventListener('sourceopen', () => {
    feed(el, ms, url, ctrl.signal).catch(e => {
      if (ctrl.signal.aborted) return;
      feedNote?.('feed-error', { msg: String((e as Error)?.name === 'Error' ? (e as Error).message : (e as Error)?.name) });
      // Fires the element's error event, as a broken progressive download would
      try { if (ms.readyState === 'open') ms.endOfStream('network'); } catch { /* closed meanwhile */ }
    });
  }, { once: true });
  el.src = objectUrl;
}

export function detachStream(el: HTMLVideoElement): void {
  feeders.get(el)?.();
  feeders.delete(el);
  pausedOut.delete(el);
  if (el.getAttribute('src')) {
    el.removeAttribute('src');
    el.load();
  }
}

// Sample entry four-character codes in the init segment -> codec strings for addSourceBuffer
const CODECS: [string, string][] = [
  ['avc1', 'avc1.640028'], ['avc3', 'avc1.640028'], ['hvc1', 'hvc1.1.6.L153.B0'], ['hev1', 'hvc1.1.6.L153.B0'],
  ['mp4a', 'mp4a.40.2'], ['ac-3', 'ac-3'], ['ec-3', 'ec-3'], ['fLaC', 'flac'], ['Opus', 'opus'],
];

function codecsOf(init: Uint8Array): string[] {
  const text = new TextDecoder('latin1').decode(init);
  return CODECS.filter(([cc]) => text.includes(cc)).map(([, c]) => c)
    .filter((c, i, a) => a.indexOf(c) === i);
}

// Bytes up to the end of the moov box, or 0 while it is incomplete
function initEnd(b: Uint8Array): number {
  let at = 0;
  while (at + 8 <= b.length) {
    const size = ((b[at] << 24) | (b[at + 1] << 16) | (b[at + 2] << 8) | b[at + 3]) >>> 0;
    const type = String.fromCharCode(b[at + 4], b[at + 5], b[at + 6], b[at + 7]);
    if (size < 8) return 0;
    if (type === 'moov') return at + size <= b.length ? at + size : 0;
    at += size;
  }
  return 0;
}

function concat(parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));

function aheadOf(el: HTMLVideoElement): number {
  const b = el.buffered;
  return b.length > 0 ? b.end(b.length - 1) - el.currentTime : 0;
}

// The data at the playback position ends before what was appended: the browser evicted it
function evicted(el: HTMLVideoElement, fedEnd: number): boolean {
  if (el.seeking || fedEnd === 0) return false;
  const ct = el.currentTime;
  const b = el.buffered;
  for (let i = 0; i < b.length; i++) {
    if (b.start(i) - 0.5 > ct || ct > b.end(i) + 0.5) continue;
    // Tiny gaps between ranges (audio and video ending apart) play through
    let end = b.end(i);
    while (i + 1 < b.length && b.start(i + 1) - end < 0.5) end = b.end(++i);
    return end < fedEnd - 1;
  }
  return b.length > 0 && ct >= b.start(0) && ct < fedEnd - 1;
}

async function feed(el: HTMLVideoElement, ms: MediaSource, url: string, signal: AbortSignal) {
  const res = await fetch(url, { signal });
  if (!res.ok || !res.body) throw new Error(`play ${res.status}`);
  const reader = res.body.getReader();
  signal.addEventListener('abort', () => { reader.cancel().catch(() => {}); });

  let sb: SourceBuffer | null = null;
  let head: Uint8Array[] = [];
  const queue: Uint8Array[] = [];
  let queued = 0;
  const managed = 'streaming' in ms;

  // A live stream can't fetch evicted data again: the stream fails right away (the player restarts
  // at the position) instead of stalling at the hole
  let fedEnd = 0;
  let lost = false;
  // Diagnostics: where the feed waits when no data came for a while
  let phase = 'read';
  let bytes = 0;
  let lastData = Date.now();
  let stuck = false;
  let played = false;
  let pausedSince = 0;
  let pauseStop = false;
  const watch = setInterval(() => {
    if (!el.paused) played = true;
    // Only the viewer's pause counts (a start waiting for a tap keeps its stream)
    pausedSince = played && el.paused && !el.ended ? pausedSince || Date.now() : 0;
    if (!pauseStop && pausedSince && Date.now() - pausedSince > PAUSE_STOP_MS) {
      pauseStop = true;
      pausedOut.add(el);
      feedNote?.('feed-paused-out', { ahead: Math.round(aheadOf(el)) });
      reader.cancel().catch(() => {});
      return;
    }
    if (!stuck && !pausedSince && !holding.has(el) && Date.now() - lastData > 5000) {
      stuck = true;
      feedNote?.('feed-stuck', {
        phase, mb: Math.round(bytes / 1e5) / 10, ahead: Math.round(aheadOf(el)), rs: ms.readyState,
        streaming: managed ? (ms as MediaSource & { streaming: boolean }).streaming : undefined, upd: sb?.updating,
      });
    }
    if (lost || !evicted(el, fedEnd)) return;
    feedNote?.('feed-evicted', { ahead: Math.round(aheadOf(el)), fed: Math.round(fedEnd) });
    lost = true;
    reader.cancel().catch(() => {});
  }, 1000);
  signal.addEventListener('abort', () => clearInterval(watch));

  const append = (data: Uint8Array) => new Promise<void>((resolve, reject) => {
    const s = sb!;
    const done = () => { s.removeEventListener('updateend', done); s.removeEventListener('error', fail); resolve(); };
    const fail = () => { s.removeEventListener('updateend', done); s.removeEventListener('error', fail); reject(new Error('append')); };
    s.addEventListener('updateend', done);
    s.addEventListener('error', fail);
    s.appendBuffer(data as BufferSource);
  });
  const remove = (from: number, to: number) => new Promise<void>(resolve => {
    const s = sb!;
    s.addEventListener('updateend', () => resolve(), { once: true });
    s.remove(from, to);
  });

  // Appends what arrived; a full buffer drops what was already watched and waits for playback
  const flush = async () => {
    while (queued > 0 && !signal.aborted && !lost) {
      // In slices: a large read (WebKit hands over everything it downloaded meanwhile) would not fit at once
      const all = concat(queue.splice(0));
      const data = all.subarray(0, APPEND_MAX);
      if (all.length > APPEND_MAX) queue.push(all.subarray(APPEND_MAX));
      queued = all.length - data.length;
      // What was watched goes before it counts against the memory the browser allows
      const back = el.currentTime - BACK_KEEP_S;
      if (el.buffered.length > 0 && el.buffered.start(0) < back - 5) await remove(0, back);
      for (let tries = 0; ; tries++) {
        try {
          phase = 'append';
          await append(data);
          break;
        } catch (e) {
          if ((e as DOMException)?.name !== 'QuotaExceededError') throw e;
          if (tries === 0) feedNote?.('feed-quota', { ahead: Math.round(aheadOf(el)), mb: Math.round(all.length / 1e5) / 10 });
          phase = 'quota';
          const back = el.currentTime - BACK_KEEP_S;
          if (back > 0) await remove(0, back);
          await sleep(1000);
          if (signal.aborted || lost) return;
        }
      }
      const b = el.buffered;
      if (b.length > 0) fedEnd = Math.max(fedEnd, b.end(b.length - 1));
      // The player watches 'progress' for arriving data, as with a progressive download
      el.dispatchEvent(new Event('progress'));
      // Playback waits at 0 when the first frame is a little later (B-frames)
      if (b.length > 0 && el.currentTime < b.start(0) && b.start(0) - el.currentTime < 1) el.currentTime = b.start(0);
    }
  };
  // Reading pauses (and the server's stream with it) while enough is buffered; ManagedMediaSource
  // tells when it wants more (streaming)
  const enough = () => {
    const ahead = aheadOf(el);
    return ahead > MAX_AHEAD_S || (managed && !(ms as MediaSource & { streaming: boolean }).streaming && ahead > MIN_AHEAD_S);
  };

  for (;;) {
    phase = 'read';
    const { done, value } = await reader.read();
    if (signal.aborted) return;
    if (done) break;
    bytes += value.length;
    lastData = Date.now();
    if (stuck) feedNote?.('feed-unstuck', {});
    stuck = false;
    if (!sb) {
      head.push(value);
      const init = concat(head);
      const end = initEnd(init);
      if (!end) {
        head = [init];
        continue;
      }
      const codecs = codecsOf(init.subarray(0, end));
      sb = ms.addSourceBuffer(`video/mp4; codecs="${codecs.join(',')}"`);
      head = [];
      queue.push(init);
      queued += init.length;
    } else {
      queue.push(value);
      queued += value.length;
    }
    await flush();
    if (enough()) {
      holding.add(el);
      const from = Date.now();
      const streaming = managed ? (ms as MediaSource & { streaming: boolean }).streaming : undefined;
      while (!signal.aborted && !lost && !pauseStop && enough()) await sleep(500);
      holding.delete(el);
      lastData = Date.now();
      if (lastData - from > 3000) feedNote?.('feed-hold', { s: Math.round((lastData - from) / 1000), ahead: Math.round(aheadOf(el)), streaming });
    }
    if (lost || pauseStop) break;
  }
  clearInterval(watch);
  // Not the end of the title: the player restarts the stream when the viewer plays on
  if (pauseStop && !lost) return;
  if (!signal.aborted) feedNote?.('feed-end', { lost, mb: Math.round(bytes / 1e5) / 10, fed: Math.round(fedEnd) });
  if (lost) throw new Error('evicted');
  if (!sb) throw new Error('no init segment');
  if (!signal.aborted) {
    await flush();
    if (ms.readyState === 'open' && !sb.updating) ms.endOfStream();
  }
}

import { KeyframeScanner, initSegmentEnd, mimeCandidates, parseCodecs } from './mp4Codecs';

// Feeds one /play stream (live fragmented MP4) to a <video> through Media Source Extensions.
// LG webOS reads the metadata of such a stream given as `src` but decodes no frame; the same bytes
// appended to a SourceBuffer play.
//
// The TV's SourceBuffer holds little (an LG TV with Chrome 120 refused appends at ~86 MB, about
// 70 s of a 1080p Blu-ray), so the buffer is planned in bytes: up to 2 min ahead (the download
// pauses there and resumes below 1.5 min) and up to 1 min behind, both shrunk to fit the budget.
// Played data is removed only up to a keyframe: remove() also drops every frame up to the next
// keyframe after its end, which could be the frames at the playhead.

const AHEAD_HIGH_S = 120;
const AHEAD_LOW_S = 90;
const KEEP_BEHIND_S = 60;
const MIN_BEHIND_S = 5;
const MIN_AHEAD_S = 8;
/** Bytes the SourceBuffer may hold; lowered for the session by a quota error */
let budgetBytes = 75 * 1024 * 1024;
const MAX_APPEND = 2 * 1024 * 1024;
const MAX_QUEUED = 8 * 1024 * 1024;
const MAX_HEAD = 4 * 1024 * 1024;
const QUOTA_RETRY_MS = 1000;

export interface MseFailure {
  status?: number;
  message: string;
}

export interface MseCallbacks {
  /** The request or an append failed; the stream is stopped */
  onFail: (e: MseFailure) => void;
  /** No SourceBuffer type fits these codecs; the stream is stopped */
  onUnsupported: (mimes: string[]) => void;
}

export const mseAvailable = () => typeof window !== 'undefined' && typeof (window as any).MediaSource === 'function' &&
  typeof ReadableStream !== 'undefined';

function concat(parts: Uint8Array[], total: number): Uint8Array {
  if (parts.length === 1) return parts[0];
  const out = new Uint8Array(total);
  let p = 0;
  for (const x of parts) {
    out.set(x, p);
    p += x.byteLength;
  }
  return out;
}

/** End of the buffered range at (or right after) t; t when nothing is buffered there */
function aheadEnd(r: TimeRanges, t: number): number {
  for (let i = 0; i < r.length; i++) {
    if (t < r.end(i) && t >= r.start(i) - 0.5) return r.end(i);
  }
  return t;
}

function bufferedSeconds(r: TimeRanges): number {
  let s = 0;
  for (let i = 0; i < r.length; i++) s += r.end(i) - r.start(i);
  return s;
}

export class MseStream {
  /** SourceBuffer type in use */
  mime = '';
  private ms = new MediaSource();
  private objectUrl: string;
  private sb: SourceBuffer | null = null;
  private ctrl = new AbortController();
  private reader: ReadableStreamDefaultReader<Uint8Array> | null = null;
  private head: Uint8Array[] = [];
  private headBytes = 0;
  private queue: Uint8Array[] = [];
  private queued = 0;
  private done = false;
  private stopped = false;
  private paused = false;
  private ended = false;
  private aligned = false;
  private evictTo = 0;
  private scanner = new KeyframeScanner();
  private keyframes: number[] = [];
  // Bytes per second of media, from what was appended
  private appended = 0;
  private mediaFrom = -1;
  private timer: ReturnType<typeof setInterval>;
  private retryTimer?: ReturnType<typeof setTimeout>;
  private wake: (() => void) | null = null;

  constructor(private video: HTMLVideoElement, private url: string, private cb: MseCallbacks) {
    this.objectUrl = URL.createObjectURL(this.ms);
    this.ms.addEventListener('sourceopen', () => this.tryInit());
    video.src = this.objectUrl;
    this.timer = setInterval(() => this.maintain(), 1000);
    this.run();
  }

  /** Still downloading (false once the file is in, or while paused with a full buffer) */
  get fetching(): boolean {
    return !this.stopped && !this.done && !this.paused;
  }

  stop() {
    if (this.stopped) return;
    this.stopped = true;
    clearInterval(this.timer);
    clearTimeout(this.retryTimer);
    this.ctrl.abort();
    // The abort polyfill of old webOS does not close the connection; cancelling the body does
    this.reader?.cancel().catch(() => {});
    this.wake?.();
    try {
      if (this.ms.readyState === 'open' && this.sb?.updating) this.sb.abort();
    } catch {
      /* already detached */
    }
    this.queue = [];
    this.head = [];
    URL.revokeObjectURL(this.objectUrl);
  }

  private fail(e: MseFailure) {
    if (this.stopped) return;
    this.stop();
    this.cb.onFail(e);
  }

  private async run() {
    let res: Response;
    try {
      res = await fetch(this.url, { signal: this.ctrl.signal });
    } catch (e: any) {
      if (!this.stopped) this.fail({ message: String(e?.message || e) });
      return;
    }
    if (this.stopped) return;
    if (!res.ok || !res.body) {
      this.fail({ status: res.status, message: `HTTP ${res.status}` });
      return;
    }
    const reader = res.body.getReader();
    this.reader = reader;
    try {
      for (;;) {
        await this.room();
        if (this.stopped) return;
        const { done, value } = await reader.read();
        if (this.stopped) return;
        if (done) break;
        if (!value || value.byteLength === 0) continue;
        const kf = this.scanner.push(value);
        if (kf.length) this.keyframes.push(...kf);
        if (this.sb) {
          this.queue.push(value);
          this.queued += value.byteLength;
          this.pump();
        } else {
          this.head.push(value);
          this.headBytes += value.byteLength;
          this.tryInit();
        }
      }
    } catch (e: any) {
      if (!this.stopped) this.fail({ message: String(e?.message || e) });
      return;
    }
    this.done = true;
    // A stream that ended before its init segment carried nothing playable
    if (!this.sb) this.fail({ message: 'stream ended before the init segment' });
    else this.pump();
  }

  /** Waits while the append queue is long or enough is buffered ahead */
  private async room() {
    while (!this.stopped && (this.queued > MAX_QUEUED || this.paused)) {
      await new Promise<void>(resolve => {
        this.wake = resolve;
        setTimeout(resolve, 250);
      });
      this.wake = null;
    }
  }

  private tryInit() {
    if (this.sb || this.stopped || this.ms.readyState !== 'open' || this.headBytes === 0) return;
    const data = concat(this.head, this.headBytes);
    this.head = [data];
    if (initSegmentEnd(data) === 0) {
      if (this.headBytes > MAX_HEAD) this.fail({ message: 'no init segment' });
      return;
    }
    const mimes = mimeCandidates(parseCodecs(data));
    for (const mime of mimes) {
      if (!MediaSource.isTypeSupported(mime)) continue;
      try {
        this.sb = this.ms.addSourceBuffer(mime);
        this.mime = mime;
        break;
      } catch {
        /* try the next one */
      }
    }
    const sb = this.sb;
    if (!sb) {
      this.stop();
      this.cb.onUnsupported(mimes);
      return;
    }
    this.video.setAttribute('data-mime', this.mime);
    sb.addEventListener('updateend', () => this.onUpdateEnd());
    sb.addEventListener('error', () => this.fail({ message: 'SourceBuffer error' }));
    this.head = [];
    this.headBytes = 0;
    this.queue = [data];
    this.queued = data.byteLength;
    this.pump();
  }

  private onUpdateEnd() {
    if (this.stopped) return;
    const v = this.video;
    if (v.buffered.length > 0) {
      if (this.mediaFrom < 0) this.mediaFrom = v.buffered.start(0);
      // Copied video may start a little after 0: start at the first buffered frame, not before it
      if (!this.aligned) {
        this.aligned = true;
        const start = v.buffered.start(0);
        if (start > v.currentTime + 0.01 && start < 10) v.currentTime = start;
      }
    }
    this.pump();
  }

  /** Bytes per second of media appended so far; 0 until a few seconds are in */
  private byteRate(): number {
    const b = this.video.buffered;
    if (this.mediaFrom < 0 || b.length === 0) return 0;
    const secs = b.end(b.length - 1) - this.mediaFrom;
    return secs >= 3 ? this.appended / secs : 0;
  }

  /** Seconds kept behind and ahead of the position within the byte budget */
  private plan(): { behind: number; ahead: number } {
    const rate = this.byteRate();
    const total = rate > 0 ? budgetBytes / rate : AHEAD_HIGH_S + KEEP_BEHIND_S;
    const behind = Math.max(MIN_BEHIND_S, Math.min(KEEP_BEHIND_S, total * 0.25));
    return { behind, ahead: Math.max(MIN_AHEAD_S, Math.min(AHEAD_HIGH_S, total - behind - 2)) };
  }

  /** The latest keyframe at or before t (data before it can go); 0 when none is known */
  private keyframeBefore(t: number): number {
    let best = 0;
    for (const k of this.keyframes) if (k <= t && k > best) best = k;
    return best;
  }

  /** One SourceBuffer operation at a time: eviction first, then appends, then end of stream */
  private pump() {
    const sb = this.sb;
    if (!sb || sb.updating || this.stopped || this.ms.readyState !== 'open') return;
    if (this.evictTo > 0) {
      const to = this.evictTo;
      this.evictTo = 0;
      if (sb.buffered.length > 0 && sb.buffered.start(0) < to - 0.1) {
        sb.remove(0, to);
        this.keyframes = this.keyframes.filter(k => k >= to);
        return;
      }
    }
    if (this.queue.length > 0) {
      const parts: Uint8Array[] = [];
      let size = 0;
      while (this.queue.length > 0 && (size === 0 || size + this.queue[0].byteLength <= MAX_APPEND)) {
        const x = this.queue.shift()!;
        parts.push(x);
        size += x.byteLength;
      }
      const data = concat(parts, size);
      try {
        sb.appendBuffer(data as Uint8Array<ArrayBuffer>);
        this.queued -= size;
        this.appended += size;
      } catch (e: any) {
        this.queue.unshift(data);
        if (e?.name === 'QuotaExceededError') this.onQuota();
        else this.fail({ message: String(e?.message || e) });
      }
      return;
    }
    if (this.done && !this.ended) {
      this.ended = true;
      try {
        this.ms.endOfStream();
      } catch {
        /* closed meanwhile */
      }
    }
  }

  // The TV's buffer is full: plan with less from now on, drop what was played and try again
  private onQuota() {
    const v = this.video;
    const rate = this.byteRate();
    const held = rate * bufferedSeconds(v.buffered);
    if (held > 0) budgetBytes = Math.max(8 * 1024 * 1024, Math.min(budgetBytes, held * 0.85));
    this.paused = true;
    const kf = this.keyframeBefore(v.currentTime - 1);
    if (kf > 0) this.evictTo = kf;
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => this.pump(), QUOTA_RETRY_MS);
  }

  private maintain() {
    const sb = this.sb;
    if (!sb || this.stopped || this.ms.readyState !== 'open') return;
    const v = this.video;
    const t = v.currentTime;
    const { behind, ahead: high } = this.plan();
    const ahead = aheadEnd(v.buffered, t) - t;
    if (!this.paused && ahead > high) this.paused = true;
    else if (this.paused && ahead < Math.min(AHEAD_LOW_S, high * 0.75)) {
      this.paused = false;
      this.wake?.();
    }
    if (sb.buffered.length > 0 && t - sb.buffered.start(0) > behind + 5) {
      const kf = this.keyframeBefore(t - behind);
      if (kf > sb.buffered.start(0) + 0.5) this.evictTo = kf;
    }
    this.pump();
  }
}

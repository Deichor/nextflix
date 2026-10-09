import { KeyframeScanner, initSegmentEnd, mimeCandidates, parseCodecs, type TrackCodec } from './mp4Codecs';
import { logNote, markSeek } from './playbackLog';

// Feeds /play streams (live fragmented MP4) to a <video> through Media Source Extensions.
// LG webOS reads the metadata of such a stream given as `src` but decodes no frame; the same bytes
// appended to a SourceBuffer play.
//
// Layouts: one muxed stream in one SourceBuffer, or split mode (server ?track=video / ?track=audio)
// with a video and an audio SourceBuffer. The TV takes E-AC-3 (Atmos) only in an audio-only
// SourceBuffer, never muxed with video. Split streams carry the file's own timestamps, so both are
// appended as they are and the element is seeked to the start position once data is in.
//
// The TV's SourceBuffers hold little (an LG TV with Chrome 120 refused appends at ~79 MB of video and
// ~18 MB of audio), so the buffer is planned in bytes per SourceBuffer: up to 2 min ahead (the
// download pauses there and resumes below 1.5 min) and up to 1 min behind, both shrunk to fit the
// budget. Played data is removed only up to a keyframe: remove() also drops every frame up to the
// next keyframe after its end, which could be the frames at the playhead.
//
// Splice (seamless source switch, like a DASH quality switch): the next stream is fetched next to
// the playing one and held in memory until it covers a few seconds past its start, then each
// SourceBuffer drops what it holds from that start on, switches codecs (changeType) when they differ
// within a family (avc1.640029 -> avc1.640028) and takes the new stream with a timestampOffset that
// puts it on the same timeline. Another codec family (H.264 -> HEVC) is refused: see checkSplice.

const AHEAD_HIGH_S = 120;
const AHEAD_LOW_S = 90;
const KEEP_BEHIND_S = 60;
const MIN_BEHIND_S = 5;
const MIN_AHEAD_S = 8;
/** Bytes a SourceBuffer may hold; lowered for the session by a quota error */
const budgets = { video: 75 * 1024 * 1024, audio: 15 * 1024 * 1024 };
const MAX_APPEND = 2 * 1024 * 1024;
const MAX_QUEUED = 8 * 1024 * 1024;
const MAX_HEAD = 4 * 1024 * 1024;
const QUOTA_RETRY_MS = 1000;
/** A spliced stream must cover this much past its start before it replaces the playing one */
export const SPLICE_READY_S = 4;
const SPLICE_QUEUED = { video: 48 * 1024 * 1024, audio: 4 * 1024 * 1024 };
/** Gaps up to this long (e.g. a few frames at a splice) are jumped over */
const MAX_GAP_S = 0.5;

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

export type LaneKind = 'muxed' | 'video' | 'audio';
/** One muxed stream, or the video and audio streams of split mode */
export type StreamUrls = Partial<Record<LaneKind, string>>;

export type SpliceResult =
  | { ok: true; cut: number }
  | { ok: false; reason: 'late' | 'failed' | 'unsupported' | 'stopped' | 'layout' };

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

const FAMILIES: Record<string, string> = { avc1: 'avc', avc3: 'avc', hvc1: 'hevc', hev1: 'hevc', dvh1: 'dv', dvhe: 'dv', mp4a: 'aac' };

/** Both MIME types carry the same codecs up to profile / level (avc1.640029 and avc1.640028) */
function sameFamily(a: string, b: string): boolean {
  const families = (m: string) => ((m.match(/codecs="([^"]*)"/) || [])[1] || '').split(',')
    .map(c => { const k = c.trim().split('.')[0]; return FAMILIES[k] || k; }).join(',');
  return families(a) === families(b);
}

const settled = (sb: SourceBuffer) => new Promise<void>(resolve => {
  if (!sb.updating) resolve();
  else sb.addEventListener('updateend', () => resolve(), { once: true });
});

/** One HTTP response of a /play stream, read into a queue of chunks (the init segment first) */
class Feed {
  queue: Uint8Array[] = [];
  queued = 0;
  /** Tracks of the init segment; null until it arrived */
  codecs: TrackCodec[] | null = null;
  done = false;
  /** Paused on purpose: enough is buffered ahead */
  paused = false;
  stopped = false;
  scanner = new KeyframeScanner();
  /** Decode times (stream timeline) of fragments that start with a keyframe */
  keyframes: number[] = [];
  private head: Uint8Array[] = [];
  private headBytes = 0;
  private ctrl = new AbortController();
  private reader: ReadableStreamDefaultReader<Uint8Array> | null = null;
  private wake: (() => void) | null = null;

  constructor(readonly url: string, private maxQueued: number, private onData: () => void, private onFail: (e: MseFailure) => void) {
    this.run();
  }

  get fetching(): boolean {
    return !this.stopped && !this.done && !this.paused;
  }

  setMaxQueued(n: number) {
    this.maxQueued = n;
    this.wake?.();
  }

  setPaused(p: boolean) {
    this.paused = p;
    if (!p) this.wake?.();
  }

  stop() {
    if (this.stopped) return;
    this.stopped = true;
    this.ctrl.abort();
    // The abort polyfill of old webOS does not close the connection; cancelling the body does
    this.reader?.cancel().catch(() => {});
    this.wake?.();
    this.queue = [];
    this.queued = 0;
    this.head = [];
  }

  /** Up to MAX_APPEND bytes from the queue */
  take(): Uint8Array | null {
    if (this.queue.length === 0) return null;
    const parts: Uint8Array[] = [];
    let size = 0;
    while (this.queue.length > 0 && (size === 0 || size + this.queue[0].byteLength <= MAX_APPEND)) {
      const x = this.queue.shift()!;
      parts.push(x);
      size += x.byteLength;
    }
    this.queued -= size;
    if (this.queued <= this.maxQueued) this.wake?.();
    return concat(parts, size);
  }

  putBack(data: Uint8Array) {
    this.queue.unshift(data);
    this.queued += data.byteLength;
  }

  private fail(e: MseFailure) {
    if (this.stopped) return;
    this.stop();
    this.onFail(e);
  }

  private async run() {
    let res: Response;
    try {
      res = await fetch(this.url, { signal: this.ctrl.signal });
    } catch (e: any) {
      this.fail({ message: String(e?.message || e) });
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
        if (this.codecs) {
          this.queue.push(value);
          this.queued += value.byteLength;
        } else {
          this.head.push(value);
          this.headBytes += value.byteLength;
          this.parseHead();
        }
        this.onData();
      }
    } catch (e: any) {
      this.fail({ message: String(e?.message || e) });
      return;
    }
    this.done = true;
    // A stream that ended before its init segment carried nothing playable
    if (!this.codecs) this.fail({ message: 'stream ended before the init segment' });
    else this.onData();
  }

  private parseHead() {
    const data = concat(this.head, this.headBytes);
    this.head = [data];
    if (initSegmentEnd(data) === 0) {
      if (this.headBytes > MAX_HEAD) this.fail({ message: 'no init segment' });
      return;
    }
    this.codecs = parseCodecs(data);
    this.head = [];
    this.headBytes = 0;
    this.queue = [data];
    this.queued = data.byteLength;
  }

  /** Waits while the queue is long or enough is buffered ahead */
  private async room() {
    while (!this.stopped && (this.queued > this.maxQueued || this.paused)) {
      await new Promise<void>(resolve => {
        this.wake = resolve;
        setTimeout(resolve, 250);
      });
      this.wake = null;
    }
  }
}

/** One SourceBuffer and the stream that feeds it */
class Lane {
  sb: SourceBuffer | null = null;
  mime = '';
  /** media time = stream time + tsOffset for the current feed */
  tsOffset = 0;
  /** Keyframes (media time) of earlier feeds of this SourceBuffer */
  pastKeyframes: number[] = [];
  evictTo = 0;
  // Bytes per second of media, from what was appended
  appended = 0;
  mediaFrom = -1;
  // Bytes of each append and the buffered end after it: what the SourceBuffer holds, counted exactly
  // (the bitrate varies a lot, an average rate times the buffered seconds overshoots the TV's limit)
  private spans: { end: number; bytes: number }[] = [];
  private pending = 0;

  constructor(readonly kind: LaneKind, public feed: Feed) {}

  appending(bytes: number) {
    this.appended += bytes;
    this.pending += bytes;
  }

  /** After an operation finished: the bytes of the last append end where the buffer ends now */
  settledAppend() {
    const b = this.sb?.buffered;
    if (this.pending === 0 || !b || b.length === 0) return;
    this.spans.push({ end: b.end(b.length - 1), bytes: this.pending });
    this.pending = 0;
  }

  /** Bytes held: appends whose data was not removed yet (one partly removed counts whole) */
  held(): number {
    const b = this.sb?.buffered;
    const start = b && b.length > 0 ? b.start(0) : Infinity;
    while (this.spans.length > 0 && this.spans[0].end <= start) this.spans.shift();
    return this.spans.reduce((n, s) => n + s.bytes, this.pending);
  }

  /** Data from t on was removed (splice) */
  heldUntil(t: number) {
    const i = this.spans.findIndex(s => s.end > t);
    if (i >= 0) this.spans.length = i + 1;
    this.pending = 0;
  }

  get budgetKey(): 'video' | 'audio' {
    return this.kind === 'audio' ? 'audio' : 'video';
  }

  /** Tracks of the init segment this SourceBuffer takes */
  tracksOf(codecs: TrackCodec[]): TrackCodec[] {
    return this.kind === 'muxed' ? codecs : codecs.filter(c => c.kind === this.kind);
  }

  /** Bytes per second of media appended so far; 0 until a few seconds are in */
  byteRate(): number {
    const b = this.sb?.buffered;
    if (!b || this.mediaFrom < 0 || b.length === 0) return 0;
    const secs = b.end(b.length - 1) - this.mediaFrom;
    return secs >= 3 ? this.appended / secs : 0;
  }

  /** Seconds kept behind and ahead of the position within the byte budget */
  plan(): { behind: number; ahead: number } {
    const rate = this.byteRate();
    const total = rate > 0 ? budgets[this.budgetKey] / rate : AHEAD_HIGH_S + KEEP_BEHIND_S;
    // Most of a small budget goes ahead (a 4K stream fits ~30 s in all)
    const behind = Math.max(MIN_BEHIND_S, Math.min(KEEP_BEHIND_S, total * 0.15));
    return { behind, ahead: Math.max(MIN_AHEAD_S, Math.min(AHEAD_HIGH_S, total - behind - 2)) };
  }

  /** The latest keyframe at or before t (data before it can go); 0 when none is known */
  keyframeBefore(t: number): number {
    let best = 0;
    for (const k of this.pastKeyframes) if (k <= t && k > best) best = k;
    for (const s of this.feed.keyframes) {
      const k = s + this.tsOffset;
      if (k <= t && k > best) best = k;
    }
    return best;
  }

  dropKeyframesBefore(to: number) {
    this.pastKeyframes = this.pastKeyframes.filter(k => k >= to);
    this.feed.keyframes = this.feed.keyframes.filter(k => k + this.tsOffset >= to);
  }
}

interface SpliceJob {
  feeds: { lane: Lane; feed: Feed }[];
  tsOffset: number;
  /** Media time the new data is expected to start at (before its first fragment is known) */
  cutHint: number;
  readyS: number;
  committing: boolean;
  resolve: (r: SpliceResult) => void;
}

export class MseStream {
  /** SourceBuffer types in use */
  mime = '';
  readonly split: boolean;
  private ms = new MediaSource();
  private objectUrl: string;
  private lanes: Lane[];
  private stopped = false;
  private ended = false;
  private aligned = false;
  private ready = false;
  private job: SpliceJob | null = null;
  private timer: ReturnType<typeof setInterval>;
  private retryTimer?: ReturnType<typeof setTimeout>;

  /**
   * startAt: split mode only, the media time to start at (the streams start on the keyframe before
   * it and carry the file's timestamps).
   */
  constructor(private video: HTMLVideoElement, urls: StreamUrls, private cb: MseCallbacks, private startAt = 0) {
    const kinds: LaneKind[] = urls.muxed ? ['muxed'] : ['video', 'audio'];
    this.split = !urls.muxed;
    this.lanes = kinds.map(kind => {
      const lane: Lane = new Lane(kind, null as unknown as Feed);
      lane.feed = this.newFeed(lane, urls[kind] || '', MAX_QUEUED);
      return lane;
    });
    this.objectUrl = URL.createObjectURL(this.ms);
    this.ms.addEventListener('sourceopen', () => this.tryInit());
    video.addEventListener('waiting', this.onWaiting);
    video.src = this.objectUrl;
    this.timer = setInterval(() => this.maintain(), 1000);
  }

  /** Still downloading (false once the file is in, or while paused with a full buffer) */
  get fetching(): boolean {
    return !this.stopped && this.lanes.some(l => l.feed.fetching) && !this.lanes.some(l => l.feed.paused);
  }

  /** Seconds ahead the byte budget allows (the smallest of the SourceBuffers) */
  aheadCap(): number {
    return Math.min(...this.lanes.map(l => l.plan().ahead));
  }

  /** A splice is being prepared or committed */
  get splicing(): boolean {
    return !!this.job;
  }

  stop() {
    if (this.stopped) return;
    this.stopped = true;
    clearInterval(this.timer);
    clearTimeout(this.retryTimer);
    this.video.removeEventListener('waiting', this.onWaiting);
    this.finishSplice({ ok: false, reason: 'stopped' });
    for (const l of this.lanes) {
      l.feed.stop();
      try {
        if (this.ms.readyState === 'open' && l.sb?.updating) l.sb.abort();
      } catch {
        /* already detached */
      }
    }
    URL.revokeObjectURL(this.objectUrl);
  }

  private newFeed(lane: Lane, url: string, maxQueued: number): Feed {
    const feed: Feed = new Feed(url, maxQueued, () => {
      if (lane.feed === feed) {
        if (this.ready) this.pump(lane);
        else this.tryInit();
      } else {
        this.checkSplice();
      }
    }, e => {
      if (lane.feed === feed) this.fail(e);
      else this.finishSplice({ ok: false, reason: 'failed' });
    });
    return feed;
  }

  private fail(e: MseFailure) {
    if (this.stopped) return;
    this.stop();
    this.cb.onFail(e);
  }

  // Every SourceBuffer is added before any media goes in: the element reaches HAVE_METADATA once
  // each one has its init segment, and no SourceBuffer can be added after that
  private tryInit() {
    if (this.ready || this.stopped || this.ms.readyState !== 'open') return;
    if (this.lanes.some(l => !l.feed.codecs)) return;
    const tried: string[] = [];
    for (const lane of this.lanes) {
      const mimes = mimeCandidates(lane.tracksOf(lane.feed.codecs!));
      tried.push(...mimes);
      for (const mime of mimes) {
        if (!MediaSource.isTypeSupported(mime)) continue;
        try {
          lane.sb = this.ms.addSourceBuffer(mime);
          lane.mime = mime;
          break;
        } catch {
          /* try the next one */
        }
      }
      if (!lane.sb) {
        this.stop();
        this.cb.onUnsupported(tried);
        return;
      }
      const sb = lane.sb;
      sb.addEventListener('updateend', () => this.onUpdateEnd(lane));
      sb.addEventListener('error', () => this.fail({ message: 'SourceBuffer error' }));
    }
    this.ready = true;
    this.setMime();
    this.lanes.forEach(l => this.pump(l));
  }

  private setMime() {
    this.mime = this.lanes.map(l => l.mime).join(' + ');
    this.video.setAttribute('data-mime', this.mime);
  }

  private onUpdateEnd(lane: Lane) {
    if (this.stopped) return;
    const b = lane.sb!.buffered;
    if (b.length > 0 && lane.mediaFrom < 0) lane.mediaFrom = b.start(0);
    lane.settledAppend();
    const v = this.video;
    if (!this.aligned && v.buffered.length > 0) {
      this.aligned = true;
      const start = v.buffered.start(0);
      if (this.split) {
        // Split streams start on the keyframe before the asked position: play from that position
        const at = this.startAt > start && this.startAt < v.buffered.end(v.buffered.length - 1) ? this.startAt : start;
        if (Math.abs(v.currentTime - at) > 0.05) {
          markSeek('align-split');
          v.currentTime = at;
        }
      } else if (start > v.currentTime + 0.01 && start < 10) {
        // Copied video may start a little after 0: start at the first buffered frame, not before it
        markSeek('align-start');
        v.currentTime = start;
      }
    }
    this.pump(lane);
  }

  /** One SourceBuffer operation at a time: eviction first, then appends, then end of stream */
  private pump(lane: Lane) {
    const sb = lane.sb;
    if (!sb || sb.updating || this.stopped || this.ms.readyState === 'closed' || this.job?.committing) return;
    if (lane.evictTo > 0) {
      const to = lane.evictTo;
      lane.evictTo = 0;
      if (sb.buffered.length > 0 && sb.buffered.start(0) < to - 0.1) {
        sb.remove(0, to);
        lane.dropKeyframesBefore(to);
        return;
      }
    }
    // Never past the plan or the byte budget: over its limit the TV's own garbage collection also
    // drops data ahead of the playhead, and playback stops there (measured on an LG TV)
    if (lane.feed.queued > 0) {
      const t = this.video.currentTime;
      const full = lane.held() + MAX_APPEND > budgets[lane.budgetKey];
      if (full || (lane.byteRate() > 0 && aheadEnd(sb.buffered, t) - t > lane.plan().ahead + 1)) {
        const kf = full ? lane.keyframeBefore(t - MIN_BEHIND_S) : 0;
        if (kf > 0 && sb.buffered.length > 0 && kf > sb.buffered.start(0) + 0.5) {
          lane.evictTo = kf;
          this.pump(lane);
        } else {
          lane.feed.setPaused(true);
        }
        return;
      }
    }
    const data = lane.feed.take();
    if (data) {
      try {
        sb.appendBuffer(data as Uint8Array<ArrayBuffer>);
        lane.appending(data.byteLength);
      } catch (e: any) {
        lane.feed.putBack(data);
        if (e?.name === 'QuotaExceededError') this.onQuota(lane);
        else this.fail({ message: String(e?.message || e) });
      }
      return;
    }
    const allIn = this.lanes.every(l => l.feed.done && l.feed.queued === 0 && !l.sb?.updating);
    if (allIn && !this.ended && this.ms.readyState === 'open') {
      this.ended = true;
      try {
        this.ms.endOfStream();
      } catch {
        /* closed meanwhile */
      }
    }
  }

  // The buffer is full: plan with less from now on, drop what was played and try again
  private onQuota(lane: Lane) {
    const held = lane.held();
    const key = lane.budgetKey;
    logNote('quota', { lane: lane.kind, mb: Math.round(held / 1048576), budget: Math.round(budgets[key] / 1048576) });
    if (held > 0) budgets[key] = Math.max((key === 'audio' ? 2 : 8) * 1024 * 1024, Math.min(budgets[key], held * 0.85));
    lane.feed.setPaused(true);
    const kf = lane.keyframeBefore(this.video.currentTime - 1);
    if (kf > 0) lane.evictTo = kf;
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => this.lanes.forEach(l => this.pump(l)), QUOTA_RETRY_MS);
  }

  private maintain() {
    if (this.stopped || !this.ready || this.ms.readyState === 'closed') return;
    const t = this.video.currentTime;
    const plans = this.lanes.map(l => l.plan());
    // Every SourceBuffer stops at the same lead, so the downloads stay together
    const high = Math.min(...plans.map(p => p.ahead));
    this.lanes.forEach((lane, i) => {
      const sb = lane.sb!;
      const ahead = aheadEnd(sb.buffered, t) - t;
      if (!lane.feed.paused && ahead > high) lane.feed.setPaused(true);
      else if (lane.feed.paused && ahead < Math.min(AHEAD_LOW_S, high * 0.75)) lane.feed.setPaused(false);
      const behind = plans[i].behind;
      if (sb.buffered.length > 0 && t - sb.buffered.start(0) > behind + 5) {
        const kf = lane.keyframeBefore(t - behind);
        if (kf > sb.buffered.start(0) + 0.5) lane.evictTo = kf;
      }
      this.pump(lane);
    });
    this.jumpGap();
    this.checkSplice();
  }

  private onWaiting = () => {
    this.logRanges();
    this.jumpGap();
  };

  /** Each SourceBuffer's ranges near the playhead, to tell an audio hole from a video one */
  private logRanges() {
    const t = this.video.currentTime;
    const near = (r: TimeRanges) => {
      const out: string[] = [];
      for (let i = 0; i < r.length; i++) {
        if (r.end(i) > t - 30 && r.start(i) < t + 120) out.push(`${r.start(i).toFixed(2)}-${r.end(i).toFixed(2)}`);
      }
      return out.join(' ');
    };
    const x: Record<string, string | number> = { t: Math.round(t * 100) / 100 };
    for (const lane of this.lanes) if (lane.sb) x[lane.kind] = near(lane.sb.buffered);
    logNote('ranges', x);
  }

  /** Plays over a small hole in the buffer (a splice boundary, the start of split streams) */
  private jumpGap() {
    const v = this.video;
    const r = v.buffered;
    const t = v.currentTime;
    for (let i = 0; i < r.length; i++) {
      if (t >= r.start(i) && t < r.end(i) - 0.1) return;
    }
    for (let i = 0; i < r.length; i++) {
      if (r.start(i) > t && r.start(i) - t <= MAX_GAP_S) {
        markSeek('gap', { gap: r.start(i) - t });
        v.currentTime = r.start(i) + 0.01;
        return;
      }
    }
  }

  // ---- splice ----

  /**
   * Prepares a switch to other streams on the same timeline: `urls` names the SourceBuffers to
   * replace (muxed, or video and / or audio), `tsOffset` maps their time to the media time and
   * `cutHint` is the media time they are expected to start at. Resolves once the new data replaced
   * the old from its start on, or why it did not.
   */
  splice(urls: StreamUrls, tsOffset: number, cutHint: number, readyS = SPLICE_READY_S): Promise<SpliceResult> {
    return new Promise(resolve => {
      if (this.stopped) return resolve({ ok: false, reason: 'stopped' });
      const kinds = Object.keys(urls) as LaneKind[];
      const lanes = kinds.map(k => this.lanes.find(l => l.kind === k));
      if (!this.ready || kinds.length === 0 || lanes.some(l => !l || !l.sb)) return resolve({ ok: false, reason: 'layout' });
      this.finishSplice({ ok: false, reason: 'stopped' });
      const feeds = (lanes as Lane[]).map(lane => ({
        lane,
        feed: this.newFeed(lane, urls[lane.kind]!, SPLICE_QUEUED[lane.budgetKey]),
      }));
      this.job = { feeds, tsOffset, cutHint, readyS, committing: false, resolve };
    });
  }

  private finishSplice(r: SpliceResult) {
    const job = this.job;
    if (!job) return;
    this.job = null;
    if (!r.ok) job.feeds.forEach(f => { if (f.lane.feed !== f.feed) f.feed.stop(); });
    job.resolve(r);
  }

  private checkSplice() {
    const job = this.job;
    if (!job || job.committing || this.stopped) return;
    const t = this.video.currentTime;
    let ready = true;
    let cut = Infinity;
    for (const { feed } of job.feeds) {
      const first = feed.scanner.firstTime;
      if (first >= 0) cut = Math.min(cut, job.tsOffset + first);
      if (!feed.codecs || first < 0 || (feed.scanner.lastTime - first < job.readyS && !feed.done)) ready = false;
    }
    const deadline = (cut < Infinity ? cut : job.cutHint) - 1;
    if (t > deadline) {
      this.finishSplice({ ok: false, reason: 'late' });
      return;
    }
    if (!ready) return;
    // Every SourceBuffer must take the new codecs before anything is removed. changeType only within
    // a codec family: an LG TV (Chrome 120) never shows a frame after avc1 -> hvc1 in one SourceBuffer
    // (the clock stops at the cut, seeks on either side never finish)
    const mimes: string[] = [];
    for (const { lane, feed } of job.feeds) {
      const candidates = mimeCandidates(lane.tracksOf(feed.codecs!));
      const mime = candidates.indexOf(lane.mime) >= 0 ? lane.mime
        : typeof (lane.sb as any).changeType === 'function'
          ? candidates.find(m => sameFamily(m, lane.mime) && MediaSource.isTypeSupported(m)) : undefined;
      if (!mime) {
        this.finishSplice({ ok: false, reason: 'unsupported' });
        return;
      }
      mimes.push(mime);
    }
    this.commit(job, mimes);
  }

  private async commit(job: SpliceJob, mimes: string[]) {
    job.committing = true;
    let cut = Infinity;
    try {
      for (let i = 0; i < job.feeds.length; i++) {
        const { lane, feed } = job.feeds[i];
        const sb = lane.sb!;
        const at = job.tsOffset + feed.scanner.firstTime;
        cut = Math.min(cut, at);
        lane.feed.stop();
        lane.evictTo = 0;
        await settled(sb);
        if (this.stopped) return;
        // Forget the old stream's partial segment, then its data from the new start on
        if (this.ms.readyState === 'open') sb.abort();
        if (sb.buffered.length > 0 && sb.buffered.end(sb.buffered.length - 1) > at) {
          sb.remove(Math.max(0, at), Infinity);
          await settled(sb);
          if (this.stopped) return;
        }
        if (mimes[i] !== lane.mime) {
          (sb as any).changeType(mimes[i]);
          lane.mime = mimes[i];
        }
        sb.timestampOffset = job.tsOffset;
        lane.pastKeyframes = lane.pastKeyframes
          .concat(lane.feed.keyframes.map(k => k + lane.tsOffset))
          .filter(k => k < at);
        lane.feed = feed;
        lane.tsOffset = job.tsOffset;
        lane.heldUntil(at);
        // The rate counts the new stream only: its bytes over its media from the cut on (the old
        // data before the cut would make it look several times smaller and overfill the buffer)
        lane.appended = 0;
        lane.mediaFrom = at;
        feed.setMaxQueued(MAX_QUEUED);
      }
    } catch (e: any) {
      job.committing = false;
      this.fail({ message: `splice: ${String(e?.message || e)}` });
      return;
    }
    job.committing = false;
    this.ended = false;
    this.setMime();
    this.finishSplice({ ok: true, cut });
    this.lanes.forEach(l => this.pump(l));
    this.watchBoundary(cut);
  }

  /**
   * Old frames just before the cut that depended on removed ones are gone too (B-frames), so the old
   * data may end a few frames before the new data starts. The TV plays over that by itself (measured:
   * H.264 to H.264 on an LG TV); should playback still stop there, it jumps to the cut.
   */
  private watchBoundary(cut: number) {
    const v = this.video;
    let last = v.currentTime;
    let still = 0;
    let jumped = false;
    const started = Date.now();
    const tick = setInterval(() => {
      const t = v.currentTime;
      if (this.stopped || jumped || Date.now() - started > 120000 || t > cut + 3) {
        clearInterval(tick);
        return;
      }
      still = t === last && !v.seeking && t < cut && cut - t < 1.5 && (!v.paused || v.readyState < 3) ? still + 100 : 0;
      last = t;
      if (still >= 1000) {
        jumped = true;
        console.info(`[splice] stuck at ${t.toFixed(3)}: seek to the cut ${cut.toFixed(3)}`);
        markSeek('splice-stuck', { cut });
        v.currentTime = cut + 0.001;
      }
    }, 100);
  }
}

import { base } from '../lib/http';
import { bufferedEnd } from './bufferHealth';

// Playback diagnostics: notable events of one <video> (waits, freezes, position jumps, dropped frame
// bursts, seeks and why, late timers) sent in small batches to POST /api/playback-log, which only
// writes them to the server log. One 250 ms timer and the element's own events; no per-frame work.
// Off: set PLAYBACK_LOG to false, or localStorage nfx-tv:playback-log = "off".
export const PLAYBACK_LOG = true;
const OFF_KEY = 'nfx-tv:playback-log';

const TICK_MS = 250;
const FRAMES_EVERY = 8; // ticks (~2 s)
const AHEAD_EVERY = 4; // ticks (~1 s)
const FLUSH_MS = 30000;
const SUMMARY_MS = 60000;
const MAX_BODY = 32 * 1024;
// Events per batch window; the rest is counted as dropped
const MAX_EVENTS = 150;
const DROP_BURST = 5;
const LATE_MS = 250;
// Media time lost (or gained) against the wall clock that counts as a freeze (jump)
const FREEZE_S = 0.25;
const JUMP_S = 0.3;
// A seek within this long after a mark is the marked one
const MARK_MS = 1500;

type Val = number | string | boolean;
export type LogFields = Record<string, Val | undefined>;

interface Ev {
  t: number;
  k: string;
  p: number;
  x?: Record<string, Val>;
}

export interface LogContext {
  type: string;
  tmdb: number;
  s?: number;
  e?: number;
  title?: string;
  src?: string;
  mode?: string;
  debrid?: boolean;
  q?: string;
  dur?: number;
}

let active: PlaybackLog | null = null;

/** A one-off event from outside the engine (mseStream.ts) */
export function logNote(k: string, x?: LogFields) {
  active?.note(k, x);
}

/** The next seek of the element is the app's own, for `why` (mseStream.ts, engine). */
export function markSeek(why: string, x?: LogFields) {
  active?.mark(why, x);
}

const r2 = (n: number) => Math.round(n * 100) / 100;

function enabled(): boolean {
  if (!PLAYBACK_LOG) return false;
  try {
    return localStorage.getItem(OFF_KEY) !== 'off';
  } catch {
    return true;
  }
}

export class PlaybackLog {
  private sid = Math.random().toString(36).slice(2, 10);
  private seq = 0;
  private events: Ev[] = [];
  private dropped = 0;
  private timer: ReturnType<typeof setInterval>;
  private ticks = 0;
  private lastTick = performance.now();
  private lastFlush = Date.now();
  private lastSummary = Date.now();
  // position tracking (media time vs wall clock)
  private gen = 0;
  private baseGen = -1;
  private lastCt = 0;
  private lag = 0;
  private lagTicks = 0;
  private lagWaited = false;
  private wait: { at: number; ct: number; p: number; x: Record<string, Val> } | null = null;
  private seek: { at: number; from: number; why: string; x?: LogFields } | null = null;
  private pendingMark: { at: number; why: string; x?: LogFields } | null = null;
  private played = false;
  // dropped / decoded frames
  private frames = { dropped: -1, total: -1 };
  // summary since the last one
  private sum = { aheadMin: Infinity, aheadSum: 0, aheadN: 0, dropped: 0, decoded: 0, waits: 0, waitMs: 0, freezes: 0, freezeMs: 0, jumps: 0, late: 0, lateMax: 0 };

  /** extra: fields added to every summary (the subtitle delay) */
  static create(video: HTMLVideoElement, pos: () => number, ctx: () => LogContext | null, extra?: () => LogFields): PlaybackLog | null {
    return enabled() ? new PlaybackLog(video, pos, ctx, extra) : null;
  }

  private constructor(private v: HTMLVideoElement, private pos: () => number, private ctx: () => LogContext | null, private extra?: () => LogFields) {
    active = this;
    const on = (type: string, fn: () => void) => v.addEventListener(type, fn);
    on('waiting', this.onWaiting);
    on('playing', this.onPlaying);
    on('stalled', this.onStalled);
    on('seeking', this.onSeeking);
    on('seeked', this.onSeeked);
    on('pause', this.onPause);
    on('ended', this.onEnded);
    on('emptied', this.onEmptied);
    this.timer = setInterval(this.tick, TICK_MS);
  }

  // ---- engine API ----

  /** An engine event (restart, splice, hold, subtitle swap ...) */
  note(k: string, x?: LogFields) {
    if (k === 'restart' || k === 'open') this.reset();
    this.push(k, x);
  }

  mark(why: string, x?: LogFields) {
    this.pendingMark = { at: Date.now(), why, x };
  }

  /** Sends what is queued (keepalive: survives the app closing). */
  flush() {
    if (this.events.length === 0 && this.dropped === 0) return;
    const events = this.events;
    const dropped = this.dropped;
    this.events = [];
    this.dropped = 0;
    this.lastFlush = Date.now();
    const head = { sid: this.sid, seq: ++this.seq, client: 'tv', app: __APP_VERSION__, ctx: this.ctx(), dropped };
    let body = JSON.stringify({ ...head, events });
    // Over the cap: the oldest events go
    let keep = events;
    while (body.length > MAX_BODY && keep.length > 1) {
      keep = keep.slice(Math.ceil(keep.length / 4));
      body = JSON.stringify({ ...head, dropped: dropped + events.length - keep.length, events: keep });
    }
    try {
      // text/plain: a simple request, no CORS preflight
      fetch(`${base()}/api/playback-log`, { method: 'POST', body, keepalive: true, headers: { 'Content-Type': 'text/plain' } }).catch(() => {});
    } catch {
      /* diagnostics never break playback */
    }
  }

  destroy() {
    clearInterval(this.timer);
    this.closeWait('exit');
    this.summary();
    this.flush();
    const v = this.v;
    v.removeEventListener('waiting', this.onWaiting);
    v.removeEventListener('playing', this.onPlaying);
    v.removeEventListener('stalled', this.onStalled);
    v.removeEventListener('seeking', this.onSeeking);
    v.removeEventListener('seeked', this.onSeeked);
    v.removeEventListener('pause', this.onPause);
    v.removeEventListener('ended', this.onEnded);
    v.removeEventListener('emptied', this.onEmptied);
    if (active === this) active = null;
  }

  // ---- internals ----

  private push(k: string, x?: LogFields) {
    if (this.events.length >= MAX_EVENTS) {
      this.dropped++;
      return;
    }
    let p = 0;
    try {
      p = r2(this.pos());
    } catch {
      /* no position */
    }
    const ev: Ev = { t: Date.now(), k, p };
    if (x) {
      const out: Record<string, Val> = {};
      let n = 0;
      for (const key in x) {
        const val = x[key];
        if (val === undefined) continue;
        out[key] = typeof val === 'number' ? r2(val) : val;
        n++;
      }
      if (n > 0) ev.x = out;
    }
    this.events.push(ev);
  }

  private ahead(): number {
    const v = this.v;
    const end = bufferedEnd(v.buffered, v.currentTime);
    return end >= 0 ? end - v.currentTime : 0;
  }

  private media(): Record<string, Val> {
    const v = this.v;
    return { ahead: r2(this.ahead()), rs: v.readyState, ns: v.networkState };
  }

  /** New stream or seek: the position baseline starts over */
  private reset() {
    this.gen++;
    this.endLag();
  }

  private onWaiting = () => {
    if (this.wait) return;
    const v = this.v;
    this.wait = { at: Date.now(), ct: v.currentTime, p: this.pos(), x: { ...this.media(), seeking: v.seeking, start: !this.played } };
  };

  private closeWait(end: string) {
    const w = this.wait;
    if (!w) return;
    this.wait = null;
    const d = Date.now() - w.at;
    // Waits of a seek or a new stream are expected; still counted, without an event when short
    if (w.x.start || w.x.seeking) {
      if (d < 1000) return;
    } else {
      this.sum.waits++;
      this.sum.waitMs += d;
    }
    this.push('wait', { d, end, ...w.x, from: r2(w.p) });
  }

  private onPlaying = () => {
    this.played = true;
    this.closeWait('playing');
  };

  private onStalled = () => {
    this.push('stalled', this.media());
  };

  private onSeeking = () => {
    const m = this.pendingMark && Date.now() - this.pendingMark.at < MARK_MS ? this.pendingMark : null;
    this.pendingMark = null;
    this.seek = { at: Date.now(), from: this.lastCt, why: m ? m.why : 'unmarked', x: m?.x };
    this.reset();
  };

  private onSeeked = () => {
    const s = this.seek;
    this.seek = null;
    this.reset();
    if (!s) return;
    this.push('seek', { why: s.why, from: r2(s.from), to: r2(this.v.currentTime), d: Date.now() - s.at, ...s.x, ...this.media() });
  };

  private onPause = () => {
    this.closeWait('pause');
    this.reset();
    if (Date.now() - this.lastFlush > 2000) this.flush();
  };

  private onEnded = () => {
    this.closeWait('ended');
    this.push('ended');
    this.summary();
    this.flush();
  };

  private onEmptied = () => {
    this.closeWait('emptied');
    this.played = false;
    this.frames = { dropped: -1, total: -1 };
    this.reset();
  };

  private tick = () => {
    const now = performance.now();
    const dWall = (now - this.lastTick) / 1000;
    this.lastTick = now;
    this.ticks++;
    const v = this.v;
    const ct = v.currentTime;
    const running = !v.paused && !v.ended && !v.seeking && v.playbackRate === 1 && this.played && !document.hidden;
    // A timer firing late while playing: a long task on the main thread
    const late = dWall * 1000 - TICK_MS;
    if (late > LATE_MS && running) {
      this.sum.late++;
      this.sum.lateMax = Math.max(this.sum.lateMax, late);
      this.push('late', { ms: Math.round(late) });
    }
    if (this.wait && running && ct > this.wait.ct + 0.05) this.closeWait('progress');
    if (this.baseGen !== this.gen || !running) {
      this.baseGen = this.gen;
      this.endLag();
    } else {
      const dCt = ct - this.lastCt;
      if (dCt - dWall > JUMP_S || dCt < -0.05) {
        this.endLag();
        this.sum.jumps++;
        this.push('jump', { by: r2(dCt - dWall), from: r2(this.lastCt), to: r2(ct), ...this.media() });
      } else if (dWall - dCt > 0.05) {
        this.lag += dWall - dCt;
        this.lagTicks++;
        if (this.wait) this.lagWaited = true;
      } else {
        this.endLag();
      }
    }
    this.lastCt = ct;

    if (this.ticks % AHEAD_EVERY === 0 && running) {
      const a = this.ahead();
      this.sum.aheadMin = Math.min(this.sum.aheadMin, a);
      this.sum.aheadSum += a;
      this.sum.aheadN++;
    }
    if (this.ticks % FRAMES_EVERY === 0) this.sampleFrames();
    const wall = Date.now();
    if (wall - this.lastSummary >= SUMMARY_MS) this.summary();
    if (wall - this.lastFlush >= FLUSH_MS) this.flush();
  };

  /** A run of slow progress ended: report it when the picture lost more than FREEZE_S (without a wait) */
  private endLag() {
    const lag = this.lag;
    const n = this.lagTicks;
    const waited = this.lagWaited;
    this.lag = 0;
    this.lagTicks = 0;
    this.lagWaited = false;
    if (lag < FREEZE_S || waited) return;
    this.sum.freezes++;
    this.sum.freezeMs += lag * 1000;
    this.push('freeze', { ms: Math.round(lag * 1000), ticks: n, ...this.media() });
  }

  private sampleFrames() {
    const v = this.v;
    if (!v.getVideoPlaybackQuality) return;
    const q = v.getVideoPlaybackQuality();
    const f = this.frames;
    const dropped = q.droppedVideoFrames;
    const total = q.totalVideoFrames;
    if (f.total >= 0 && total >= f.total && dropped >= f.dropped) {
      const dd = dropped - f.dropped;
      const dt = total - f.total;
      this.sum.dropped += dd;
      this.sum.decoded += dt;
      if (dd > DROP_BURST) this.push('drops', { n: dd, of: dt, total: dropped, corrupt: q.corruptedVideoFrames });
    }
    this.frames = { dropped, total };
  }

  private summary() {
    const s = this.sum;
    this.lastSummary = Date.now();
    if (s.aheadN === 0 && s.waits === 0 && s.freezes === 0 && s.jumps === 0 && s.dropped === 0) {
      this.resetSum();
      return;
    }
    this.push('summary', {
      ahead_min: s.aheadN ? s.aheadMin : undefined,
      ahead_avg: s.aheadN ? s.aheadSum / s.aheadN : undefined,
      dropped: s.dropped, decoded: s.decoded,
      waits: s.waits, wait_ms: s.waitMs, freezes: s.freezes, freeze_ms: Math.round(s.freezeMs),
      jumps: s.jumps, late: s.late, late_max: Math.round(s.lateMax),
      w: this.v.videoWidth, h: this.v.videoHeight, ...this.extra?.(),
    });
    this.resetSum();
  }

  private resetSum() {
    this.sum = { aheadMin: Infinity, aheadSum: 0, aheadN: 0, dropped: 0, decoded: 0, waits: 0, waitMs: 0, freezes: 0, freezeMs: 0, jumps: 0, late: 0, lateMax: 0 };
  }
}

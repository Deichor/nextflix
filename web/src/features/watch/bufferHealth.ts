// Buffer health of the playing stream: how far the buffer reaches past the playback position and
// whether the download keeps pace with playback. Pure logic (no DOM), fed once per second by the player.

export type BufferState = 'healthy' | 'draining' | 'critical';

export interface BufferSample {
  /** Wall clock (ms) */
  at: number;
  /** Playback position in the file (s) */
  pos: number;
  /** End of the buffered range that holds pos (s); pos when nothing is buffered there */
  end: number;
  /** The browser is downloading (networkState LOADING) and did not suspend since the last sample.
   *  False: it stopped on its own (Chrome keeps only a few seconds of a progressive stream ahead) */
  fetching: boolean;
  /** The buffer reaches the end of the file: nothing left to download */
  atEnd?: boolean;
}

export interface BufferVerdict {
  state: BufferState;
  /** Seconds buffered ahead of the position */
  ahead: number;
  /** Buffered end growth per second of playback over the window; null until it spans enough playback */
  growth: number | null;
  /** How long the stream has been healthy (ms) since it last was draining or reset; 0 while not */
  healthyMs: number;
}

export const BUFFER_HEALTH = {
  /** Sliding window the growth is measured over */
  WINDOW_MS: 10000,
  /** Playback the window needs before its growth counts ("shrinking for 6 s") */
  MIN_SPAN_S: 6,
  /** Below this much buffer, any shrinking is draining */
  DRAIN_AHEAD_S: 25,
  /** Growth below this is draining whatever the buffer */
  DRAIN_GROWTH: 0.9,
  /** Below this much buffer while not keeping pace: critical, the stall is seconds away */
  CRITICAL_AHEAD_S: 6,
  /** A pause between samples longer than this (paused, hidden tab) restarts the window */
  GAP_MS: 3000,
  /** After the browser idled on its own, its download comes in bursts that keep a small lead: only judged
   *  again once it fetched without a break for this long */
  IDLE_GRACE_MS: 15000,
  /** Fetching with less than this buffered (also within the grace) is critical */
  IDLE_MIN_AHEAD_S: 1,
};

// End of the buffered range that contains t (a small gap before a range counts), or -1
export function bufferedEnd(r: { length: number; start(i: number): number; end(i: number): number }, t: number): number {
  for (let i = 0; i < r.length; i++) {
    if (t >= r.start(i) - 0.1 && t <= r.end(i)) return r.end(i);
  }
  return -1;
}

export class BufferMonitor {
  private win: BufferSample[] = [];
  private healthySince = 0;
  private idleAt = 0;

  /** Forget everything (new stream, seek, source switch) */
  reset() {
    this.win = [];
    this.healthySince = 0;
    this.idleAt = 0;
  }

  push(s: BufferSample): BufferVerdict {
    const H = BUFFER_HEALTH;
    const ahead = Math.max(0, s.end - s.pos);
    const last = this.win[this.win.length - 1];
    // A jump in position (seek) or a gap in sampling breaks the comparison
    if (last && (s.at - last.at > H.GAP_MS || s.pos < last.pos - 0.5 || s.pos - last.pos > (s.at - last.at) / 1000 * 2 + 2)) {
      this.reset();
    }
    // The browser paused the download itself: a shrinking buffer says nothing about the network
    // (stalls are handled by the player's 'waiting' path, not here)
    if (!s.fetching) {
      this.win = [];
      this.idleAt = s.at;
    }
    this.win.push(s);
    while (this.win.length > 2 && s.at - this.win[1].at >= H.WINDOW_MS) this.win.shift();

    const first = this.win[0];
    const span = s.pos - first.pos;
    const growth = span >= H.MIN_SPAN_S ? (s.end - first.end) / span : null;

    let state: BufferState = 'healthy';
    let known = growth !== null;
    if (s.atEnd || !s.fetching) {
      known = true;
    } else if (this.idleAt && s.at - this.idleAt < H.IDLE_GRACE_MS) {
      known = true;
      if (ahead < H.IDLE_MIN_AHEAD_S) state = 'critical';
    } else if (growth !== null) {
      if (growth < 1 && ahead < H.CRITICAL_AHEAD_S) state = 'critical';
      else if (growth < H.DRAIN_GROWTH || (growth < 1 && ahead < H.DRAIN_AHEAD_S)) state = 'draining';
    }

    // A window that is still filling neither starts nor ends a healthy stretch
    if (state !== 'healthy') this.healthySince = 0;
    else if (known && !this.healthySince) this.healthySince = s.at;
    return { state, ahead, growth, healthyMs: this.healthySince ? s.at - this.healthySince : 0 };
  }
}

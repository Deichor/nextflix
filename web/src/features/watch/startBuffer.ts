// Adaptive start buffer. A source whose swarm resolve measured below 1.3x the file's bitrate
// starts only once ~8 s are buffered, at most 4 s after it could have started; after a stall
// any source resumes with 4 s buffered (6 s at most), so a slow swarm does not freeze, play a
// second and freeze again. Pure logic, fed by the player's poll.

export const START_BUFFER = {
  /** rate / need at which a source starts at once */
  HEALTHY_RATIO: 1.3,
  /** Seconds buffered ahead before a borderline source starts */
  START_AHEAD_S: 8,
  /** Longest extra wait after the first frames were playable */
  START_MAX_WAIT_MS: 4000,
  /** Seconds buffered ahead before playback resumes after a stall */
  RESUME_AHEAD_S: 4,
  /** Longest hold after a stall */
  RESUME_MAX_WAIT_MS: 6000,
  /** The browser stopped downloading on its own with this much buffered: it will not fetch more */
  IDLE_AHEAD_S: 2,
};

export type HoldKind = 'start' | 'stall';

/** rate / need of the resolve answer's health; undefined when it was not measured */
export function healthRatio(h?: { rate: number; need: number; dead?: boolean } | null): number | undefined {
  if (!h) return undefined;
  if (h.dead) return 0;
  return h.need > 0 ? h.rate / h.need : undefined;
}

/** A source whose start waits for a larger buffer: measured and borderline (unknown starts at once) */
export function needsStartHold(ratio: number | undefined): boolean {
  return ratio !== undefined && ratio < START_BUFFER.HEALTHY_RATIO;
}

export interface HoldSample {
  /** Seconds buffered ahead of the position */
  ahead: number;
  /** ms since the hold could have ended (start: the first frames were playable; stall: the stall began) */
  waitedMs: number;
  /** networkState LOADING */
  fetching: boolean;
  /** The buffer reaches the end of the file */
  atEnd: boolean;
}

/** The hold of kind ends now: enough buffered, the file is in, the browser stopped fetching, or the cap passed */
export function holdDone(kind: HoldKind, s: HoldSample): boolean {
  const target = kind === 'start' ? START_BUFFER.START_AHEAD_S : START_BUFFER.RESUME_AHEAD_S;
  const cap = kind === 'start' ? START_BUFFER.START_MAX_WAIT_MS : START_BUFFER.RESUME_MAX_WAIT_MS;
  return s.ahead >= target || s.atEnd || s.waitedMs >= cap || (!s.fetching && s.ahead >= START_BUFFER.IDLE_AHEAD_S);
}

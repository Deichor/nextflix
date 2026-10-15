// Adaptive start buffer (port of web/src/features/watch/startBuffer.ts). A source whose swarm
// resolve measured below 2x the file's bitrate (or did not measure) starts only once ~20 s
// are buffered, at most 10 s after it could have started; after a stall any source resumes
// only with 8 s buffered, so a slow swarm does not freeze, play a second and freeze again.
// Pure logic, fed by the player's poll.

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

/**
 * An explicit starving verdict: keeps_up false with a measured rate below need (or a dead swarm). An answer
 * still measuring (`measuring`, or nothing sampled yet) only counts below half its need.
 */
export function healthStarving(
  h?: { rate: number; need: number; keeps_up?: boolean; dead?: boolean; measuring?: boolean } | null,
): boolean {
  if (!h || h.keeps_up !== false) return false;
  if (h.dead) return true;
  const limit = h.measuring ? 0.5 * h.need : h.need;
  return h.rate > 0 && h.need > 0 && h.rate < limit;
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

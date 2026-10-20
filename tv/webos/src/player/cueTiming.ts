// When the subtitle overlay looks the cues up again, and which cues show (tested in cueTiming.test.mjs).

interface Timed {
  start: number;
  end: number;
}

/** The overlay checks at least this often (seeks, pauses, a new cue list) */
export const CUE_CHECK_MAX_MS = 100;
/** ...and this long after a cue boundary, so the position has passed it */
const CUE_EDGE_MS = 4;

// Per cue list (sorted by start): the latest end of the cues up to each index, so a lookup goes back
// only as far as a cue still shows (a long sign or song cue under many short ones too)
const maxEnds = new WeakMap<readonly Timed[], Float64Array>();

function maxEndsOf(cues: readonly Timed[]): Float64Array {
  let m = maxEnds.get(cues);
  if (m && m.length === cues.length) return m;
  m = new Float64Array(cues.length);
  let max = -Infinity;
  for (let i = 0; i < cues.length; i++) {
    max = Math.max(max, cues[i].end);
    m[i] = max;
  }
  maxEnds.set(cues, m);
  return m;
}

/** Index after the last cue starting at or before t */
function startsUpTo(cues: readonly Timed[], t: number): number {
  let lo = 0;
  let hi = cues.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (cues[mid].start <= t) lo = mid + 1; else hi = mid;
  }
  return lo;
}

/** Cues showing at time t (cues sorted by start), in start order. */
export function activeCues<T extends Timed>(cues: readonly T[], t: number): T[] {
  const ends = maxEndsOf(cues);
  const out: T[] = [];
  for (let i = startsUpTo(cues, t) - 1; i >= 0 && ends[i] > t; i--) {
    if (cues[i].end > t) out.push(cues[i]);
  }
  return out.reverse();
}

/**
 * Seconds from t until the cues shown change (the next start, or the end of a shown cue); Infinity when
 * nothing changes any more. Cues sorted by start.
 */
export function nextCueChange(cues: readonly Timed[], t: number): number {
  const ends = maxEndsOf(cues);
  const lo = startsUpTo(cues, t);
  let next = lo < cues.length ? cues[lo].start : Infinity;
  for (let i = lo - 1; i >= 0 && ends[i] > t; i--) {
    if (cues[i].end > t) next = Math.min(next, cues[i].end);
  }
  return next - t;
}

/**
 * Delay of the next check: right after the next boundary (a cue shows within a few ms of its time
 * instead of up to a 100 ms poll later), at most CUE_CHECK_MAX_MS.
 */
export function cueCheckDelay(cues: readonly Timed[], t: number): number {
  const ms = Math.round(nextCueChange(cues, t) * 1000) + CUE_EDGE_MS;
  return Math.max(CUE_EDGE_MS, Math.min(CUE_CHECK_MAX_MS, ms));
}

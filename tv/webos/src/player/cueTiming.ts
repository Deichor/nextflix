// When the subtitle overlay looks the cues up again (tested in cueTiming.test.mjs).

interface Timed {
  start: number;
  end: number;
}

/** The overlay checks at least this often (seeks, pauses, a new cue list) */
export const CUE_CHECK_MAX_MS = 100;
/** ...and this long after a cue boundary, so the position has passed it */
const CUE_EDGE_MS = 4;

/**
 * Seconds from t until the cues shown change (the next start, or the end of a shown cue); Infinity when
 * nothing changes any more. Cues sorted by start; overlaps looked back as far as activeCues does.
 */
export function nextCueChange(cues: Timed[], t: number): number {
  let lo = 0;
  let hi = cues.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (cues[mid].start <= t) lo = mid + 1; else hi = mid;
  }
  let next = lo < cues.length ? cues[lo].start : Infinity;
  for (let i = lo - 1; i >= 0 && i > lo - 13; i--) {
    if (cues[i].end > t) next = Math.min(next, cues[i].end);
  }
  return next - t;
}

/**
 * Delay of the next check: right after the next boundary (a cue shows within a few ms of its time
 * instead of up to a 100 ms poll later), at most CUE_CHECK_MAX_MS.
 */
export function cueCheckDelay(cues: Timed[], t: number): number {
  const ms = Math.round(nextCueChange(cues, t) * 1000) + CUE_EDGE_MS;
  return Math.max(CUE_EDGE_MS, Math.min(CUE_CHECK_MAX_MS, ms));
}

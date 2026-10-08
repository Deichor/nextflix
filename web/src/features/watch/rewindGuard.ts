// A restarted or replaced stream never jumps back to the start of the title on its own:
// once playback got past REWIND_GUARD_S, a start at 0 that the viewer did not ask for
// (a source switch, a recovery, a retry) resumes at the last known position instead.

export const REWIND_GUARD_S = 30;

/**
 * Where a stream (re)starts. `target` is the requested start, `known` the last position that
 * really played; `userAsked` is a seek of the viewer, which always wins.
 */
export function guardedStart(target: number, known: number, userAsked = false): number {
  const t = Number.isFinite(target) && target > 0 ? target : 0;
  if (userAsked || t >= 1 || !(known > REWIND_GUARD_S)) return t;
  return known;
}

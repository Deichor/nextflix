// Safari restarts the playing element for a switch or recovery. Copied video starts on the keyframe
// before the position (up to ~10 s earlier), so the element seeks forward inside the new stream,
// hidden, before anything shows: the viewer never sees those seconds again. Pure logic.

/** A smaller gap is not worth a seek (the feed lines up the first frame itself) */
export const CATCH_UP_MIN_S = 0.3;
/** Data needed past a seek target */
const END_PAD_S = 0.2;
/** The feed reads on only while little is buffered ahead (10 s while ManagedMediaSource has
 *  enough): with this much ahead and the target still out of reach, the element steps forward */
const STEP_AHEAD_S = 8;

interface Ranges {
  length: number;
  start(i: number): number;
  end(i: number): number;
}

/** Element time that shows file time `target` in a stream that starts at file time `start`; 0 when nothing to skip */
export function catchUpTime(target: number, start: number): number {
  const rel = target - start;
  return rel > CATCH_UP_MIN_S ? rel : 0;
}

/**
 * Next seek towards element time `rel` from `current`: `last` once the buffer reaches it, else a
 * step through what arrived when the feed would wait for playback. null: wait for more data.
 */
export function catchUpStep(rel: number, current: number, buffered: Ranges): { to: number; last: boolean } | null {
  for (let i = 0; i < buffered.length; i++) {
    if (rel >= buffered.start(i) - 0.1 && rel <= buffered.end(i) - END_PAD_S) return { to: rel, last: true };
  }
  for (let i = 0; i < buffered.length; i++) {
    const end = buffered.end(i);
    if (current >= buffered.start(i) - 0.1 && current <= end && current < rel && end - current >= STEP_AHEAD_S) {
      return { to: end - 1, last: false };
    }
  }
  return null;
}

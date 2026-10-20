// Switching between releases of one title (upgrade, sidegrade): their lengths differ a little
// (Game of Thrones S01E01: the WEB-DL runs 3695.1 s, the BluRay 3702.1 s). Up to
// max(30 s, 1 %) apart they are the same title; under KEEP_TIME_S apart the same time is the
// same moment, beyond that a position is mapped by the length ratio.
// Port of web/src/features/watch/switchLength.ts.

const SAME_TITLE_S = 30;
const SAME_TITLE_SHARE = 0.01;
const KEEP_TIME_S = 10;

/** The new file can replace the playing one (lengths in seconds; unknown lengths always fit). */
export function lengthsMatch(playing: number, next: number): boolean {
  if (playing <= 0 || next <= 0) return true;
  return Math.abs(next - playing) <= Math.max(SAME_TITLE_S, SAME_TITLE_SHARE * Math.max(playing, next));
}

/** Factor from a position in the playing file to the same moment in the new one. */
export function timeScale(playing: number, next: number): number {
  if (playing <= 0 || next <= 0 || Math.abs(next - playing) <= KEEP_TIME_S) return 1;
  return next / playing;
}

/** A switch this far in never starts over: a file that cannot take over sends the player back */
const GUARD_FROM_S = 30;
/** The new file has to reach the position by this much */
const END_MARGIN_S = 10;

/**
 * Where a switch (upgrade, sidegrade, step down) from the playing file at `at` starts in the new one, or
 * null when the new file cannot take over (it does not reach the moment, or it is not the same length:
 * a pack's wrong file, another cut) and the player goes back to the file it came from at `at`.
 */
export function switchStart(playing: number, next: number, at: number): number | null {
  const same = lengthsMatch(playing, next);
  const start = same ? at * timeScale(playing, next) : at;
  if (at > GUARD_FROM_S && next > 0 && (start >= next - END_MARGIN_S || !same)) return null;
  return start;
}

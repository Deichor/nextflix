// Switching between releases of one title (upgrade, sidegrade): their lengths differ a little
// (Game of Thrones S01E01: the WEB-DL runs 3695.1 s, the BluRay 3702.1 s). Up to
// max(30 s, 1 %) apart they are the same title; under KEEP_TIME_S apart the same time is the
// same moment, beyond that a position is mapped by the length ratio.
// Ported to tv/webos/src/player/switchLength.ts.

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

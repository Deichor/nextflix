// Pure decisions of the player engine (engine.ts), tested in playRules.test.mjs.

/** A start this close to the end counts as the end (seconds) */
export const END_ZONE_S = 10;

/**
 * Where an opened source starts. A first resume at the very end watches the title again from 0; a
 * switch or recovery that lands there means the title is over ('end'), never a replay from 0.
 */
export function openStart(startAt: number, duration: number, firstResume: boolean): number | 'end' {
  if (startAt <= 0) return 0;
  if (duration <= 0 || startAt < duration - END_ZONE_S) return startAt;
  return firstResume ? 0 : 'end';
}

/**
 * An answer of a subtitle file. Empty: another subtitle only when this one never answered (a quota or
 * broken file); a live track that already answered asks again (a network error or 5xx while polling).
 * `loads`: earlier answers that got through.
 */
export function cueAnswer(cues: number, partial: boolean, loads: number): 'show' | 'fallback' | 'retry' {
  if (cues > 0 || partial) return 'show';
  return loads === 0 ? 'fallback' : 'retry';
}

/**
 * Wait before asking a subtitle file again after its n-th server or network error in a row (n >= 1):
 * 5 s, doubling up to 5 minutes. Such errors are often over in seconds (an AI translation's first answer).
 */
export function subtitleRetryMs(n: number): number {
  return Math.min(5 * 60 * 1000, 5000 * 2 ** Math.max(0, n - 1));
}

/** Seeks act only on an opened source: not while another one is looked up, nor after a fatal error */
export function canSeek(s: { phase: string; fatal: unknown; duration: number }, hash: string): boolean {
  return !!hash && s.phase === 'ready' && !s.fatal && s.duration > 0;
}

export interface SaveInput {
  finished: boolean;
  /** Something of this item played (kept across source switches, unlike the per-source flag) */
  itemPlayed: boolean;
  duration: number;
  /** Position is exact (else the stream's start is the best known position) */
  exact: boolean;
  position: number;
  offset: number;
}

/** Position to save, or null: nothing played of the item yet (a failed start never overwrites progress) or under 10 s */
export function progressToSave(x: SaveInput): number | null {
  if (x.duration <= 0 || (!x.finished && !x.itemPlayed)) return null;
  const pos = x.finished ? x.duration : x.exact ? Math.min(x.duration, x.position) : x.offset;
  return pos < 10 ? null : pos;
}

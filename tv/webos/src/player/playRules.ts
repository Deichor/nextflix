// Pure decisions of the player engine (engine.ts), tested in playRules.test.mjs.

import type { QualityCap } from '../lib/stream';

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

/** The cap below a source of this resolution, or null at 720p and lower (the server's lowest maxq) */
export function lowerCap(q: number): QualityCap | null {
  return q >= 2160 ? 1080 : q >= 1080 ? 720 : null;
}

/** How a torrent without a first frame is replaced (handoverPlan) */
export interface HandoverPlan {
  /** maxq of the resolve; undefined: uncapped */
  maxq?: QualityCap;
  /** maxq steps a 4K source down to 1080p (the item keeps that cap when its answer plays well) */
  step: boolean;
  /** The last handover: it resolves without the torrents earlier handovers left (one of them may have woken up) */
  last: boolean;
}

/**
 * The n-th handover (`handovers` before it) of a torrent without a first frame, null once `max` ran. It never
 * asks above the starving source: a 4K steps down to 1080p (unless 1080p found nothing, `empty`), a 1080p or
 * 720p swarm is replaced at most at its own resolution (a slow swarm is no proof the line can't carry it; the
 * stall step down still runs once it plays). An unknown resolution keeps the cap.
 */
export function handoverPlan(quality: number, cap: QualityCap | undefined, handovers: number, max: number, empty: QualityCap[] = []): HandoverPlan | null {
  if (handovers >= max) return null;
  const last = handovers === max - 1;
  if (quality > 1080 && (!cap || cap > 1080) && empty.indexOf(1080) < 0) return { maxq: 1080, step: true, last };
  const own: QualityCap | undefined = quality > 1080 || quality <= 0 ? undefined : quality >= 1080 ? 1080 : 720;
  return { maxq: own && (!cap || own < cap) ? own : cap, step: false, last };
}

/**
 * Whether the answer of a handover resolve at a lowered cap may keep that cap: it keeps up, or is unmeasured,
 * or still measures at its need or above. A starving answer plays but leaves the cap where it was.
 */
export function keepsLowerCap(h?: { rate: number; need: number; keeps_up?: boolean; dead?: boolean } | null): boolean {
  if (!h || h.keeps_up) return true;
  return !h.dead && h.need > 0 && h.rate >= h.need;
}

import type { Cue } from '@/features/watch/subtitles';

/** Cues showing at time t (cues must be sorted by start). */
export function activeCues(cues: Cue[], t: number): Cue[] {
  let lo = 0;
  let hi = cues.length - 1;
  let last = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (cues[mid].start <= t) { last = mid; lo = mid + 1; } else hi = mid - 1;
  }
  const out: Cue[] = [];
  // Overlapping cues are rare and short; looking back a few is enough
  for (let i = last; i >= 0 && i > last - 12; i--) {
    if (cues[i].end > t) out.unshift(cues[i]);
  }
  return out;
}

/** The overlay's lines at their last change: the list they came from and the cue time then */
export interface ShownCues {
  cues: Cue[];
  t: number;
  shown: Cue[];
}

// A new list (a live track reloading, another track) is looked up at the last known time until
// the next frame does it at the current one: no blank frame, and never the old list's lines.
export function shownCues(active: ShownCues | null, cues: Cue[]): Cue[] {
  if (!active || cues.length === 0) return [];
  return active.cues === cues ? active.shown : activeCues(cues, active.t);
}

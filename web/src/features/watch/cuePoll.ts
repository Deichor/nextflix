// When the player asks a subtitle file again. Pure, tested in cuePoll.test.mjs; same rules as the TV's
// tv/webos/src/player/playRules.ts (finalCues, cuePollMs).

// A subtitle file is asked again this often while partial (a live track grows, the timing runs)...
export const LIVE_CUES_POLL_MS = 15000;
// ...while still untimed in the first minutes of its viewing (the first timing comes seconds after the
// played audio allows it)...
export const PENDING_CUES_POLL_MS = 5000;
export const PENDING_FAST_FOR_MS = 3 * 60 * 1000;
// ...while an AI translation still misses the lines at the position...
export const AI_CUES_POLL_MS = 3000;
// ...and while the viewer watches, when complete but its timing may still change (live, pending, failed)
export const PROVISIONAL_CUES_POLL_MS = 60000;

// X-Subtitle-Sync states whose timing no longer changes (docs/API.md)
const FINAL_SYNC = ['synced', 'unchanged', 'replaced', 'poor', 'original'];

/** An answer of a subtitle file that will not change: complete and finally timed (only these are kept). */
export function finalCues(f: { partial: boolean; sync: string }): boolean {
  return !f.partial && FINAL_SYNC.includes(f.sync);
}

/** When to ask a subtitle file again (ms), null once its answer is final; shownMs: since the subtitle was put on. */
export function cuePollMs(f: { partial: boolean; sync: string; translating?: boolean }, shownMs = Infinity): number | null {
  if (f.partial) {
    if (f.translating) return AI_CUES_POLL_MS;
    return f.sync === 'pending' && shownMs < PENDING_FAST_FOR_MS ? PENDING_CUES_POLL_MS : LIVE_CUES_POLL_MS;
  }
  return finalCues(f) ? null : PROVISIONAL_CUES_POLL_MS;
}

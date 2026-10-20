// How far the video element's clock runs ahead of the picture and sound (tested in clockLead.test.mjs).
// webOS restarts currentTime at a seek's target, and within ~20 ms of playing it reports about 0.25 s
// more, then advances at rate 1 (LG B5, webOS 25: 0.21-0.29 s over 12 seeks; paused and resumed without
// a seek it does not jump): the pipeline counts audio handed to the sound output, not heard yet (Kodi
// uses -275 ms on webOS). Cues drawn at currentTime showed that much early, so subtitles subtract it.
// It is measured after every seek on the device itself; Chrome measures about 0.

/** Used until a seek was measured (webOS only) */
export const DEFAULT_LEAD_S = 0.25;
/** A sample over this is a stall or a missed event, not the clock */
const MAX_LEAD_S = 0.6;
/** Samples kept (the median is used) */
const KEEP = 7;
/** How long after playing a sample is taken (the jump comes within ~30 ms) */
export const LEAD_SAMPLE_MS = 1500;

/** One sample: currentTime that long after a seek's playback started, minus where it should be. */
export function leadSample(target: number, ct: number, elapsedS: number): number | null {
  const lead = ct - target - elapsedS;
  if (!Number.isFinite(lead) || lead < -0.05 || lead > MAX_LEAD_S) return null;
  return Math.max(0, Math.round(lead * 1000) / 1000);
}

export function addSample(samples: number[], x: number): number[] {
  return [...samples, x].slice(-KEEP);
}

/** The lead to use: the median of the samples, else the default */
export function leadOf(samples: number[], fallback: number): number {
  if (!samples.length) return fallback;
  const s = [...samples].sort((a, b) => a - b);
  return s.length % 2 ? s[s.length >> 1] : (s[s.length / 2 - 1] + s[s.length / 2]) / 2;
}

/**
 * What restarted the clock: a seek inside the buffer (the playing pipeline only jumps), or a new muxed or
 * split stream. Seeks are measured cleanest, so their samples win; a new stream's own count only without them.
 */
export type LeadKind = 'seek' | 'muxed' | 'split';
export type LeadSamples = Record<LeadKind, number[]>;

const KINDS: LeadKind[] = ['seek', 'muxed', 'split'];

export function addKindSample(all: LeadSamples, kind: LeadKind, x: number): LeadSamples {
  return { ...all, [kind]: addSample(all[kind], x) };
}

/** The lead for a stream of this mode: the seeks' median, else the mode's own, else the fallback */
export function leadFor(all: LeadSamples, mode: Exclude<LeadKind, 'seek'>, fallback: number): number {
  return leadOf(all.seek.length ? all.seek : all[mode], fallback);
}

const valid = (v: unknown): number[] =>
  Array.isArray(v) ? v.filter(x => typeof x === 'number' && x >= 0 && x <= MAX_LEAD_S).slice(-KEEP) : [];

/** Stored samples by kind (anything else read as none) */
export function parseSamples(raw: unknown): LeadSamples {
  const o = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw as Record<string, unknown> : {};
  return { seek: valid(o.seek), muxed: valid(o.muxed), split: valid(o.split) };
}

// Samples by kind; the older key held untagged samples taken when the `playing` handler ran (late under load)
const KEY = 'nfx-tv:clock-lead2';
const OLD_KEY = 'nfx-tv:clock-lead';

export function loadSamples(): LeadSamples {
  try {
    localStorage.removeItem(OLD_KEY);
    return parseSamples(JSON.parse(localStorage.getItem(KEY) || '{}'));
  } catch {
    return parseSamples(null);
  }
}

export function saveSamples(all: LeadSamples): void {
  try {
    const out: Partial<LeadSamples> = {};
    for (const k of KINDS) if (all[k].length) out[k] = all[k];
    localStorage.setItem(KEY, JSON.stringify(out));
  } catch {
    /* storage full or off */
  }
}

/**
 * When an event happened on the performance.now() clock: its timeStamp (a handler may run well after it on a
 * busy TV), else now when the engine stamps events otherwise (epoch ms on old engines, or 0).
 */
export function eventTime(timeStamp: number, now: number): number {
  return timeStamp > 0 && timeStamp <= now ? timeStamp : now;
}

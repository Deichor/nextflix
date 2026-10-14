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

const KEY = 'nfx-tv:clock-lead';

export function loadSamples(): number[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) || '[]');
    return Array.isArray(v) ? v.filter(x => typeof x === 'number' && x >= 0 && x <= MAX_LEAD_S).slice(-KEEP) : [];
  } catch {
    return [];
  }
}

export function saveSamples(samples: number[]): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(samples));
  } catch {
    /* storage full or off */
  }
}

// Opening intro: settings, cold-launch gate and the two-hit sting synthesized with Web Audio (no audio files).
import { readJson, writeJson } from './storage';

export interface IntroPrefs {
  animation: boolean;
  sound: boolean;
}

const PREFS_KEY = 'nfx-tv:intro';

export function readIntroPrefs(): IntroPrefs {
  const p = readJson<Partial<IntroPrefs>>(PREFS_KEY) || {};
  return { animation: p.animation !== false, sound: p.sound !== false };
}

export function writeIntroPrefs(p: IntroPrefs): void {
  writeJson(PREFS_KEY, p);
}

/** Timeline in seconds; the animation and the sting share it. */
export const INTRO = { duration: 2.8, hit1: 0.3, hit2: 1.05, soundEnd: 2.55 };

let taken = false;

/**
 * What the intro does on this launch, or null. Only the first call per page load gets an answer:
 * relaunching from the background or remounting never replays it.
 */
export function takeIntro(): { animate: boolean; sound: boolean } | null {
  if (taken) return null;
  taken = true;
  const p = readIntroPrefs();
  let reduced = false;
  try {
    reduced = !!window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  } catch {
    /* old engine */
  }
  const animate = p.animation && !reduced;
  return animate || p.sound ? { animate, sound: p.sound } : null;
}

/** Old engines (webOS 4/5) or few cores: fewer ribbons, no extra layers. */
export function liteIntro(): boolean {
  const m = navigator.userAgent.match(/Chrome\/(\d+)/);
  const major = m ? Number(m[1]) : 0;
  const cores = navigator.hardwareConcurrency || 0;
  return (major > 0 && major < 79) || (cores > 0 && cores <= 2);
}

// ---- Launch visibility ----
// A CSS animation runs on the clock, not on frames: started while the TV draws nothing it plays
// out unseen. On webOS the page is already 'visible' behind LG's splash, and the first frames
// after launch can stall (measured on an LG TV with Chrome 120: 1-1.4 s while the intro's canvases
// first reach the GPU). The intro waits until frames are drawn steadily, plus the splash's fade-out.

const STEADY_FRAMES = 4;
const FRAME_GAP_MS = 100;
const SPLASH_FADE_MS = 250;
const ON_SCREEN_CAP_MS = 8000;

/** Launch timeline (ms since navigation), readable from DevTools as window.__nxBoot */
const boot: string[] = [];
export const markBoot = (what: string) => { boot.push(`${Math.round(performance.now())} ${what}`); };
markBoot(`script ${document.visibilityState}`);
document.addEventListener('visibilitychange', () => markBoot(`visibility ${document.visibilityState}`));
document.addEventListener('webOSLaunch', () => markBoot('webOSLaunch'));
window.addEventListener('load', () => markBoot('load'));
(window as unknown as { __nxBoot: string[] }).__nxBoot = boot;

let onScreen: Promise<void> | null = null;

/** Resolves once the app is really on screen (at most ~8 s after the first call). */
export function whenOnScreen(): Promise<void> {
  if (!onScreen) {
    onScreen = new Promise<void>(resolve => {
      let done = false;
      let last = 0;
      let steady = 0;
      const finish = (why: string) => {
        if (done) return;
        done = true;
        clearTimeout(cap);
        markBoot(`on screen (${why})`);
        setTimeout(resolve, why === 'frames' && isWebOSPage() ? SPLASH_FADE_MS : 0);
      };
      const tick = () => {
        if (done) return;
        const now = performance.now();
        const gap = last ? now - last : 0;
        if (gap > FRAME_GAP_MS) markBoot(`frame gap ${Math.round(gap)} ms`);
        steady = last && gap <= FRAME_GAP_MS && document.visibilityState === 'visible' ? steady + 1 : 0;
        last = now;
        if (steady >= STEADY_FRAMES) finish('frames');
        else requestAnimationFrame(tick);
      };
      const cap = setTimeout(() => finish('cap'), ON_SCREEN_CAP_MS);
      requestAnimationFrame(tick);
    });
  }
  return onScreen;
}

const isWebOSPage = () => {
  const w = window as unknown as { webOSSystem?: unknown; PalmSystem?: unknown };
  return !!(w.webOSSystem || w.PalmSystem);
};

// ---- Sound ----

type Ctx = BaseAudioContext;

// Output level: puts the loudest moment (the second hit) near -12 dBFS
const MASTER = 0.2;

function noise(ctx: Ctx, seconds: number, channels = 1, decay = 0): AudioBuffer {
  const len = Math.max(1, Math.floor(ctx.sampleRate * seconds));
  const buf = ctx.createBuffer(channels, len, ctx.sampleRate);
  for (let c = 0; c < channels; c++) {
    const d = buf.getChannelData(c);
    for (let i = 0; i < len; i++) {
      const env = decay ? Math.pow(1 - i / len, decay) : 1;
      d[i] = (Math.random() * 2 - 1) * env;
    }
  }
  return buf;
}

/** Exponential envelope: fast attack, decay to silence by t + len. */
function env(ctx: Ctx, t: number, level: number, attack: number, len: number): GainNode {
  const g = ctx.createGain();
  g.gain.setValueAtTime(0.0001, t);
  g.gain.exponentialRampToValueAtTime(level, t + attack);
  g.gain.exponentialRampToValueAtTime(0.0001, t + len);
  return g;
}

function tone(ctx: Ctx, out: AudioNode, type: OscillatorType, t: number, f0: number, f1: number, drop: number, level: number, len: number) {
  const o = ctx.createOscillator();
  o.type = type;
  o.frequency.setValueAtTime(f0, t);
  o.frequency.exponentialRampToValueAtTime(f1, t + drop);
  const g = env(ctx, t, level, 0.006, len);
  o.connect(g);
  g.connect(out);
  o.start(t);
  o.stop(t + len + 0.05);
}

/** Low thump with a pitch drop, a body an octave up and a soft filtered noise click. */
function hit(ctx: Ctx, out: AudioNode, t: number, f: number, len: number, level: number) {
  tone(ctx, out, 'sine', t, f * 2.4, f, 0.12, level, len);
  tone(ctx, out, 'triangle', t, f * 4, f * 2, 0.08, level * 0.32, len * 0.55);
  const src = ctx.createBufferSource();
  src.buffer = noise(ctx, 0.12);
  const lp = ctx.createBiquadFilter();
  lp.type = 'lowpass';
  lp.frequency.value = 2200;
  const g = env(ctx, t, level * 0.22, 0.002, 0.09);
  src.connect(lp);
  lp.connect(g);
  g.connect(out);
  src.start(t);
}

/** Warm open-fifth pad that swells after the second hit, its filter opening and closing. */
function swell(ctx: Ctx, out: AudioNode, t: number, len: number, level: number) {
  const lp = ctx.createBiquadFilter();
  lp.type = 'lowpass';
  lp.Q.value = 0.7;
  lp.frequency.setValueAtTime(220, t);
  lp.frequency.exponentialRampToValueAtTime(1500, t + 0.35);
  lp.frequency.exponentialRampToValueAtTime(300, t + len);
  const g = ctx.createGain();
  g.gain.setValueAtTime(0.0001, t);
  g.gain.exponentialRampToValueAtTime(level, t + 0.28);
  g.gain.exponentialRampToValueAtTime(0.0001, t + len);
  lp.connect(g);
  g.connect(out);
  // D2, A2, D3, A3 with a slight detune for width
  const notes = [73.42, 110, 146.83, 220.0];
  for (let i = 0; i < notes.length; i++) {
    for (const det of [-6, 6]) {
      const o = ctx.createOscillator();
      o.type = i < 2 ? 'triangle' : 'sawtooth';
      o.frequency.value = notes[i];
      o.detune.value = det;
      const og = ctx.createGain();
      og.gain.value = i < 2 ? 0.3 : 0.1;
      o.connect(og);
      og.connect(lp);
      o.start(t);
      o.stop(t + len + 0.05);
    }
  }
}

/**
 * Schedules the sting so that the intro's t = 0 is `t0` in context time. Returns the master gain
 * (ramp it down to stop early). Peak is about -12 dBFS.
 */
export function scheduleSting(ctx: Ctx, t0: number): GainNode {
  const master = ctx.createGain();
  master.gain.value = MASTER;
  master.connect(ctx.destination);

  const bus = ctx.createGain();
  bus.connect(master);
  const verb = ctx.createConvolver();
  verb.buffer = noise(ctx, 1.1, 2, 3.2);
  const wet = ctx.createGain();
  wet.gain.value = 0.28;
  bus.connect(verb);
  verb.connect(wet);
  wet.connect(master);

  const now = ctx.currentTime;
  const h1 = t0 + INTRO.hit1;
  const h2 = t0 + INTRO.hit2;
  if (h1 >= now) hit(ctx, bus, h1, 46, 0.55, 0.85);
  if (h2 >= now) {
    hit(ctx, bus, h2, 55, 1.0, 1);
    swell(ctx, bus, h2 + 0.02, 1.35, 0.5);
  }
  const end = t0 + INTRO.soundEnd;
  master.gain.setValueAtTime(MASTER, Math.max(now, end - 0.25));
  master.gain.linearRampToValueAtTime(0, Math.max(now, end));
  return master;
}

type AudioCtor = new () => AudioContext;

function audioCtor(): AudioCtor | undefined {
  const w = window as Window & { webkitAudioContext?: AudioCtor };
  return (window.AudioContext as AudioCtor | undefined) || w.webkitAudioContext;
}

// Browsers only start audio after a user gesture; this context is created inside one
let shared: AudioContext | null = null;

/** Call inside a click / key handler (picking a profile) so the sting can sound in a browser. */
export function unlockAudio(): void {
  try {
    const Ctor = audioCtor();
    if (!shared && Ctor) shared = new Ctor();
    if (shared && shared.resume) shared.resume().catch(() => {});
  } catch {
    shared = null;
  }
}

export interface StingHandle {
  stop(fadeMs?: number): void;
}

/**
 * Plays the sting with the intro starting `elapsed` seconds ago. Silent when Web Audio is missing
 * or the context stays suspended.
 */
export function playSting(elapsed = 0): StingHandle {
  const Ctor = audioCtor();
  let ctx: AudioContext | null = null;
  let master: GainNode | null = null;
  let stopped = false;
  const started = Date.now();
  const close = () => {
    const c = ctx;
    ctx = null;
    // The unlocked context is kept for later stings (a new one would be blocked again)
    if (c && c !== shared && c.close) c.close().catch(() => {});
  };
  const start = () => {
    if (stopped || !ctx || ctx.state !== 'running') return;
    const t0 = ctx.currentTime - elapsed - (Date.now() - started) / 1000;
    master = scheduleSting(ctx, t0);
    setTimeout(close, Math.max(0, (t0 + INTRO.soundEnd - ctx.currentTime) * 1000) + 200);
  };
  try {
    if (shared) ctx = shared;
    else if (Ctor) ctx = new Ctor();
  } catch {
    ctx = null;
  }
  if (ctx) {
    if (ctx.state === 'running') start();
    else {
      // Autoplay policy: give resume() a moment, otherwise stay silent
      const c = ctx;
      Promise.resolve(c.resume ? c.resume() : undefined).then(start, close);
      setTimeout(() => { if (!master && ctx === c) close(); }, 600);
    }
  }
  return {
    stop(fadeMs = 120) {
      stopped = true;
      const c = ctx;
      if (!c) return;
      if (master) {
        const now = c.currentTime;
        master.gain.cancelScheduledValues(now);
        master.gain.setValueAtTime(master.gain.value, now);
        master.gain.linearRampToValueAtTime(0, now + fadeMs / 1000);
      }
      setTimeout(close, fadeMs + 50);
    },
  };
}

// Anime4K upscaling drawn on a canvas over the <video> (anime4k.js, a WebGL port of the mpv shaders).
// WebGL, not WebGPU: WebGPU needs a secure context and the LAN app runs on plain http.

import type { Anime4KShader, PassThrough, TextureData } from 'anime4k.js';
import type { Mode, Tier } from '@/features/watch/anime4kShaders';
import type { UpscaleNote } from '@/features/watch/upscaleLog';

type Shaders = typeof import('@/features/watch/anime4kShaders');

// Watchdog step downs: extra (secondary mode, HQ) -> M -> S -> stop
const NEXT_TIER: Record<Tier, Tier | null> = { X: 'M', M: 'S', S: null };

/** Why upscaling turned itself off */
export type UpscaleStop = 'unsupported' | 'slow' | 'failed';

/** The chain that runs: mode A/B/C picked from the video, secondary (A+A, B+B, C+A) for extra */
export interface UpscaleMode {
  mode: Mode;
  secondary: boolean;
  /** Extra is on but this screen doesn't upscale the video x2, so the primary mode runs */
  extraIdle: boolean;
  /** Video lines (height, or width for wide pictures) */
  lines: number;
}

export interface UpscalerOptions {
  onStop: (why: UpscaleStop) => void;
  /** The first upscaled frame is on the canvas */
  onReady?: () => void;
  /** Use the secondary mode (A+A, B+B, C+A) where the screen allows */
  extra?: boolean;
  /** The watchdog stepped down from the secondary mode */
  onExtraDropped?: () => void;
  /** The chain changed (first frame, another source, a step down) */
  onMode?: (m: UpscaleMode) => void;
  /** Release year: SD video of an old title is a real SD master (Mode A), not a downscale (Mode C) */
  year?: number;
  note?: UpscaleNote;
}

// Before this, SD anime was made in SD
const OLD_SD_YEAR = 2009;
// Anime4K: secondary modes only for x2 or more
const SECONDARY_MIN_SCALE = 1.9;

/**
 * Anime4K's advice per source: A for most 1080p (and old SD), B for most 720p, C for SD that was
 * downscaled from an HD master (modern 480p releases).
 */
export function pickMode(width: number, height: number, year?: number): { mode: Mode; lines: number } {
  const lines = Math.round(Math.max(height, (width * 9) / 16));
  if (lines >= 900) return { mode: 'A', lines };
  if (lines >= 600) return { mode: 'B', lines };
  return { mode: year && year < OLD_SD_YEAR ? 'A' : 'C', lines };
}

// Watchdog: out of WATCH_FRAMES presented frames, more than MAX_SKIPPED missed by the loop means
// the GPU can't keep up (one step down in NEXT_LEVEL)
const WATCH_FRAMES = 240;
const MAX_SKIPPED = 0.25;
// Not judged right after the shaders were built: anime4k.js compiles them synchronously and
// ANGLE (Windows) finishes the big ones on the first draws, which freezes the loop for a while
const WARMUP_MS = 2500;
// A longer gap between two frames is a pause of the page (hidden tab, a stall), not GPU load
const GAP_MS = 500;
// The x2 pass never goes past twice the video size
const MAX_SCALE = 2;

const REQUIRED = ['OES_texture_float', 'OES_texture_float_linear', 'OES_texture_half_float', 'EXT_color_buffer_half_float'];

function enableExtensions(gl: WebGLRenderingContext): boolean {
  const ok = REQUIRED.every(name => !!gl.getExtension(name));
  // Linear sampling of half-float textures; not every GPU lists it
  gl.getExtension('OES_texture_half_float_linear');
  return ok;
}

let supported: boolean | null = null;

/** WebGL with the half-float render targets the shaders need */
export function upscaleSupported(): boolean {
  if (supported !== null) return supported;
  try {
    const gl = document.createElement('canvas').getContext('webgl');
    supported = !!gl && enableExtensions(gl);
    gl?.getExtension('WEBGL_lose_context')?.loseContext();
  } catch {
    supported = false;
  }
  return supported;
}

// "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)" -> GPU name and backend
function gpuInfo(gl: WebGLRenderingContext): { gpu: string; api: string } {
  const ext = gl.getExtension('WEBGL_debug_renderer_info');
  const raw = String(gl.getParameter(ext ? ext.UNMASKED_RENDERER_WEBGL : gl.RENDERER) || '');
  const m = /^ANGLE \((.*)\)$/.exec(raw);
  if (!m) return { gpu: raw, api: '' };
  const parts = m[1].split(', ');
  return { gpu: (parts[1] || parts[0]).replace(/ (Direct3D|vs_|ps_|OpenGL|\(0x).*$/, ''), api: parts[2] || '' };
}

function makeTexture(gl: WebGLRenderingContext): WebGLTexture {
  const tex = gl.createTexture();
  if (!tex) throw new Error('createTexture failed');
  gl.bindTexture(gl.TEXTURE_2D, tex);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
  gl.bindTexture(gl.TEXTURE_2D, null);
  return tex;
}

const errName = (e: unknown) => (e instanceof Error ? e.name === 'Error' ? e.message : e.name : 'unknown');
const r1 = (n: number) => Math.round(n * 10) / 10;

interface Gpu {
  gl: WebGLRenderingContext;
  framebuffer: WebGLFramebuffer;
  input: WebGLTexture;
  // Only their sizes are read (mpv's NATIVE and OUTPUT)
  native: WebGLTexture;
  output: WebGLTexture;
  passthrough: PassThrough;
}

/**
 * Renders the given video through Anime4K into the canvas on every new frame. Calls onStop once
 * (and stops) when it can't run or can't keep up.
 */
export class Upscaler {
  private gpu: Gpu | null = null;
  // The canvas context, also when the GPU setup failed (lost on destroy)
  private gl: WebGLRenderingContext | null = null;
  private shaders: Shaders | null = null;
  private programs: Anime4KShader[] = [];
  private tier: Tier;
  // What the programs were built for ("A:M"), '' before the first frame
  private built = '';
  private video: HTMLVideoElement | null = null;
  private handle: number | null = null;
  private handleVideo: HTMLVideoElement | null = null;
  private destroyed = false;
  private ready = false;
  // Set when the current programs were built; 0 once their first frame was checked
  private builtAt = 0;
  private warmUntil = 0;
  // Watchdog window
  private lastPresented = -1;
  private lastFrameAt = 0;
  private seen = 0;
  private skipped = 0;
  private drawSum = 0;
  private drawMax = 0;
  private draws = 0;
  private size = { inW: 0, inH: 0, outW: 0, outH: 0 };

  constructor(private readonly canvas: HTMLCanvasElement, private readonly opts: UpscalerOptions) {
    this.tier = opts.extra ? 'X' : 'M';
    canvas.addEventListener('webglcontextlost', this.onContextLost);
    this.init();
  }

  /** The element to read frames from (the player's active slot) */
  setVideo(video: HTMLVideoElement | null) {
    if (video === this.video) return;
    this.cancelFrame();
    this.video = video;
    this.lastPresented = -1;
    this.schedule();
  }

  destroy() {
    if (this.destroyed) return;
    this.destroyed = true;
    this.cancelFrame();
    this.canvas.removeEventListener('webglcontextlost', this.onContextLost);
    this.dropPrograms();
    const g = this.gpu;
    if (g) {
      g.passthrough.destroy();
      g.gl.deleteFramebuffer(g.framebuffer);
      for (const tex of [g.input, g.native, g.output]) g.gl.deleteTexture(tex);
    }
    this.gpu = null;
    // Free the context now instead of at GC (browsers cap live contexts, ~16 in Chrome)
    this.gl?.getExtension('WEBGL_lose_context')?.loseContext();
    this.gl = null;
  }

  private async init() {
    try {
      this.shaders = await import('@/features/watch/anime4kShaders');
    } catch (e) {
      this.stop('failed', 'load', e);
      return;
    }
    if (this.destroyed) return;
    const gl = this.canvas.getContext('webgl', { premultipliedAlpha: false, depth: false, stencil: false, antialias: false });
    this.gl = gl;
    if (!gl || !enableExtensions(gl)) {
      this.stop('unsupported', gl ? 'extensions' : 'webgl');
      return;
    }
    this.opts.note?.('a4k-gpu', { ...gpuInfo(gl), dpr: r1(window.devicePixelRatio || 1) });
    try {
      const framebuffer = gl.createFramebuffer();
      if (!framebuffer) throw new Error('createFramebuffer failed');
      this.gpu = {
        gl,
        framebuffer,
        input: makeTexture(gl),
        native: makeTexture(gl),
        output: makeTexture(gl),
        passthrough: new this.shaders.PassThrough(gl),
      };
    } catch (e) {
      this.stop('failed', 'build', e);
      return;
    }
    // The programs are built on the first frame, once the video's size picks the mode
    this.schedule();
  }

  /** Builds the chain for this video when it differs from the running one */
  private ensurePrograms(inW: number, inH: number) {
    const { gpu, shaders } = this;
    if (!gpu || !shaders) return;
    const { mode, lines } = pickMode(inW, inH, this.opts.year);
    // Secondary modes need x2: judged against the screen, so fullscreen on/off doesn't rebuild
    const dpr = window.devicePixelRatio || 1;
    const screenScale = Math.min((screen.width * dpr) / inW, (screen.height * dpr) / inH);
    const extraIdle = this.tier === 'X' && screenScale < SECONDARY_MIN_SCALE;
    const tier: Tier = extraIdle ? 'M' : this.tier;
    const key = `${mode}:${tier}`;
    if (key === this.built) return;
    this.dropPrograms();
    const t0 = performance.now();
    this.programs = shaders.chain(mode, tier).map(Ctor => new Ctor(gpu.gl));
    this.built = key;
    this.opts.note?.('a4k-build', { mode, tier, lines, extra_idle: extraIdle, ms: Math.round(performance.now() - t0) });
    this.builtAt = performance.now();
    this.resetWindow();
    this.opts.onMode?.({ mode, secondary: tier === 'X', extraIdle, lines });
  }

  /** The tier the running chain uses */
  private runningTier(): Tier {
    return (this.built.split(':')[1] as Tier) || this.tier;
  }

  private dropPrograms() {
    for (const p of this.programs) p.destroy();
    this.programs = [];
  }

  private stop(why: UpscaleStop, at?: string, err?: unknown) {
    if (this.destroyed) return;
    this.opts.note?.('a4k-stop', { why, chain: this.built || '-', ...(at ? { at } : {}), ...(err ? { err: errName(err) } : {}) });
    this.destroy();
    this.opts.onStop(why);
  }

  private readonly onContextLost = () => this.stop('failed', 'context-lost');

  private schedule() {
    const v = this.video;
    if (this.destroyed || !this.gpu || !v || this.handle !== null) return;
    this.handleVideo = v;
    this.handle = typeof v.requestVideoFrameCallback === 'function'
      ? v.requestVideoFrameCallback(this.onFrame)
      : requestAnimationFrame(() => this.onFrame(performance.now()));
  }

  private cancelFrame() {
    const v = this.handleVideo;
    if (this.handle !== null) {
      if (v && typeof v.cancelVideoFrameCallback === 'function') v.cancelVideoFrameCallback(this.handle);
      else cancelAnimationFrame(this.handle);
    }
    this.handle = null;
    this.handleVideo = null;
  }

  private readonly onFrame = (_now: number, meta?: VideoFrameCallbackMetadata) => {
    this.handle = null;
    this.handleVideo = null;
    if (this.destroyed) return;
    const t0 = performance.now();
    let drawn: boolean;
    try {
      drawn = this.draw();
    } catch (e) {
      // A cross-origin video (no CORS) throws on upload; anything else is a broken GPU state
      this.stop('failed', 'draw', e);
      return;
    }
    const t1 = performance.now();
    if (drawn && this.builtAt > 0) {
      // First frame of new programs: compile time shows here (readPixels waits for the GPU)
      const s = this.size;
      this.opts.note?.('a4k-first', { chain: this.built, ms: Math.round(t1 - this.builtAt), in_w: s.inW, in_h: s.inH, out_w: s.outW, out_h: s.outH });
      this.builtAt = 0;
      this.warmUntil = t1 + WARMUP_MS;
      if (!this.ready) {
        this.ready = true;
        this.opts.onReady?.();
      }
    } else if (drawn) {
      this.drawSum += t1 - t0;
      this.drawMax = Math.max(this.drawMax, t1 - t0);
      this.draws++;
    }
    this.watch(meta, t1);
    this.schedule();
  };

  /** Draws the current frame; false when the video has none yet */
  private draw(): boolean {
    const { gpu, video: v, canvas } = this;
    if (!gpu || !v || v.readyState < v.HAVE_CURRENT_DATA || !v.videoWidth || !v.videoHeight) return false;
    const { gl } = gpu;
    const inW = v.videoWidth;
    const inH = v.videoHeight;
    // Output at the picture's on-screen size in device pixels, at most MAX_SCALE x the video
    const dpr = window.devicePixelRatio || 1;
    let scale = Math.min((canvas.clientWidth * dpr) / inW, (canvas.clientHeight * dpr) / inH, MAX_SCALE);
    if (!Number.isFinite(scale) || scale <= 0) scale = 1;
    const outW = Math.max(1, Math.round(inW * scale));
    const outH = Math.max(1, Math.round(inH * scale));
    if (canvas.width !== outW) canvas.width = outW;
    if (canvas.height !== outH) canvas.height = outH;
    this.size = { inW, inH, outW, outH };
    this.ensurePrograms(inW, inH);

    gl.bindTexture(gl.TEXTURE_2D, gpu.input);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, v);
    gl.bindTexture(gl.TEXTURE_2D, null);

    // mpv hook order: every MAIN hook, then MAIN becomes PREKERNEL for the PREKERNEL hooks
    const textures = new Map<string, TextureData>([
      ['MAIN', { texture: gpu.input, width: inW, height: inH }],
      ['NATIVE', { texture: gpu.native, width: inW, height: inH }],
      ['OUTPUT', { texture: gpu.output, width: outW, height: outH }],
    ]);
    for (const p of this.programs) p.hook_MAIN(textures, gpu.framebuffer);
    const main = textures.get('MAIN');
    if (!main) throw new Error('no MAIN output');
    textures.delete('MAIN');
    textures.set('PREKERNEL', main);
    for (const p of this.programs) p.hook_PREKERNEL(textures, gpu.framebuffer);
    gpu.passthrough.render(textures.get('PREKERNEL') ?? main, outW, outH);

    // A shader that failed to compile leaves the output empty (transparent): check the first frame
    if (this.builtAt > 0) {
      const px = new Uint8Array(4);
      gl.readPixels(outW >> 1, outH >> 1, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, px);
      if (px[3] === 0) throw new Error('empty output');
    }
    return true;
  }

  private resetWindow() {
    this.lastPresented = -1;
    this.seen = this.skipped = 0;
    this.drawSum = this.drawMax = this.draws = 0;
  }

  private watch(meta: VideoFrameCallbackMetadata | undefined, now: number) {
    const v = this.video;
    const gap = now - this.lastFrameAt;
    this.lastFrameAt = now;
    // Only steady playback is judged: not paused, not seeking, not warming up, no long gap
    if (!meta || !v || v.paused || v.seeking || document.hidden || now < this.warmUntil || gap > GAP_MS || this.builtAt > 0) {
      this.lastPresented = meta ? meta.presentedFrames : -1;
      return;
    }
    const n = meta.presentedFrames;
    if (this.lastPresented >= 0 && n > this.lastPresented) {
      this.seen += n - this.lastPresented;
      this.skipped += n - this.lastPresented - 1;
    }
    this.lastPresented = n;
    if (this.seen < WATCH_FRAMES) return;
    const ratio = this.skipped / this.seen;
    const slow = ratio > MAX_SKIPPED;
    this.opts.note?.('a4k-window', {
      chain: this.built,
      frames: this.seen,
      skipped: this.skipped,
      draw_avg: r1(this.draws ? this.drawSum / this.draws : 0),
      draw_max: r1(this.drawMax),
      in_h: this.size.inH,
      out_h: this.size.outH,
      slow,
    });
    this.resetWindow();
    if (!slow) return;
    const next = NEXT_TIER[this.runningTier()];
    if (!next) {
      this.stop('slow');
      return;
    }
    this.opts.note?.('a4k-level', { from: this.built, to: next, ratio: r1(ratio * 100) });
    if (this.tier === 'X') this.opts.onExtraDropped?.();
    this.tier = next;
    // The next frame builds the lighter chain
    this.built = '';
  }
}

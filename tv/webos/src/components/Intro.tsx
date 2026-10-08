import { useEffect, useRef, useState } from 'react';
import { INTRO, liteIntro, markBoot, playSting, whenOnScreen, type StingHandle } from '../lib/intro';

// Opening animation: the red N lands on black (hit 1), crimson light ribbons fan out of it (hit 2),
// then it zooms through into the app. Only transform / opacity are animated (CSS keyframes, compositor).

const D = INTRO.duration;
const SKIP_MS = 130;

type Frame = [number, string];

/** Keyframes on the shared timeline: [seconds, declarations] pairs become percentages of D. */
function kf(name: string, frames: Frame[]): string {
  const body = frames.map(([t, css]) => `${((Math.min(D, Math.max(0, t)) / D) * 100).toFixed(2)}%{${css}}`).join('');
  return `@keyframes ${name}{${body}}`;
}

const tf = (x: number, y: number, rot: number, sx: number, sy: number) =>
  `transform:translate(${x.toFixed(2)}rem,${y.toFixed(2)}rem) rotate(${rot.toFixed(2)}deg) scale(${sx},${sy})`;

interface Ribbon {
  css: string;
  width: number;
  tone: number;
}

function ribbons(count: number): Ribbon[] {
  const out: Ribbon[] = [];
  let seed = 7;
  const rnd = () => {
    seed = (seed * 16807) % 2147483647;
    return seed / 2147483647;
  };
  const pairs = count / 2;
  for (let i = 0; i < count; i++) {
    const side = i % 2 ? 1 : -1;
    const rank = Math.floor(i / 2);
    const reach = 7 + (rank / Math.max(1, pairs - 1)) * 52 + rnd() * 4;
    const angle = side * (2 + rank * (12 / pairs) + rnd() * 2);
    const y = (rnd() - 0.5) * 8;
    const peak = 0.7 + rnd() * 0.3;
    const s = INTRO.hit2 - 0.03 + rank * 0.03 + rnd() * 0.04;
    const a = `ribbon${i}`;
    const frames: Frame[] = [
      [0, `${tf(0, y, 0, 0.15, 0.08)};opacity:0`],
      [s, `${tf(0, y, 0, 0.15, 0.08)};opacity:0;animation-timing-function:cubic-bezier(.2,.7,.3,1)`],
      [s + 0.14, `${tf(side * reach * 0.12, y * 0.6, angle * 0.25, 1, 0.85)};opacity:${peak.toFixed(2)};animation-timing-function:linear`],
      [s + 0.6, `${tf(side * reach * 0.62, y * 0.2, angle * 0.8, 1.3, 1.05)};opacity:${(peak * 0.55).toFixed(2)};animation-timing-function:ease-in`],
      [s + 1.15, `${tf(side * reach, -y * 0.3, angle, 1.7, 1.15)};opacity:0`],
      [D, `${tf(side * reach, -y * 0.3, angle, 1.7, 1.15)};opacity:0`],
    ];
    out.push({ css: kf(`nfx-${a}`, frames), width: 1.2 + rnd() * 3, tone: i % 3 });
  }
  return out;
}

function stageCss(lite: boolean, rib: Ribbon[]): string {
  const h1 = INTRO.hit1;
  const h2 = INTRO.hit2;
  const zoom = lite ? 4 : 9;
  return [
    kf('nfx-n', [
      [0, 'transform:scale(1.5);opacity:0'],
      [0.08, 'transform:scale(1.5);opacity:0;animation-timing-function:cubic-bezier(.6,0,.9,.45)'],
      [h1, 'transform:scale(1);opacity:1;animation-timing-function:ease-out'],
      [h1 + 0.08, 'transform:scale(0.975);opacity:1;animation-timing-function:ease-in-out'],
      [h1 + 0.22, 'transform:scale(1);opacity:1;animation-timing-function:linear'],
      [h2, 'transform:scale(1.05);opacity:1;animation-timing-function:ease-out'],
      [h2 + 0.08, 'transform:scale(1.11);opacity:1;animation-timing-function:ease-in-out'],
      [1.95, 'transform:scale(1.16);opacity:1;animation-timing-function:cubic-bezier(.55,0,.85,.35)'],
      [2.5, `transform:scale(${zoom});opacity:0`],
      [D, `transform:scale(${zoom});opacity:0`],
    ]),
    kf('nfx-hot', [
      [0, 'opacity:0'],
      [h2 - 0.04, 'opacity:0'],
      [h2 + 0.05, 'opacity:0.85'],
      [1.75, 'opacity:0.25'],
      [2.2, 'opacity:0'],
      [D, 'opacity:0'],
    ]),
    kf('nfx-glow', [
      [0, 'transform:scale(0.6);opacity:0'],
      [h1 - 0.04, 'transform:scale(0.6);opacity:0'],
      [h1 + 0.03, 'transform:scale(0.9);opacity:0.6'],
      [h2 - 0.05, 'transform:scale(1);opacity:0.3'],
      [h2 + 0.07, 'transform:scale(1.25);opacity:0.95'],
      [1.85, 'transform:scale(1.45);opacity:0.5'],
      [2.55, 'transform:scale(2.4);opacity:0'],
      [D, 'transform:scale(2.4);opacity:0'],
    ]),
    kf('nfx-flash', [
      [0, 'transform:scale(0.3);opacity:0'],
      [h2 - 0.02, 'transform:scale(0.3);opacity:0'],
      [h2 + 0.05, 'transform:scale(1);opacity:0.85'],
      [h2 + 0.55, 'transform:scale(1.8);opacity:0'],
      [D, 'transform:scale(1.8);opacity:0'],
    ]),
    kf('nfx-sweep', [
      [0, 'transform:translateX(-70rem) skewX(-18deg);opacity:0'],
      [h2 - 0.1, 'transform:translateX(-70rem) skewX(-18deg);opacity:0'],
      [h2, 'transform:translateX(-45rem) skewX(-18deg);opacity:1'],
      [h2 + 0.65, 'transform:translateX(70rem) skewX(-18deg);opacity:0'],
      [D, 'transform:translateX(70rem) skewX(-18deg);opacity:0'],
    ]),
    kf('nfx-stage', [
      [0, 'opacity:1'],
      [2.25, 'opacity:1'],
      [2.75, 'opacity:0'],
      [D, 'opacity:0'],
    ]),
    ...rib.map(r => r.css),
  ].join('');
}

/**
 * Draws the N (depth copies + gradient face) and its hot glow into canvases. A canvas is one GPU
 * texture that the zoom only scales; the same N as text was re-rastered at the animation's
 * largest scale and stalled the TV's compositor for ~2 s, so the animation was never seen.
 */
function drawN(face: HTMLCanvasElement, hot: HTMLCanvasElement, lite: boolean) {
  const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  const size = 26 * rem;
  const ratio = Math.min(2.5, 1.25 * (window.devicePixelRatio || 1));
  const font = `800 ${size}px Inter, sans-serif`;
  const probe = face.getContext('2d');
  if (!probe) return;
  probe.font = font;
  const m = probe.measureText('N');
  // Old engines lack the bounding box metrics: Inter's N is ~0.73 em tall
  const left = m.actualBoundingBoxLeft ?? 0;
  const right = m.actualBoundingBoxRight ?? m.width;
  const ascent = m.actualBoundingBoxAscent ?? size * 0.73;
  const descent = m.actualBoundingBoxDescent ?? 0;
  const gw = left + right;
  const gh = ascent + descent;
  const depth = lite ? 0.45 * rem : 0.9 * rem;

  const setup = (c: HTMLCanvasElement, pad: number, scale: number) => {
    const w = gw + depth + pad * 2;
    const h = gh + depth + pad * 2;
    c.width = Math.ceil(w * scale);
    c.height = Math.ceil(h * scale);
    c.style.width = `${w}px`;
    c.style.height = `${h}px`;
    const ctx = c.getContext('2d')!;
    ctx.scale(scale, scale);
    ctx.font = font;
    return { ctx, x: pad + left, y: pad + ascent, w, h };
  };

  const f = setup(face, 0, ratio);
  const depths: [number, number, string][] = lite
    ? [[0.25, 0.45, '#7d040d']]
    : [[0.5, 0.9, '#3f0207'], [0.25, 0.45, '#7d040d']];
  for (const [dx, dy, color] of depths) {
    f.ctx.fillStyle = color;
    f.ctx.fillText('N', f.x + dx * rem, f.y + dy * rem);
  }
  const g = f.ctx.createLinearGradient(0, f.y - ascent, 0, f.y + descent);
  g.addColorStop(0, '#ff3a44');
  g.addColorStop(0.46, '#e50914');
  g.addColorStop(1, '#a8050e');
  f.ctx.fillStyle = g;
  f.ctx.fillText('N', f.x, f.y);

  const pad = (lite ? 4 : 9) * rem;
  // A soft glow: a quarter of the pixels is enough and keeps the blur fast
  const hotScale = 0.5;
  const hh = setup(hot, pad, hotScale);
  hh.ctx.fillStyle = '#ff7d84';
  const shadows: [number, string][] = lite
    ? [[3, 'rgba(255, 70, 82, 0.9)']]
    : [[8, 'rgba(229, 9, 20, 0.7)'], [3, 'rgba(255, 70, 82, 0.9)']];
  for (const [blur, color] of shadows) {
    hh.ctx.shadowBlur = blur * rem * hotScale; // shadowBlur ignores the context scale
    hh.ctx.shadowColor = color;
    hh.ctx.fillText('N', hh.x, hh.y);
  }
  hot.style.left = `${-pad}px`;
  hot.style.top = `${-pad}px`;
}

/** A radial gradient (CSS `radial-gradient(circle, ...)` of a square box) on a small canvas the GPU scales. */
function paintRadial(c: HTMLCanvasElement | null, stops: [number, string][]) {
  const ctx = c && c.getContext('2d');
  if (!c || !ctx) return;
  const n = 512;
  c.width = n;
  c.height = n;
  const g = ctx.createRadialGradient(n / 2, n / 2, 0, n / 2, n / 2, n / Math.SQRT2);
  for (const [at, color] of stops) g.addColorStop(at, color);
  ctx.fillStyle = g;
  ctx.fillRect(0, 0, n, n);
}

export default function Intro({ sound, onDone }: { sound: boolean; onDone: () => void }) {
  const lite = useRef(liteIntro()).current;
  const [css] = useState(() => {
    const rib = ribbons(lite ? 6 : 14);
    return { rib, text: stageCss(lite, rib) };
  });
  const [playing, setPlaying] = useState(false);
  const [skipping, setSkipping] = useState(false);
  const doneRef = useRef(onDone);
  doneRef.current = onDone;
  const skipRef = useRef<() => void>(() => {});
  const faceRef = useRef<HTMLCanvasElement>(null);
  const hotRef = useRef<HTMLCanvasElement>(null);
  const glowRef = useRef<HTMLCanvasElement>(null);
  const flashRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    let sting: StingHandle | null = null;
    let finished = false;
    const timers: number[] = [];
    const finish = () => {
      if (finished) return;
      finished = true;
      document.body.classList.remove('intro-on');
      doneRef.current();
    };
    const skip = () => {
      if (finished) return;
      if (sting) sting.stop(SKIP_MS);
      setSkipping(true);
      document.body.classList.remove('intro-on');
      timers.push(window.setTimeout(finish, SKIP_MS + 20));
    };
    skipRef.current = skip;
    let begun = false;
    const begin = () => {
      if (begun || finished) return;
      begun = true;
      setPlaying(true);
      if (!lite) document.body.classList.add('intro-on');
      if (sound) sting = playSting();
      timers.push(window.setTimeout(finish, D * 1000 + 60));
    };
    // Held on its black first frame until the app is on screen (not behind the TV's splash),
    // and briefly for the wordmark's font so the N does not swap mid-animation
    const fonts = (document as Document & { fonts?: { load(f: string): Promise<unknown> } }).fonts;
    const font = new Promise<unknown>(resolve => {
      if (fonts && fonts.load) fonts.load('800 100px Inter').then(resolve, resolve);
      timers.push(window.setTimeout(resolve, 500));
    });
    // Painted while the app may still be behind the splash: the first canvas use is slow on a TV
    const painted = font.then(() => {
      if (finished) return;
      try {
        if (faceRef.current && hotRef.current) drawN(faceRef.current, hotRef.current, lite);
        paintRadial(glowRef.current, [[0, 'rgba(229, 9, 20, 0.55)'], [0.32, 'rgba(150, 0, 18, 0.25)'], [0.62, 'rgba(0, 0, 0, 0)']]);
        paintRadial(flashRef.current, [[0, 'rgba(255, 236, 236, 0.9)'], [0.26, 'rgba(255, 80, 92, 0.45)'], [0.62, 'rgba(255, 80, 92, 0)']]);
      } catch {
        /* no 2D canvas: the stage stays black */
      }
      markBoot('intro painted');
    });
    Promise.all([painted, whenOnScreen()]).then(() => {
      if (finished) return;
      // Let the canvases reach the GPU before the clock starts
      requestAnimationFrame(() => requestAnimationFrame(() => {
        markBoot('intro start');
        begin();
      }));
    });

    // Any key skips; the app does not see it
    const onKey = (e: KeyboardEvent) => {
      e.preventDefault();
      e.stopImmediatePropagation();
      skip();
    };
    // Only a real hide (Home button); webOS reports 'launching' while the splash is up
    const onHidden = () => { if (document.visibilityState === 'hidden') skip(); };
    window.addEventListener('keydown', onKey, true);
    document.addEventListener('visibilitychange', onHidden);
    document.addEventListener('webOSRelaunch', skip);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      document.removeEventListener('visibilitychange', onHidden);
      document.removeEventListener('webOSRelaunch', skip);
      timers.forEach(t => clearTimeout(t));
      document.body.classList.remove('intro-on');
      if (sting) sting.stop(SKIP_MS);
    };
  }, [lite, sound]);

  const stop = (e: { preventDefault(): void; stopPropagation(): void }) => {
    e.preventDefault();
    e.stopPropagation();
    skipRef.current();
  };

  return (
    <div className={`intro${playing ? ' is-playing' : ''}${skipping ? ' is-skipping' : ''}${lite ? ' is-lite' : ''}`} onMouseDown={stop} onClick={stop}>
      <style>{css.text}</style>
      <div className="intro-stage">
        <canvas ref={glowRef} className="intro-glow" />
        <div className="intro-ribbons">
          {css.rib.map((r, i) => (
            <div
              key={i}
              className={`intro-ribbon intro-ribbon-${r.tone}`}
              style={{ width: `${r.width.toFixed(2)}rem`, marginLeft: `${(-r.width / 2).toFixed(2)}rem`, animationName: playing ? `nfx-ribbon${i}` : undefined }}
            />
          ))}
        </div>
        <canvas ref={flashRef} className="intro-flash" />
        <div className="intro-center">
          <div className="intro-n">
            <canvas ref={faceRef} className="intro-n-face" />
            <canvas ref={hotRef} className="intro-n-hot" />
          </div>
        </div>
        <div className="intro-sweep" />
      </div>
    </div>
  );
}

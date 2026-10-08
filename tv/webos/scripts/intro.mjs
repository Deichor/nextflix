// Opening intro check (SCRIPT=intro scripts/smoke.sh): frames at fixed times, the app behind it, skipping
// with a key, the settings switch and the sting's level (rendered offline). Screenshots: out/screens/intro-*.png
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const APP = process.env.APP_URL || 'http://127.0.0.1:5173';
const SERVER = process.env.SERVER_URL || 'http://192.168.1.221:8252';
const OUT = process.env.OUT_DIR || '/work/out/screens';
const URL = `${APP}/?server=${encodeURIComponent(SERVER)}`;
const FRAMES = [0.3, 1.0, 1.8, 2.2, 2.6];

mkdirSync(OUT, { recursive: true });
const sleep = ms => new Promise(r => setTimeout(r, ms));
const log = (...a) => console.log('[intro]', ...a);
let failed = false;
const check = (ok, what) => { log(ok ? 'OK  ' : 'FAIL', what); if (!ok) failed = true; };

const browser = await chromium.launch({
  channel: process.env.CHANNEL || undefined,
  args: ['--autoplay-policy=no-user-gesture-required'],
});
const newPage = async () => {
  const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } });
  page.errors = [];
  page.on('pageerror', e => page.errors.push(String(e)));
  // Remember the audio contexts the app opens
  await page.addInitScript(() => {
    const Real = window.AudioContext;
    window.__ctx = [];
    window.AudioContext = function () { const c = new Real(); window.__ctx.push(c); return c; };
  });
  return page;
};

// 1. Frames: fake timers keep the intro mounted, CSS animations are paused and seeked
{
  const page = await newPage();
  await page.clock.install();
  await page.goto(URL);
  await page.waitForSelector('.intro.is-playing', { timeout: 10000 });
  await page.evaluate(() => document.getAnimations().forEach(a => a.pause()));
  for (const t of FRAMES) {
    await page.evaluate(ms => document.getAnimations().forEach(a => { a.currentTime = ms; }), t * 1000);
    await sleep(150);
    await page.screenshot({ path: `${OUT}/intro-${t.toFixed(1)}s.png` });
    log('frame', `${t}s`);
  }
  const state = await page.evaluate(() => window.__ctx.map(c => c.state));
  check(state.length === 1 && state[0] === 'running', `audio context running (${JSON.stringify(state)})`);
  await page.evaluate(() => document.getAnimations().forEach(a => a.play()));
  await page.clock.runFor(3000);
  await page.waitForSelector('.intro', { state: 'detached', timeout: 3000 }).catch(() => {});
  check(!(await page.$('.intro')), 'intro removed after 2.8 s');
  await page.close();
}

// 2. Real time: the profile screen loads behind the intro and shows afterwards
{
  const page = await newPage();
  const t0 = Date.now();
  await page.goto(URL);
  await page.waitForSelector('.intro.is-playing', { timeout: 10000 });
  const started = Date.now() - t0;
  await page.waitForSelector('.profile-tile', { state: 'attached', timeout: 20000 });
  const tilesDuring = !!(await page.$('.intro'));
  await page.waitForSelector('.intro', { state: 'detached', timeout: 6000 });
  log('intro playing after', started, 'ms, ended after', Date.now() - t0, 'ms');
  check(tilesDuring, 'profiles loaded while the intro was still playing');
  await sleep(600);
  const focus = await page.evaluate(() => document.activeElement?.getAttribute('data-nav-id'));
  check(/^profile-/.test(focus || ''), `profile screen focused (${focus})`);
  await page.screenshot({ path: `${OUT}/intro-after.png` });
  // Relaunch from the background must not replay it
  await page.evaluate(() => document.dispatchEvent(new Event('webOSRelaunch')));
  await sleep(300);
  check(!(await page.$('.intro')), 'webOSRelaunch does not replay');
  check(page.errors.length === 0, `no page errors ${JSON.stringify(page.errors)}`);
  await page.close();
}

// 3. A key skips it (fades within ~150 ms) and does not reach the app
{
  const page = await newPage();
  await page.goto(URL);
  await page.waitForSelector('.profile-tile', { timeout: 20000 });
  await page.waitForSelector('.intro.is-playing', { timeout: 10000 });
  await sleep(700);
  const before = await page.evaluate(() => document.activeElement?.getAttribute('data-nav-id'));
  const t = Date.now();
  await page.keyboard.press('Enter');
  await page.waitForSelector('.intro', { state: 'detached', timeout: 2000 });
  const gone = Date.now() - t;
  await sleep(300);
  const after = await page.evaluate(() => ({ focus: document.activeElement?.getAttribute('data-nav-id'), profiles: !!document.querySelector('.profiles-screen') }));
  check(gone < 400, `Enter skipped the intro in ${gone} ms`);
  check(after.profiles && after.focus === before, `key not passed to the app (focus ${before} -> ${after.focus})`);
  await page.screenshot({ path: `${OUT}/intro-skipped.png` });
  await page.close();
}

// 4. Settings off: no intro
{
  const page = await newPage();
  await page.addInitScript(() => localStorage.setItem('nfx-tv:intro', JSON.stringify({ animation: false, sound: false })));
  await page.goto(URL);
  await page.waitForSelector('.profile-tile', { timeout: 20000 });
  check(!(await page.$('.intro')), 'animation off in settings: no intro');
  await page.close();
}

// 5. Sting level and length, rendered offline
{
  const page = await newPage();
  await page.goto(URL);
  const r = await page.evaluate(async () => {
    const mod = await import('/src/lib/intro.ts');
    const rate = 44100;
    const ctx = new OfflineAudioContext(2, rate * 3.2, rate);
    mod.scheduleSting(ctx, 0);
    const buf = await ctx.startRendering();
    let peak = 0;
    let first = -1;
    let last = 0;
    for (let c = 0; c < buf.numberOfChannels; c++) {
      const d = buf.getChannelData(c);
      for (let i = 0; i < d.length; i++) {
        const v = Math.abs(d[i]);
        if (v > peak) peak = v;
        if (v > 0.001) { last = Math.max(last, i); if (first < 0 || i < first) first = i; }
      }
    }
    return { peakDb: 20 * Math.log10(peak), first: first / rate, last: last / rate };
  });
  log(`sting peak ${r.peakDb.toFixed(1)} dBFS, sound ${r.first.toFixed(2)}-${r.last.toFixed(2)} s`);
  check(r.peakDb < -9 && r.peakDb > -16, 'peak around -12 dBFS');
  check(r.last - r.first <= 2.5, 'sting at most 2.5 s');
  await page.close();
}

await browser.close();
log(failed ? 'FAILED' : 'all checks passed');
process.exit(failed ? 1 : 0);

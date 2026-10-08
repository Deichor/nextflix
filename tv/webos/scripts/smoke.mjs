// Remote-control smoke test against the dev server (scripts/smoke.sh runs it in a Playwright container).
// Screenshots go to out/screens/. PLAY=1 also starts "Night of the Living Dead" (1968, public domain).
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const APP = process.env.APP_URL || 'http://127.0.0.1:5173';
const SERVER = process.env.SERVER_URL || 'http://192.168.1.221:8252';
const OUT = process.env.OUT_DIR || '/work/out/screens';
const PLAY = process.env.PLAY === '1';
const NOTLD = 10331;

mkdirSync(OUT, { recursive: true });
const sleep = ms => new Promise(r => setTimeout(r, ms));
const log = (...a) => console.log('[smoke]', ...a);

const browser = await chromium.launch({
  channel: process.env.CHANNEL || undefined,
  args: ['--autoplay-policy=no-user-gesture-required'],
});
const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } });
const errors = [];
page.on('pageerror', e => errors.push(String(e)));
page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });

const focused = () => page.evaluate(() => document.activeElement?.getAttribute('data-nav-id') || document.activeElement?.tagName);
const key = async (k, n = 1, wait = 250) => {
  for (let i = 0; i < n; i++) {
    await page.keyboard.press(k);
    await sleep(wait);
  }
};
const shot = async name => {
  await page.screenshot({ path: `${OUT}/${name}.png` });
  log('screenshot', name, 'focus:', await focused());
};
const waitFor = async (selector, name, timeout = 30000) => {
  try {
    await page.waitForSelector(selector, { timeout });
  } catch (e) {
    await page.screenshot({ path: `${OUT}/fail-${name}.png` });
    log('FAILED waiting for', selector, 'focus:', await focused(), 'errors:', errors);
    await browser.close();
    process.exit(1);
  }
};
const NAV = ['nav-profile', 'nav-search', 'nav-home', 'nav-tv', 'nav-movies', 'nav-settings'];
const navTo = async id => {
  for (let i = 0; i < 12 && !String(await focused()).startsWith('nav-'); i++) await key('ArrowLeft', 1, 250);
  for (let i = 0; i < 8; i++) {
    const cur = NAV.indexOf(await focused());
    const want = NAV.indexOf(id);
    if (cur === want) break;
    await key(cur < want ? 'ArrowDown' : 'ArrowUp', 1, 250);
  }
  await key('Enter', 1, 800);
};
const api = (path, init) => page.evaluate(async ([url, init]) => {
  const res = await fetch(url, init);
  return { status: res.status, body: res.status === 204 ? null : await res.json().catch(() => null) };
}, [`${SERVER}${path}`, init || {}]);

// 1. Profile picker
await page.goto(`${APP}/?server=${encodeURIComponent(SERVER)}`);
await page.waitForSelector('.profile-tile', { timeout: 20000 });
// The opening intro swallows keys until it ends
await page.waitForSelector('.intro', { state: 'detached', timeout: 10000 });
await sleep(800);
await shot('01-profiles');
const profileId = Number((await focused()).replace('profile-', ''));
log('profile', profileId);

// 2. Home
await key('Enter', 1, 400);
await page.waitForSelector('.row .card-backdrop, .row .card-rank', { timeout: 30000 });
await sleep(2500);
await shot('02-home');

// 3. Into the rows: down twice, right three times
await key('ArrowDown', 2, 400);
await key('ArrowRight', 3, 350);
await sleep(1200);
const cardBefore = await focused();
await shot('03-home-rows');

// 4. Detail page and Back (focus must come back to the same card)
await key('Enter', 1, 500);
await waitFor('.detail-top', 'detail', 60000);
await sleep(2500);
await shot('04-detail');
await key('Escape', 1, 800);
await sleep(800);
const cardAfter = await focused();
log('focus restore', cardBefore, '->', cardAfter, cardBefore === cardAfter ? 'OK' : 'MISMATCH');
await shot('05-home-after-back');

// 5. Menu -> Search, type with the keyboard
await key('ArrowLeft', 4, 300);
for (let i = 0; i < 6 && (await focused()) !== 'nav-search'; i++) await key('ArrowUp', 1, 250);
await key('Enter', 1, 600);
await page.waitForSelector('.search-screen', { timeout: 10000 });
await page.keyboard.type('night of the living dead', { delay: 40 });
await page.waitForSelector(`[data-nav-id="search:movie-${NOTLD}"]`, { timeout: 30000 });
await sleep(1500);
await shot('06-search');

// 6. Pointer (Magic Remote) click on the result
await page.click(`[data-nav-id="search:movie-${NOTLD}"]`);
await waitFor('.detail-top', 'notld-detail', 60000);
await sleep(3000);
await shot('07-detail-notld');

let hadProgress = null;
if (PLAY) {
  hadProgress = (await api(`/api/stream-progress/item?profile_id=${profileId}&media_type=movie&tmdb_id=${NOTLD}`)).status === 200;
  await page.evaluate(() => document.querySelector('[data-nav-id="detail-play"]')?.focus());
  await key('Enter', 1, 500);
  await page.waitForSelector('.player', { timeout: 10000 });
  await sleep(4000);
  await shot('08-player-starting');
  // wait for frames
  const started = Date.now();
  let t = 0;
  while (Date.now() - started < 150000) {
    t = await page.evaluate(() => document.querySelector('video')?.currentTime || 0);
    if (t > 3) break;
    await sleep(2000);
  }
  log('video currentTime', t.toFixed(1), 'after', Math.round((Date.now() - started) / 1000), 's');
  const info = await page.evaluate(() => ({
    src: document.querySelector('video')?.getAttribute('src'),
    status: document.querySelector('.player-status')?.textContent,
    badge: Array.from(document.querySelectorAll('.badge')).map(b => b.textContent),
  }));
  log('player', JSON.stringify(info));
  await shot('09-player');
  await key('ArrowRight', 3, 150);
  await sleep(300);
  await shot('10-player-seek-preview');
  await sleep(4000);
  await key('ArrowUp', 1, 400);
  await shot('11-player-controls');
  for (let i = 0; i < 6 && (await focused()) !== 'pl-audio'; i++) await key('ArrowRight', 1, 250);
  await key('Enter', 1, 1500);
  await shot('12-player-audio-subtitles');
  // English subtitles, then jump to the first dialogue (about 4 minutes in)
  await key('ArrowRight', 1, 300);
  for (let i = 0; i < 4 && (await focused()) !== 'sub-en'; i++) await key('ArrowDown', 1, 250);
  await key('Enter', 1, 800);
  await key('Escape', 1, 800);
  await key('ArrowDown', 1, 300);
  await key('ArrowRight', 18, 120);
  await sleep(6000);
  let cue = '';
  for (let i = 0; i < 15 && !cue; i++) {
    cue = await page.evaluate(() => document.querySelector('.subtitles-text')?.textContent || '');
    if (!cue) await sleep(1000);
  }
  log('subtitle cue:', JSON.stringify(cue), 'position', await page.evaluate(() => document.querySelector('.timeline-times span')?.textContent));
  await shot('13-player-subtitles');
  await key('Escape', 1, 1500);
  await shot('14-after-player');
  if (hadProgress === false) {
    const del = await api(`/api/stream-progress?profile_id=${profileId}&media_type=movie&tmdb_id=${NOTLD}`, { method: 'DELETE' });
    log('removed test progress', del.status);
  }
}

// TV Shows -> a show's episodes
await key('Escape', 1, 800);
await navTo('nav-tv');
await page.waitForSelector('.row .card-backdrop, .row .card-rank', { timeout: 30000 });
await sleep(2500);
await shot('15-tv-shows');
await key('ArrowDown', 1, 400);
await key('Enter', 1, 600);
await waitFor('.detail-top', 'tv-detail', 60000);
await sleep(2500);
await key('ArrowDown', 2, 600);
await sleep(2000);
await shot('16-tv-detail-episodes');

// Settings
await key('Escape', 1, 800);
await navTo('nav-settings');
await sleep(1200);
await shot('17-settings');

// Second profile (language follows the profile)
await navTo('nav-profile');
await page.waitForSelector('.profile-tile', { timeout: 20000 });
await key('ArrowRight', 1, 300);
await key('Enter', 1, 600);
await page.waitForSelector('.row .card-backdrop, .row .card-rank', { timeout: 30000 });
await sleep(3000);
await shot('18-home-second-profile');

log('page errors', errors.filter(e => !e.includes('404')).length ? errors : 'none (404s are expected: no rating / progress yet)');
await browser.close();

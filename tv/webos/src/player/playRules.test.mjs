import { test } from 'node:test';
import assert from 'node:assert/strict';
import { canSeek, cueAnswer, handoverPlan, keepsLowerCap, openStart, progressToSave, subtitleRetryMs } from './playRules.ts';
import { healthStarving } from './startBuffer.ts';

test('subtitleRetryMs: a server or network error is asked again after 5 s, doubling up to 5 minutes', () => {
  assert.deepEqual([1, 2, 3, 4].map(subtitleRetryMs), [5000, 10000, 20000, 40000]);
  assert.equal(subtitleRetryMs(7), 300000);
  assert.equal(subtitleRetryMs(30), 300000);
});

test('openStart: a resume or switch away from the end keeps its position', () => {
  assert.equal(openStart(0, 3600, true), 0);
  assert.equal(openStart(1200, 3600, true), 1200);
  assert.equal(openStart(1200, 3600, false), 1200);
  assert.equal(openStart(3589, 3600, false), 3589);
  // Unknown length: the position is kept
  assert.equal(openStart(1200, 0, false), 1200);
});

test('openStart: the first resume at the very end watches again from 0', () => {
  assert.equal(openStart(3595, 3600, true), 0);
  assert.equal(openStart(3600, 3600, true), 0);
});

test('openStart: a switch or recovery in the last 10 s ends the title, never replays it', () => {
  assert.equal(openStart(3590, 3600, false), 'end');
  assert.equal(openStart(3598, 3600, false), 'end');
  assert.equal(openStart(4000, 3600, false), 'end');
});

test('cueAnswer: cues or a partial answer are shown', () => {
  assert.equal(cueAnswer(12, false, 0), 'show');
  assert.equal(cueAnswer(12, true, 3), 'show');
  assert.equal(cueAnswer(0, true, 0), 'show');
});

test('cueAnswer: an empty first answer tries another subtitle', () => {
  assert.equal(cueAnswer(0, false, 0), 'fallback');
});

test('cueAnswer: an empty answer while a live track is polled (network error, 5xx) asks again', () => {
  assert.equal(cueAnswer(0, false, 1), 'retry');
  assert.equal(cueAnswer(0, false, 7), 'retry');
});

test('canSeek: only an opened source', () => {
  const ready = { phase: 'ready', fatal: null, duration: 3600 };
  assert.ok(canSeek(ready, 'abc'));
  assert.ok(!canSeek(ready, ''));
  assert.ok(!canSeek({ ...ready, duration: 0 }, 'abc'));
  assert.ok(!canSeek({ ...ready, fatal: { key: 'player.playbackFailed' } }, 'abc'));
  // Finding another source: the failed source's hash and duration are still set
  assert.ok(!canSeek({ ...ready, phase: 'resolving' }, 'abc'));
  assert.ok(!canSeek({ ...ready, phase: 'error' }, 'abc'));
});

test('progressToSave: nothing of the item played keeps the saved progress', () => {
  const x = { finished: false, itemPlayed: false, duration: 3600, exact: true, position: 1200, offset: 1190 };
  assert.equal(progressToSave(x), null);
  assert.equal(progressToSave({ ...x, finished: true }), 3600);
});

test('progressToSave: a source switch before its first frame still saves the position', () => {
  // The new source's stream starts at the switch position (offset) until its seek point is known
  const x = { finished: false, itemPlayed: true, duration: 3600, exact: false, position: 1500, offset: 1490 };
  assert.equal(progressToSave(x), 1490);
  assert.equal(progressToSave({ ...x, exact: true }), 1500);
  assert.equal(progressToSave({ ...x, exact: true, position: 3700 }), 3600);
});

test('progressToSave: no length or under 10 s saves nothing', () => {
  const x = { finished: false, itemPlayed: true, duration: 0, exact: true, position: 1500, offset: 0 };
  assert.equal(progressToSave(x), null);
  assert.equal(progressToSave({ ...x, duration: 3600, position: 8 }), null);
});

test('handoverPlan: never above the starving source; a 4K steps down to 1080p', () => {
  // TV without maxq: a starving 1080p is replaced at 1080p, never by a 4K
  assert.deepEqual(handoverPlan(1080, undefined, 0, 3), { maxq: 1080, step: false, last: false });
  assert.deepEqual(handoverPlan(720, 1080, 0, 3), { maxq: 720, step: false, last: false });
  assert.deepEqual(handoverPlan(1080, 720, 0, 3), { maxq: 720, step: false, last: false });
  assert.deepEqual(handoverPlan(2160, undefined, 0, 3), { maxq: 1080, step: true, last: false });
  // 1080p found nothing: the 4K is replaced at the original cap
  assert.deepEqual(handoverPlan(2160, undefined, 1, 3, [1080]), { maxq: undefined, step: false, last: false });
  assert.deepEqual(handoverPlan(0, undefined, 0, 3), { maxq: undefined, step: false, last: false });
});

test('keepsLowerCap: only an answer that plays well keeps the lowered cap', () => {
  assert.equal(keepsLowerCap({ rate: 4, need: 2, keeps_up: true }), true);
  assert.equal(keepsLowerCap(undefined), true);
  assert.equal(keepsLowerCap({ rate: 2.5, need: 2, keeps_up: false }), true);
  // Detour (1945): the 720p answer needs 1.84 MB/s from 2-3 seeders
  assert.equal(keepsLowerCap({ rate: 0.4e6, need: 1.84e6, keeps_up: false }), false);
  assert.equal(keepsLowerCap({ rate: 0, need: 1.84e6, keeps_up: false, dead: true }), false);
});

test('Detour sequence: a starving 1080p is handed over at 1080p, the last one without the earlier avoids, then the window', () => {
  const plans = [0, 1, 2, 3].map(n => handoverPlan(1080, undefined, n, 3));
  assert.deepEqual(plans, [
    { maxq: 1080, step: false, last: false },
    { maxq: 1080, step: false, last: false },
    { maxq: 1080, step: false, last: true },
    null,
  ]);
});

test('healthStarving: only a measured rate below need (or a dead swarm) is starving, not a measurement in progress', () => {
  assert.equal(healthStarving({ rate: 2, need: 5, keeps_up: false }), true);
  assert.equal(healthStarving({ rate: 0, need: 5, keeps_up: false, dead: true }), true);
  assert.equal(healthStarving({ rate: 0, need: 5, keeps_up: false }), false);
  assert.equal(healthStarving({ rate: 6, need: 5, keeps_up: false }), false);
  assert.equal(healthStarving({ rate: 2, need: 5, keeps_up: true }), false);
  assert.equal(healthStarving(undefined), false);
});

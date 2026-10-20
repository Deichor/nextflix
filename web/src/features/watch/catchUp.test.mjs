// Run in a node:22 container: node --experimental-strip-types --test src/features/watch/catchUp.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { catchUpStep, catchUpTime, playPress } from './catchUp.ts';

const ranges = (...r) => ({ length: r.length, start: i => r[i][0], end: i => r[i][1] });

test('the element skips from the keyframe to the switch point (The Mentalist S03E03: 6.36 s, keyframe at 0)', () => {
  assert.ok(Math.abs(catchUpTime(6.36, -0.08) - 6.44) < 1e-9);
  assert.ok(Math.abs(catchUpTime(1650.2, 1641.0) - 9.2) < 1e-9);
  // The stream already starts there (a keyframe close to it, or no seekpoint answer)
  assert.equal(catchUpTime(6.36, 6.2), 0);
  assert.equal(catchUpTime(6.36, 6.36), 0);
  assert.equal(catchUpTime(6.36, 7), 0);
});

test('it seeks once the buffer reaches the target', () => {
  assert.equal(catchUpStep(6.44, 0, ranges()), null);
  assert.equal(catchUpStep(6.44, 0, ranges([0, 4])), null);
  assert.equal(catchUpStep(6.44, 0, ranges([0, 6.5])), null);
  assert.deepEqual(catchUpStep(6.44, 0, ranges([-0.04, 6.7])), { to: 6.44, last: true });
  // A gap before it does not matter
  assert.deepEqual(catchUpStep(6.44, 0, ranges([0, 3], [5, 9])), { to: 6.44, last: true });
});

test('a buffer the feed stops filling is stepped through', () => {
  // 10 s ahead and a keyframe 11.5 s back: the feed waits for playback, so the element moves on
  assert.deepEqual(catchUpStep(11.5, 0, ranges([0, 10.2])), { to: 9.2, last: false });
  assert.equal(catchUpStep(11.5, 9.2, ranges([0, 10.2])), null);
  assert.deepEqual(catchUpStep(11.5, 9.2, ranges([0, 12])), { to: 11.5, last: true });
});

test('Play during a catch-up holds the element until it is past the frames before the position', () => {
  // Paused by the viewer (or a restart that kept the pause), and Play during the start buffer
  assert.equal(playPress(null, true, true), 'hold');
  assert.equal(playPress('start', true, true), 'hold');
  // Without a catch-up they play at once
  assert.equal(playPress(null, false, true), 'play');
  assert.equal(playPress('start', false, true), 'play');
  // Pause during a stall hold (a catch-up's too) stays paused; a playing element pauses
  assert.equal(playPress('stall', true, true), 'release');
  assert.equal(playPress('stall', false, true), 'release');
  assert.equal(playPress(null, false, false), 'pause');
});

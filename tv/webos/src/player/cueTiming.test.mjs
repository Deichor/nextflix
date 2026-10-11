import { test } from 'node:test';
import assert from 'node:assert/strict';
import { CUE_CHECK_MAX_MS, cueCheckDelay, nextCueChange } from './cueTiming.ts';

const cues = [
  { start: 10, end: 12 },
  { start: 11, end: 20 }, // overlaps the first, ends after the next starts
  { start: 15, end: 16 },
  { start: 30, end: 31 },
];
const near = (a, b) => assert.ok(Math.abs(a - b) < 1e-9, `${a} != ${b}`);

test('nextCueChange: the next start or the end of a shown cue, whichever comes first', () => {
  near(nextCueChange(cues, 0), 10);
  near(nextCueChange(cues, 10), 1); // the second starts at 11
  near(nextCueChange(cues, 11.5), 0.5); // the first ends at 12
  near(nextCueChange(cues, 12), 3); // only the long one shows; the third starts at 15
  near(nextCueChange(cues, 15.5), 0.5);
  near(nextCueChange(cues, 16), 4); // the long one ends at 20
  near(nextCueChange(cues, 25), 5);
  assert.equal(nextCueChange(cues, 31), Infinity);
  assert.equal(nextCueChange([], 5), Infinity);
});

test('cueCheckDelay: right after the boundary, never longer than the old poll', () => {
  assert.equal(cueCheckDelay(cues, 9.95), 54);
  assert.equal(cueCheckDelay(cues, 5), CUE_CHECK_MAX_MS);
  assert.equal(cueCheckDelay(cues, 31), CUE_CHECK_MAX_MS);
  // At the boundary itself: a few ms, never 0 or negative
  assert.ok(cueCheckDelay(cues, 10) >= 4);
});

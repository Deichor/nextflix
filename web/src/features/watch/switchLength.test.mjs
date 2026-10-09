// Run in a node:22 container: node --experimental-strip-types --test src/features/watch/switchLength.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { lengthsMatch, timeScale } from './switchLength.ts';

test('another cut of the same episode fits (GoT S01E01 WEB-DL vs BluRay)', () => {
  assert.ok(lengthsMatch(3702.1, 3695.1));
  assert.ok(lengthsMatch(3699.2, 3695.1));
  // max(30 s, 1 %): 37 s on a 62 min episode, 30 s on a short one
  assert.ok(lengthsMatch(3700, 3736));
  assert.ok(!lengthsMatch(3700, 3745));
  assert.ok(lengthsMatch(1300, 1329));
  assert.ok(!lengthsMatch(1300, 1335));
  // A pack's other episode, or a making-of
  assert.ok(!lengthsMatch(3700, 5160));
  // Unknown lengths never block a switch
  assert.ok(lengthsMatch(0, 3695));
});

test('the same time is the same moment within 10 s, else mapped by the lengths', () => {
  assert.equal(timeScale(3702.1, 3695.1), 1);
  assert.equal(timeScale(3700, 3710), 1);
  const s = timeScale(3700, 3730);
  assert.ok(Math.abs(1646 * s - 1659.3) < 0.1);
});

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { lengthsMatch, switchStart, timeScale } from './switchLength.ts';

test('lengthsMatch: up to max(30 s, 1 %) apart, unknown lengths always fit', () => {
  assert.equal(lengthsMatch(3695.1, 3702.1), true);
  assert.equal(lengthsMatch(3600, 3636), true);
  assert.equal(lengthsMatch(3600, 3640), false); // over 1 % of 3640 s
  assert.equal(lengthsMatch(7200, 7270), true); // 1 % of 7270 s
  assert.equal(lengthsMatch(7200, 7275), false);
  assert.equal(lengthsMatch(0, 5000), true);
  assert.equal(timeScale(3600, 3605), 1);
  assert.equal(timeScale(7200, 7260), 7260 / 7200);
});

test('switchStart: the same moment in a file of the same title', () => {
  assert.equal(switchStart(3600, 3605, 1800), 1800);
  // Over 10 s apart: mapped by the lengths
  assert.equal(switchStart(7200, 7260, 3600), 3630);
  // Unknown lengths: the position as it is
  assert.equal(switchStart(0, 3600, 1800), 1800);
  assert.equal(switchStart(3600, 0, 1800), 1800);
  // Early on a file that does not fit still plays from the position
  assert.equal(switchStart(7200, 3600, 20), 20);
});

test('switchStart: a file of another length goes back, with the playing file\'s position', () => {
  // 40 s apart at 2 h is within 1 % (72 s): the same title, kept (a fixed 60 s limit allowed less than lengthsMatch)
  assert.equal(switchStart(7200, 7240, 3600), 3600 * (7240 / 7200));
  // 70 s apart at 1 h is over max(30 s, 1 %): another cut, back to the playing file
  assert.equal(switchStart(3600, 3670, 1800), null);
  assert.equal(switchStart(5160, 10380, 1800), null);
  // Does not reach the moment
  assert.equal(switchStart(3600, 3600, 3595), null);
});

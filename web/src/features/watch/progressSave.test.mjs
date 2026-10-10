import { test } from 'node:test';
import assert from 'node:assert/strict';
import { savePosition } from './progressSave.ts';

test('a save stores the position, the end when finished', () => {
  assert.equal(savePosition(600, 1420, false, false), 600);
  assert.equal(savePosition(1290, 1420, true, false), 1420);
  // Too early, or the length is not known yet
  assert.equal(savePosition(5, 1420, false, false), null);
  assert.equal(savePosition(600, 0, false, false), null);
});

test('leaving from the end credits: the unmount save does not undo the finished mark', () => {
  // Anime whose credits start at ~91 %, below the 92 % finished threshold
  const duration = 1420;
  const creditsAt = 1292;
  assert.equal(savePosition(creditsAt, duration, true, false), duration);
  assert.equal(savePosition(creditsAt, duration, false, true), null);
  // Saving it finished again still works
  assert.equal(savePosition(creditsAt, duration, true, true), duration);
});

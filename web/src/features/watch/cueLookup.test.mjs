import { test } from 'node:test';
import assert from 'node:assert/strict';
import { activeCues, shownCues } from './cueLookup.ts';

const track = () => [
  { start: 1, end: 3, text: 'one' },
  { start: 4, end: 6, text: 'two' },
  { start: 7, end: 9, text: 'three' },
];

test('activeCues finds the cues showing at a time', () => {
  const cues = track();
  assert.deepEqual(activeCues(cues, 4.5).map(c => c.text), ['two']);
  assert.deepEqual(activeCues(cues, 3.5), []);
  assert.deepEqual(activeCues(cues, 0), []);
});

test('the same list shows the lines of the last frame', () => {
  const cues = track();
  const active = { cues, t: 4.5, shown: activeCues(cues, 4.5) };
  assert.equal(shownCues(active, cues), active.shown);
});

test('a live track reloading its cues keeps the line on screen', () => {
  const old = track();
  const active = { cues: old, t: 4.5, shown: activeCues(old, 4.5) };
  const reloaded = [...track(), { start: 10, end: 12, text: 'four' }];
  assert.deepEqual(shownCues(active, reloaded).map(c => c.text), ['two']);
});

test('another track never shows the old lines, and an empty list shows nothing', () => {
  const old = track();
  const active = { cues: old, t: 4.5, shown: activeCues(old, 4.5) };
  const other = [{ start: 4, end: 5, text: 'dos' }];
  assert.deepEqual(shownCues(active, other).map(c => c.text), ['dos']);
  assert.deepEqual(shownCues(active, [{ start: 20, end: 21, text: 'later' }]), []);
  assert.deepEqual(shownCues(active, []), []);
  assert.deepEqual(shownCues(null, old), []);
});

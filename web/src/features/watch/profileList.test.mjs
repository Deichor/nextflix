import { test } from 'node:test';
import assert from 'node:assert/strict';
import { listFor, storeLoad } from './profileList.ts';

const EMPTY = [];

test("another profile's cards never show after a switch", () => {
  const a = { profileId: 1, list: ['a1', 'a2'] };
  assert.deepEqual(listFor(a, 1, EMPTY), ['a1', 'a2']);
  assert.equal(listFor(a, 2, EMPTY), EMPTY);
  assert.equal(listFor(a, null, EMPTY), EMPTY);
  assert.equal(listFor(null, 1, EMPTY), EMPTY);
});

test("a late load for the previous profile does not replace the new profile's list", () => {
  const b = { profileId: 2, list: ['b1'] };
  assert.equal(storeLoad(b, { profileId: 1, list: ['a1'] }, 2), b);
  // Before B's own list arrived as well
  assert.equal(storeLoad(null, { profileId: 1, list: ['a1'] }, 2), null);
});

test('a load for the active profile is stored, also after switching back', () => {
  const b = { profileId: 2, list: ['b1'] };
  const a = { profileId: 1, list: ['a1'] };
  assert.equal(storeLoad(b, a, 1), a);
  assert.equal(storeLoad(null, a, 1), a);
});

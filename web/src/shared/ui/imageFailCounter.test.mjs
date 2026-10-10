import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createFailCounter } from './imageFailCounter.ts';

test('the same broken URL on many cards never blocks their own fallback', () => {
  const counter = createFailCounter(2);
  const cards = Array.from({ length: 10 }, () => ({}));
  for (const img of cards) assert.equal(counter.fail(img), false);
});

test('an element that keeps retrying a broken image is stopped after maxFails', () => {
  const counter = createFailCounter(2);
  const img = {};
  assert.equal(counter.fail(img), false);
  assert.equal(counter.fail(img), false);
  assert.equal(counter.fail(img), true);
  assert.equal(counter.fail(img), true);
});

test('a successful load starts the count over', () => {
  const counter = createFailCounter(2);
  const img = {};
  counter.fail(img);
  counter.fail(img);
  counter.loaded(img);
  assert.equal(counter.fail(img), false);
});

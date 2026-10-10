import { test } from 'node:test';
import assert from 'node:assert/strict';
import { peekEntry, storeEntry } from './answerCache.ts';

const MIN = 60000;

test('a fresh entry is returned, an expired one is dropped', () => {
  const cache = new Map();
  storeEntry(cache, 'a', 10 * MIN, 'A', 10, 0);
  assert.equal(peekEntry(cache, 'a', 5 * MIN, MIN), 'A');
  // Older than the caller wants, but still within its own ttl: kept
  assert.equal(peekEntry(cache, 'a', 5 * MIN, 6 * MIN), undefined);
  assert.ok(cache.has('a'));
  assert.equal(peekEntry(cache, 'a', 20 * MIN, 11 * MIN), undefined);
  assert.ok(!cache.has('a'));
});

test('reads keep an entry: the least recently used goes first', () => {
  const cache = new Map();
  storeEntry(cache, 'home', MIN, 'feed', 3, 0);
  storeEntry(cache, 'b', MIN, 'B', 3, 1);
  storeEntry(cache, 'c', MIN, 'C', 3, 2);
  assert.equal(peekEntry(cache, 'home', MIN, 3), 'feed');
  storeEntry(cache, 'd', MIN, 'D', 3, 4);
  assert.deepEqual(Array.from(cache.keys()), ['c', 'home', 'd']);
  assert.equal(peekEntry(cache, 'b', MIN, 5), undefined);
});

test('storing drops expired entries and replaces the same key', () => {
  const cache = new Map();
  storeEntry(cache, 'old', 10, 'x', 5, 0);
  storeEntry(cache, 'k', MIN, 1, 5, 20);
  storeEntry(cache, 'k', MIN, 2, 5, 30);
  assert.deepEqual(Array.from(cache.keys()), ['k']);
  assert.equal(peekEntry(cache, 'k', MIN, 31), 2);
});

// Run in a node:24 container: node --experimental-strip-types --test src/lib/rowLoad.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { allRowsLoaded, loadRows } from './rowLoad.ts';

test('all rows loaded: kept, an empty row included', async () => {
  const d = await loadRows({ a: Promise.resolve([1, 2]), b: Promise.resolve([]) });
  assert.deepEqual(d.rows, { a: [1, 2], b: [] });
  assert.equal(d.failed, false);
  assert.equal(allRowsLoaded(d), true);
});

test('a failed row is empty and the answer is not kept', async () => {
  const d = await loadRows({ a: Promise.resolve([1]), b: Promise.reject(new Error('timeout')) });
  assert.deepEqual(d.rows, { a: [1], b: [] });
  assert.equal(allRowsLoaded(d), false);
});

test('every row failed', async () => {
  const d = await loadRows({ a: Promise.reject(new Error('x')), b: Promise.reject(new Error('y')) });
  assert.deepEqual(d.rows, { a: [], b: [] });
  assert.equal(allRowsLoaded(d), false);
});

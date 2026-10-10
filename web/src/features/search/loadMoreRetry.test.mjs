import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadMoreRetryDelay } from './loadMoreRetry.ts';

test('a failed page is retried a few times with growing waits', () => {
  assert.equal(loadMoreRetryDelay(0), null);
  const waits = [1, 2, 3].map(loadMoreRetryDelay);
  assert.ok(waits.every(w => typeof w === 'number' && w > 0));
  assert.ok(waits[0] < waits[1] && waits[1] < waits[2]);
});

test('after the last automatic retry it waits for the Retry button', () => {
  assert.equal(loadMoreRetryDelay(4), null);
  assert.equal(loadMoreRetryDelay(10), null);
});

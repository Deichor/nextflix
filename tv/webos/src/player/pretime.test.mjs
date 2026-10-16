import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PRETIME_POLL_MS, PRETIME_TRIES, pretimeNext } from './pretime.ts';

function steps(over = {}) {
  const log = [];
  const s = {
    cachedSource: async () => { log.push('source'); return { hash: 'aa', index: 1 }; },
    subtitles: async src => { log.push(`list ${src.hash}`); return [{ id: 'x' }, { id: 'y' }]; },
    pick: items => items[1].id,
    pretime: async id => { log.push(`pre ${id}`); },
    wait: async ms => { log.push(`wait ${ms}`); },
    cancelled: () => false,
    ...over,
  };
  return { s, log };
}

test('pretimeNext: the source, its list, the pick, then the server times it', async () => {
  const { s, log } = steps();
  assert.equal(await pretimeNext(s), 'y');
  assert.deepEqual(log, ['source', 'list aa', 'pre y']);
});

test('pretimeNext: waits for the prefetch to find the source, at most PRETIME_TRIES lookups', async () => {
  let n = 0;
  const { s, log } = steps({ cachedSource: async () => (++n < 3 ? null : { hash: 'bb', index: 2 }) });
  assert.equal(await pretimeNext(s), 'y');
  assert.deepEqual(log, [`wait ${PRETIME_POLL_MS}`, `wait ${PRETIME_POLL_MS}`, 'list bb', 'pre y']);
  let asked = 0;
  const never = steps({ cachedSource: async () => { asked++; return null; } });
  assert.equal(await pretimeNext(never.s), null);
  assert.equal(asked, PRETIME_TRIES);
  assert.equal(never.log.filter(l => l.startsWith('pre')).length, 0);
});

test('pretimeNext: nothing to show, or the player moved on: nothing is timed', async () => {
  const none = steps({ pick: () => null });
  assert.equal(await pretimeNext(none.s), null);
  assert.deepEqual(none.log, ['source', 'list aa']);
  let stop = false;
  const gone = steps({ subtitles: async () => { stop = true; return [{ id: 'x' }, { id: 'y' }]; }, cancelled: () => stop });
  assert.equal(await pretimeNext(gone.s), null);
  assert.equal(gone.log.filter(l => l.startsWith('pre')).length, 0);
});

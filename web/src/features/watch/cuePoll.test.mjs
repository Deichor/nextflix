import { test } from 'node:test';
import assert from 'node:assert/strict';
import { AI_CUES_POLL_MS, LIVE_CUES_POLL_MS, PROVISIONAL_CUES_POLL_MS, cuePollMs, finalCues } from './cuePoll.ts';

test('finalCues: only a complete answer with final timing is final', () => {
  for (const sync of ['synced', 'unchanged', 'replaced', 'poor', 'original']) {
    assert.equal(finalCues({ partial: false, sync }), true, sync);
    assert.equal(finalCues({ partial: true, sync }), false, sync);
  }
  // Provisional timing without the partial mark (no live audio for 90 s, the end of the file heard)
  for (const sync of ['live', 'pending', 'failed']) assert.equal(finalCues({ partial: false, sync }), false, sync);
  // Unknown state: an older server or a hidden header
  assert.equal(finalCues({ partial: false, sync: '-' }), false);
  assert.equal(finalCues({ partial: false, sync: 'hidden' }), false);
});

test('cuePollMs: partial answers every 15 s (AI lines every 3 s), provisional ones every minute, final ones never', () => {
  assert.equal(cuePollMs({ partial: true, sync: 'live' }), LIVE_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: true, sync: 'original' }), LIVE_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: true, sync: 'pending', translating: true }), AI_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: false, sync: 'live' }), PROVISIONAL_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: false, sync: 'pending' }), PROVISIONAL_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: false, sync: 'failed' }), PROVISIONAL_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: false, sync: 'synced' }), null);
  assert.equal(cuePollMs({ partial: false, sync: 'original', translating: false }), null);
  assert.deepEqual([LIVE_CUES_POLL_MS, AI_CUES_POLL_MS, PROVISIONAL_CUES_POLL_MS], [15000, 3000, 60000]);
});

import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  AI_CUES_POLL_MS, LIVE_CUES_POLL_MS, PENDING_CUES_POLL_MS, PENDING_FAST_FOR_MS, PROVISIONAL_CUES_POLL_MS, cuePollMs, finalCues,
} from './cuePoll.ts';

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

test('cuePollMs: an untimed (pending) partial answer is asked every 5 s in the first 3 minutes of its viewing', () => {
  assert.equal(cuePollMs({ partial: true, sync: 'pending' }, 0), PENDING_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: true, sync: 'pending' }, PENDING_FAST_FOR_MS - 1), PENDING_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: true, sync: 'pending' }, PENDING_FAST_FOR_MS), LIVE_CUES_POLL_MS);
  // Without the time it was put on: the regular partial poll
  assert.equal(cuePollMs({ partial: true, sync: 'pending' }), LIVE_CUES_POLL_MS);
  // Timed already, an AI translation, or not partial: unchanged
  assert.equal(cuePollMs({ partial: true, sync: 'live' }, 0), LIVE_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: true, sync: 'pending', translating: true }, 0), AI_CUES_POLL_MS);
  assert.equal(cuePollMs({ partial: false, sync: 'pending' }, 0), PROVISIONAL_CUES_POLL_MS);
  assert.deepEqual([PENDING_CUES_POLL_MS, PENDING_FAST_FOR_MS], [5000, 180000]);
});

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { addKindSample, addSample, eventTime, leadFor, leadOf, leadSample, parseSamples } from './clockLead.ts';

test('a seek measured on the TV gives its jump', () => {
  // seeked at 196.534, 1.5 s of playback later currentTime said 198.243
  assert.equal(leadSample(196.534, 198.243, 1.5), 0.209);
  assert.equal(leadSample(100, 101.5, 1.5), 0);
  assert.equal(leadSample(100, 101.48, 1.5), 0); // timer jitter just under
});

test('stalls and missed events are not samples', () => {
  assert.equal(leadSample(100, 102.5, 1.5), null);
  assert.equal(leadSample(100, 100.2, 1.5), null);
  assert.equal(leadSample(100, NaN, 1.5), null);
});

test('the median of the last samples, the default without any', () => {
  assert.equal(leadOf([], 0.25), 0.25);
  assert.equal(leadOf([0.21, 0.29, 0.25], 0), 0.25);
  assert.equal(leadOf([0.2, 0.3], 0), 0.25);
  let s = [];
  for (let i = 0; i < 10; i++) s = addSample(s, i / 100);
  assert.deepEqual(s, [0.03, 0.04, 0.05, 0.06, 0.07, 0.08, 0.09]);
});

test('samples by kind: seeks win, else the stream mode\'s own, else the default', () => {
  let all = parseSamples(null);
  assert.deepEqual(all, { seek: [], muxed: [], split: [] });
  assert.equal(leadFor(all, 'muxed', 0.25), 0.25);
  all = addKindSample(all, 'muxed', 0.3);
  all = addKindSample(all, 'split', 0.1);
  assert.equal(leadFor(all, 'muxed', 0.25), 0.3);
  assert.equal(leadFor(all, 'split', 0.25), 0.1);
  all = addKindSample(all, 'seek', 0.22);
  assert.equal(leadFor(all, 'muxed', 0.25), 0.22);
  assert.equal(leadFor(all, 'split', 0.25), 0.22);
});

test('stored samples: bad values and the old untagged list are dropped', () => {
  assert.deepEqual(parseSamples([0.2, 0.3]), { seek: [], muxed: [], split: [] });
  assert.deepEqual(parseSamples({ seek: [0.2, -1, 'x', 0.9], muxed: 'no' }), { seek: [0.2], muxed: [], split: [] });
});

test('eventTime: the event\'s own time on the performance clock, else now', () => {
  assert.equal(eventTime(1000, 1200), 1000);
  assert.equal(eventTime(0, 1200), 1200);
  assert.equal(eventTime(1.7e12, 1200), 1200); // epoch ms
});

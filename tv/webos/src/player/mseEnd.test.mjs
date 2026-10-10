import { test } from 'node:test';
import assert from 'node:assert/strict';
import { allDataIn, endOfStreamDue } from './mseEnd.ts';

const lane = (x = {}) => ({ done: true, queued: 0, updating: false, ...x });

// MediaSource as the spec (and Chromium) has it: remove() or appendBuffer() on an ended one reopens it
class FakeMediaSource {
  readyState = 'open';
  ends = 0;
  endOfStream() {
    this.readyState = 'ended';
    this.ends++;
  }
  remove() {
    if (this.readyState === 'ended') this.readyState = 'open';
  }
}

test('ends once every stream is in and no SourceBuffer is busy', () => {
  assert.ok(endOfStreamDue('open', [lane()]));
  assert.ok(endOfStreamDue('open', [lane(), lane()]));
  assert.ok(!endOfStreamDue('open', [lane({ done: false })]));
  assert.ok(!endOfStreamDue('open', [lane(), lane({ queued: 1024 })]));
  assert.ok(!endOfStreamDue('open', [lane({ updating: true })]));
  assert.ok(!endOfStreamDue('closed', [lane()]));
});

test('not again while ended', () => {
  assert.ok(!endOfStreamDue('ended', [lane()]));
});

test('a remove() after the end reopens the MediaSource: endOfStream() is due again', () => {
  const ms = new FakeMediaSource();
  const lanes = [lane()];
  if (endOfStreamDue(ms.readyState, lanes)) ms.endOfStream();
  assert.equal(ms.readyState, 'ended');
  ms.remove();
  assert.equal(ms.readyState, 'open');
  // A one-shot "ended" flag never called it again, so the element never fired `ended`
  if (endOfStreamDue(ms.readyState, lanes)) ms.endOfStream();
  assert.equal(ms.readyState, 'ended');
  assert.equal(ms.ends, 2);
});

test('played data stays once the whole file is in (no remove that reopens it)', () => {
  assert.ok(allDataIn([lane(), lane()]));
  assert.ok(!allDataIn([lane(), lane({ done: false })]));
  assert.ok(!allDataIn([lane({ queued: 1 })]));
  // A busy SourceBuffer does not matter here: nothing more will be appended
  assert.ok(allDataIn([lane({ updating: true })]));
});

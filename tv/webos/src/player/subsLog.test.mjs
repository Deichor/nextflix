import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  DELAY_SETTLE_MS, DelayLog, SUBS_MOVE_LOG_MS, cueShift, cueSpan, logToken, noneFields, subsChange, subsFields, syncValue,
} from './subsLog.ts';

const cue = (start, text) => ({ start, end: start + 2, text });
const cues = (offset, n = 10) => Array.from({ length: n }, (_, i) => cue(60 + i * 10 + offset, `line ${i}`));
// The server's own rule (playbacklog tokenRe, 40 chars): a safe token passes it unchanged
const serverToken = s => s.slice(0, 40).replace(/[^A-Za-z0-9._:/+-]/g, '_') || '-';

test('logToken: release names become one safe token the server keeps as is', () => {
  assert.equal(logToken('Breaking.Bad.S02E10.720p.WEB-DL'), 'Breaking.Bad.S02E10.720p.WEB-DL');
  assert.equal(logToken('Türkçe Altyazı "Şahane"\nçeviri'), 'Turkce_Altyazi_Sahane_ceviri');
  assert.equal(logToken('  [HDTV] x264 '), 'HDTV_x264');
  assert.equal(logToken(''), '-');
  assert.equal(logToken(undefined), '-');
  assert.equal(logToken('"""'), '-');
  const long = logToken('The.Long.Release.Name.Of.Some.Show.S02E10.1080p.NF.WEB-DL.DDP5.1.x264-GROUP');
  assert.ok(long.length <= 40);
  assert.equal(serverToken(long), long);
  // Cut at the limit without a trailing "_"
  assert.equal(logToken('abc def', 4), 'abc');
});

test('syncValue: the raw header, "hidden" when a cross-origin answer hides it, "-" when there is none', () => {
  assert.equal(syncValue('synced', true), 'synced');
  assert.equal(syncValue('live', false), 'live');
  assert.equal(syncValue(' poor ', true), 'poor');
  assert.equal(syncValue('weird value;x=1', true), 'weird_value_x_1');
  assert.equal(syncValue(null, true), 'hidden');
  assert.equal(syncValue(null, false), '-');
  assert.equal(syncValue('', true), 'hidden');
});

test('cueSpan: first and last cue start at 0.1 s', () => {
  assert.deepEqual(cueSpan([]), {});
  assert.deepEqual(cueSpan([cue(1.234, 'a'), cue(2501.96, 'b')]), { first: 1.2, last: 2502 });
});

test('cueShift: median start change of cues matched by text', () => {
  assert.deepEqual(cueShift(cues(0), cues(0)), { shift: 0, moved: 0, matched: 10 });
  assert.deepEqual(cueShift(cues(0), cues(-3.24)), { shift: -3.2, moved: 10, matched: 10 });
  // A few lines moved more (a timing change in a pause): the median stays the common shift
  const next = cues(1.5);
  next[9] = cue(next[9].start + 20, next[9].text);
  assert.equal(cueShift(cues(0), next).shift, 1.5);
  assert.deepEqual(cueShift(cues(0), [cue(1, 'other')]), { moved: 0, matched: 0 });
});

test('subsChange: a new subtitle and the first answer are logged', () => {
  const a = { id: 'a', cues: cues(0), partial: true, sync: 'pending' };
  assert.deepEqual(subsChange(null, a), { log: true });
  assert.deepEqual(subsChange(a, { ...a, id: 'b' }), { log: true });
});

test('subsChange: the same answer again (a cache hit after a seek) is not', () => {
  const a = { id: 'a', cues: cues(0), partial: false, sync: 'synced' };
  assert.equal(subsChange(a, { ...a }).log, false);
  assert.equal(subsChange(a, { ...a, cues: cues(0) }).log, false);
});

test('subsChange: a poll that timed the cues is logged with the shift', () => {
  const a = { id: 'a', cues: cues(0), partial: true, sync: 'pending' };
  assert.deepEqual(subsChange(a, { id: 'a', cues: cues(2.5), partial: false, sync: 'synced' }), { log: true, shift: 2.5, moved: 10 });
  // Still partial, moved by a live timing step
  assert.deepEqual(subsChange(a, { ...a, cues: cues(-1) }), { log: true, shift: -1, moved: 10 });
  // Only the state changed
  assert.deepEqual(subsChange(a, { ...a, cues: cues(0), partial: false, sync: 'unchanged' }), { log: true, shift: undefined, moved: undefined });
});

test('subsChange: moved cues while partial are logged at most once a minute; a state change at once', () => {
  const a = { id: 'a', cues: cues(0), partial: true, sync: 'live' };
  assert.deepEqual(subsChange(a, { ...a, cues: cues(0.5) }, SUBS_MOVE_LOG_MS - 1), { log: false });
  // Against the last logged answer the shift adds up
  assert.deepEqual(subsChange(a, { ...a, cues: cues(1.2) }, SUBS_MOVE_LOG_MS), { log: true, shift: 1.2, moved: 10 });
  assert.equal(subsChange(a, { ...a, cues: cues(1.2), partial: false, sync: 'synced' }, 1000).log, true);
});

test('subsChange: a live track that only grew is not logged, its first cues and its end are', () => {
  const empty = { id: 'e', cues: [], partial: true, sync: 'hidden' };
  const some = { ...empty, cues: cues(0, 5) };
  assert.equal(subsChange(empty, some).log, true);
  const more = { ...some, cues: cues(0, 10) };
  assert.equal(subsChange(some, more).log, false);
  assert.equal(subsChange(more, { ...more, cues: cues(0, 12), partial: false }).log, true);
});

test('subsChange: cues replaced by another subtitle are logged', () => {
  const a = { id: 'a', cues: cues(0), partial: true, sync: 'pending' };
  const other = Array.from({ length: 10 }, (_, i) => cue(61 + i * 10, `satır ${i}`));
  assert.equal(subsChange(a, { ...a, cues: other }).log, true);
});

test('subsFields: the subs event fields', () => {
  const item = { id: 'b3M6MTIzNDU2Nzg5MDpoYXNoOjE=', source: 'turkcealtyazi', language: 'tr', release: 'Show S02E10 HDTV x264-LOL', label: 'Türkçe' };
  const shown = { id: item.id, cues: [cue(12.34, 'a'), cue(2560.04, 'b')], partial: true, sync: 'pending' };
  assert.deepEqual(subsFields(item, shown, 'open', 0), {
    id: 'b3M6MTIzNDU2Nzg5MDpoYXNo', cues: 2, partial: true, lang: 'tr', provider: 'turkcealtyazi',
    name: 'Show_S02E10_HDTV_x264-LOL', sync: 'pending', why: 'open', first: 12.3, last: 2560, subdelay: 0,
    shift: undefined, moved: undefined,
  });
  const f = subsFields(undefined, { ...shown, cues: [] }, 'poll', 1.5, { shift: -2, moved: 30 });
  assert.equal(f.provider, undefined);
  assert.equal(f.first, undefined);
  assert.equal(f.shift, -2);
  assert.equal(f.subdelay, 1.5);
  // The server drops events with more than 16 fields
  assert.ok(Object.keys(f).length <= 16);
});

test('noneFields: why, the profile language and the languages listed', () => {
  const items = [{ id: '1', source: 'subdl', language: 'en' }, { id: '2', source: 'embedded', language: 'eng' }, { id: '3', source: 'x', language: 'en' }];
  assert.deepEqual(noneFields('missing', 'tr', items, { audio: 'en' }), { why: 'missing', lang: 'tr', n: 3, have: 'en+eng', audio: 'en' });
  assert.deepEqual(noneFields('off', 'tr', null), { why: 'off', lang: 'tr', n: 0, have: undefined });
});

test('DelayLog: the settled value once the changes stop, with the value before', () => {
  const d = new DelayLog();
  d.change(0.5, 1000);
  d.change(1, 1500);
  d.change(1.5, 2000);
  assert.equal(d.take(2000 + DELAY_SETTLE_MS - 1), null);
  assert.deepEqual(d.take(2000 + DELAY_SETTLE_MS), { subdelay: 1.5, from: 0 });
  assert.equal(d.take(99999), null);
  assert.equal(d.current, 1.5);
  d.change(-2, 10000);
  assert.deepEqual(d.take(10000 + DELAY_SETTLE_MS), { subdelay: -2, from: 1.5 });
});

test('DelayLog: back where it was logs nothing; force logs a pending change at once', () => {
  const d = new DelayLog();
  d.change(0.5, 0);
  d.change(0, 100);
  assert.equal(d.take(10000), null);
  d.change(2, 20000);
  assert.deepEqual(d.take(20001, true), { subdelay: 2, from: 0 });
});

test('DelayLog: a new file resets the baseline, a pending change of the old one comes first', () => {
  const d = new DelayLog();
  d.change(3, 0);
  assert.deepEqual(d.reset(0, 500), { subdelay: 3, from: 0 });
  assert.equal(d.current, 0);
  assert.equal(d.reset(0, 600), null);
  d.change(1, 1000);
  assert.deepEqual(d.take(1000 + DELAY_SETTLE_MS), { subdelay: 1, from: 0 });
});

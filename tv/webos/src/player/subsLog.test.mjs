import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  DELAY_SETTLE_MS, DelayLog, NEAR_AFTER_S, SUBS_MOVE_LOG_MS, cueShift, cueSpan, idHash, logToken, noneFields, subsChange, subsFields, syncValue,
} from './subsLog.ts';

const cue = (start, text) => ({ start, end: start + 2, text });
const cues = (offset, n = 10) => Array.from({ length: n }, (_, i) => cue(60 + i * 10 + offset, `line ${i}`));
// The server's own rule (playbacklog tokenRe, 40 chars): a safe token passes it unchanged
const serverToken = s => s.slice(0, 40).replace(/[^A-Za-z0-9._:/+-]/g, '_') || '-';
// 100 cues every 10 s from 60 s; cues from..to (indexes) moved by `by`
const retimed = (from, to, by) => cues(0, 100).map((c, i) => (i >= from && i <= to ? cue(c.start + by, c.text) : c));

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

test('idHash: 8 hex chars of FNV-1a, different for ids that start alike', () => {
  assert.equal(idHash(''), '811c9dc5');
  assert.equal(idHash('a'), 'e40c292c');
  assert.equal(idHash('foobar'), 'bf9cf968');
  // Two subtitles timed for one file: the first 24 chars are the same
  const tr = 'c3luY3xhMjJmMzdiNDJiMWQzYmE0NDdmYThjZDAxOGU3NDY2ZWFhMGJhMTFmfDF8c3ViZjJtfHR1cmtpc2h8MzU3ODQ0NQ';
  const en = 'c3luY3xhMjJmMzdiNDJiMWQzYmE0NDdmYThjZDAxOGU3NDY2ZWFhMGJhMTFmfDF8c3ViZjJtfGVuZ2xpc2h8MzU3NjAzMQ';
  assert.equal(logToken(tr, 24), logToken(en, 24));
  // The same values as the Android app's SubsLogTest
  assert.equal(idHash(tr), '71abc72f');
  assert.equal(idHash(en), 'e6dc49ac');
  assert.equal(serverToken(idHash(tr)), idHash(tr));
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
  const s = cueShift(cues(0), cues(-3.24));
  assert.deepEqual([s.shift, s.moved, s.matched], [-3.2, 10, 10]);
  // A few lines moved more (a timing change in a pause): the median stays the common shift
  const next = cues(1.5);
  next[9] = cue(next[9].start + 20, next[9].text);
  assert.equal(cueShift(cues(0), next).shift, 1.5);
  assert.deepEqual(cueShift(cues(0), [cue(1, 'other')]), { moved: 0, matched: 0 });
});

test('cueShift: where the moved cues are and how far they moved, not only the median of all', () => {
  // A re-fit moved 23 cues of 100 by -1.5 s, one of them by -2.25 s: the median of all stays 0
  const next = retimed(40, 62, -1.5);
  next[50] = cue(next[50].start - 0.75, next[50].text);
  const s = cueShift(cues(0, 100), next);
  assert.equal(s.shift, 0);
  assert.equal(s.moved, 23);
  assert.equal(s.mfrom, 458.5);
  assert.equal(s.mto, 678.5);
  assert.equal(s.mshift, -1.5);
  assert.equal(s.mmax, -2.25);
  // Without the position nothing about the viewer
  assert.equal(s.near, undefined);
  assert.equal(s.here, undefined);
});

test('cueShift: moved cues near the viewer and the change of the line on screen', () => {
  const next = retimed(40, 62, -1.5);
  // The viewer at 500.5 sees line 44 (500-502): it moved by -1.5 s
  const at = cueShift(cues(0, 100), next, 500.5);
  assert.equal(at.here, -1.5);
  // Moved cues with a start (old or new) from 495.5 to 620.5: lines 44-56
  assert.equal(at.near, 13);
  // Between two lines (no cue shows): no `here`; far before the moves: none near
  const gap = cueShift(cues(0, 100), next, 105);
  assert.equal(gap.here, undefined);
  assert.equal(gap.near, 0);
  // A moved cue whose old start was near counts too (it left the viewer)
  const away = cues(0, 100).map((c, i) => (i === 10 ? cue(c.start + 300, c.text) : c));
  assert.equal(cueShift(cues(0, 100), away, 155).near, 1);
  assert.equal(cueShift(cues(0, 100), away, 160 + NEAR_AFTER_S + 1).near, 0);
});

test('cueShift: the line on screen is matched to its nearest start (its text may come more than once)', () => {
  const prev = [cue(100, 'Yes.'), cue(400, 'No.'), cue(900, 'Yes.')];
  const next = [cue(100, 'Yes.'), cue(400.4, 'No.'), cue(900.5, 'Yes.')];
  assert.equal(cueShift(prev, next, 901).here, 0.5);
  // The line on screen stayed while another moved
  assert.equal(cueShift(prev, next, 101).here, 0);
  assert.equal(cueShift(prev, next, 401.5).here, 0.4);
  // Its line is gone from the new answer
  assert.equal(cueShift([cue(10, 'a'), cue(20, 'b')], [cue(10.5, 'a')], 21).here, undefined);
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

test('subsChange: a poll that timed the cues is logged with the moves', () => {
  const a = { id: 'a', cues: cues(0), partial: true, sync: 'pending' };
  const timed = subsChange(a, { id: 'a', cues: cues(2.5), partial: false, sync: 'synced' });
  assert.equal(timed.log, true);
  assert.deepEqual([timed.moves.shift, timed.moves.moved, timed.moves.mshift], [2.5, 10, 2.5]);
  // Still partial, moved by a live timing step
  const step = subsChange(a, { ...a, cues: cues(-1) });
  assert.deepEqual([step.log, step.moves.shift, step.moves.moved], [true, -1, 10]);
  // Only the state changed
  assert.deepEqual(subsChange(a, { ...a, cues: cues(0), partial: false, sync: 'unchanged' }), { log: true });
});

test('subsChange: moves away from the viewer are logged at most once a minute; near the viewer or a state change at once', () => {
  const a = { id: 'a', cues: cues(0, 100), partial: true, sync: 'live' };
  const far = { ...a, cues: retimed(80, 99, 0.5) };
  // The viewer at 100 s, the moves from 860 s on
  assert.deepEqual(subsChange(a, far, SUBS_MOVE_LOG_MS - 1, 100), { log: false });
  // Against the last logged answer the moves add up
  const later = subsChange(a, { ...a, cues: retimed(80, 99, 1.2) }, SUBS_MOVE_LOG_MS, 100);
  assert.deepEqual([later.log, later.moves.mshift, later.moves.near], [true, 1.2, 0]);
  // Moves at the viewer (161 s, line 10 on screen): logged at once
  const near = subsChange(a, { ...a, cues: retimed(10, 30, -1.4) }, 1000, 161);
  assert.deepEqual([near.log, near.moves.moved, near.moves.near, near.moves.here], [true, 21, 13, -1.4]);
  assert.equal(subsChange(a, { ...a, cues: retimed(80, 99, 1.2), partial: false, sync: 'synced' }, 1000, 100).log, true);
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
    id: idHash(item.id), cues: 2, partial: true, lang: 'tr', provider: 'turkcealtyazi',
    name: 'Show_S02E10_HDTV_x264-LOL', sync: 'pending', why: 'open', first: 12.3, last: 2560, subdelay: 0,
    shift: undefined, moved: undefined, mfrom: undefined, mto: undefined, mshift: undefined, mmax: undefined,
    near: undefined, here: undefined,
  });
  const f = subsFields(undefined, { ...shown, cues: [] }, 'poll', 1.5, { shift: -2, moved: 30, matched: 600 });
  assert.equal(f.provider, undefined);
  assert.equal(f.first, undefined);
  assert.equal(f.shift, -2);
  assert.equal(f.subdelay, 1.5);
});

test('subsFields: a poll that moved cues near the viewer stays within the server limit of 16 fields', () => {
  const item = { id: 'x'.repeat(90), source: 'subf2m', language: 'tr', release: 'The.Mentalist.S03.WEBRip.Amazon.tr' };
  const prev = { id: item.id, cues: cues(0, 100), partial: true, sync: 'live' };
  const next = { ...prev, cues: retimed(40, 62, -1.5) };
  const ch = subsChange(prev, next, 1000, 500.5);
  const f = subsFields(item, next, 'poll', 0, ch.moves);
  const sent = Object.keys(f).filter(k => f[k] !== undefined);
  assert.equal(sent.length, 16);
  // The list item's fields come with the first event of a subtitle, not with its polls
  assert.equal(f.name, undefined);
  assert.deepEqual([f.mfrom, f.mto, f.mshift, f.mmax, f.near, f.here], [458.5, 678.5, -1.5, -1.5, 13, -1.5]);
  // Field names the server accepts
  assert.ok(sent.every(k => /^[a-z][a-z0-9_]{0,15}$/.test(k)));
  assert.equal(subsFields(item, next, 'open', 0).name, 'The.Mentalist.S03.WEBRip.Amazon.tr');
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

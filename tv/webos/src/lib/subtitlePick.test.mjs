import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bestSubtitleOf, lateSubtitles, pickSubtitle } from './subtitlePick.ts';

const sub = (id, language, x = {}) => ({ id, source: 'opensubtitles', language, label: '', release: '', hi: false, forced: false, synced: false, score: 0, ...x });

// Server order (ranking.go): in a language with holes the complete one comes ahead of a synced one with holes
const list = [
  sub('tr-complete', 'tr'),
  sub('tr-synced-holes', 'tr', { synced: true }),
  sub('en-forced', 'en', { forced: true, synced: true }),
  sub('en-1', 'en'),
  sub('en-2', 'en', { synced: true }),
];

test('bestSubtitleOf: the first one the server ranked, forced ones only when nothing else', () => {
  assert.equal(bestSubtitleOf(list, 'tr'), 'tr-complete');
  assert.equal(bestSubtitleOf(list, 'tur'), 'tr-complete');
  assert.equal(bestSubtitleOf(list, 'en'), 'en-1');
  assert.equal(bestSubtitleOf([sub('f', 'en', { forced: true })], 'en'), 'f');
  assert.equal(bestSubtitleOf(list, 'de'), null);
});

test('pickSubtitle: the viewer\'s choice, else Turkish for a Turkish profile, in the server\'s order', () => {
  assert.equal(pickSubtitle(list, { profileLang: 'tr' }), 'tr-complete');
  assert.equal(pickSubtitle(list, { profileLang: 'tr', audioLang: 'tur' }), null);
  assert.equal(pickSubtitle(list, { profileLang: 'en' }), null);
  assert.equal(pickSubtitle(list, { profileLang: 'tr', pref: 'off' }), null);
  assert.equal(pickSubtitle(list, { profileLang: 'tr', pref: { lang: 'en' } }), 'en-1');
  assert.equal(pickSubtitle(list, { profileLang: 'tr', pref: { lang: 'en', id: 'en-2' } }), 'en-2');
  // The chosen one is not on this file's list: the best of its language
  assert.equal(pickSubtitle(list, { profileLang: 'tr', pref: { lang: 'tr', id: 'gone' } }), 'tr-complete');
});

test('lateSubtitles: a newly arrived language picks its first regular subtitle unless the viewer chose', () => {
  const shown = [sub('en-1', 'en')];
  const fresh = [sub('tr-forced', 'tr', { forced: true }), sub('tr-a', 'tr'), sub('tr-b', 'tr', { synced: true })];
  const late = lateSubtitles(shown, fresh, 'tr', false);
  assert.equal(late.arrived, true);
  assert.equal(late.pick, 'tr-a');
  assert.deepEqual(late.items.map(s => s.id), ['tr-forced', 'tr-a', 'tr-b', 'en-1']);
  assert.equal(lateSubtitles(shown, fresh, 'tr', true).pick, null);
  // An AI translation does not count as the language
  const ai = lateSubtitles([sub('ai-tr', 'tr', { source: 'ai' })], fresh, 'tr', false);
  assert.equal(ai.arrived, true);
  assert.equal(lateSubtitles(fresh, [], 'tr', false).arrived, false);
});

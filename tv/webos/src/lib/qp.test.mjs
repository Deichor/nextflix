// Run in a node:24 container: node --test src/lib/qp.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { withQueryParams } from './qp.ts';

test('a path without a query gets both', () => {
  assert.equal(withQueryParams('/api/profiles', 'tr', 3), '/api/profiles?x_lang=tr&profile_id=3');
});

test('an existing query is extended', () => {
  assert.equal(
    withQueryParams('/api/top10?region=TR&type=movie', 'en', 2),
    '/api/top10?region=TR&type=movie&x_lang=en&profile_id=2',
  );
});

test('an existing profile_id is kept', () => {
  assert.equal(withQueryParams('/api/recommendations?profile_id=5', 'tr', 2), '/api/recommendations?profile_id=5&x_lang=tr');
  assert.equal(withQueryParams('/api/stream-progress?x=1&profile_id=5', 'en', 2), '/api/stream-progress?x=1&profile_id=5&x_lang=en');
});

test('no profile: only the language', () => {
  assert.equal(withQueryParams('/api/tmdb/movie/popular', 'en', null), '/api/tmdb/movie/popular?x_lang=en');
  assert.equal(withQueryParams('/api/tmdb/movie/popular?page=1', 'tr', 0), '/api/tmdb/movie/popular?page=1&x_lang=tr');
});

test('an explicit x_lang is not repeated', () => {
  assert.equal(withQueryParams('/api/recommendations?profile_id=4&x_lang=tr', 'en', 1), '/api/recommendations?profile_id=4&x_lang=tr');
});

test('subalt ?lang= is not mistaken for x_lang', () => {
  assert.equal(withQueryParams('/api/torrent-stream/subalt?lang=tr', 'en', 1), '/api/torrent-stream/subalt?lang=tr&x_lang=en&profile_id=1');
});

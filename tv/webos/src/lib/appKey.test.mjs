// Run in a node:24 container: node --test src/lib/appKey.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { withAppKey } from './appKey.ts';

const KEY = 'k3y';

test('server API and image URLs get the key', () => {
  assert.equal(withAppKey('http://nextflix.local:8252/api/profiles', KEY), 'http://nextflix.local:8252/api/profiles?app_key=k3y');
  assert.equal(
    withAppKey('http://192.168.1.5:8252/api/torrent-stream/release?hash=ab,cd', KEY),
    'http://192.168.1.5:8252/api/torrent-stream/release?hash=ab,cd&app_key=k3y',
  );
  assert.equal(withAppKey('http://127.0.0.1:8252/tmdb-img/t/p/w342/a.jpg', KEY), 'http://127.0.0.1:8252/tmdb-img/t/p/w342/a.jpg?app_key=k3y');
});

test('other URLs, a present key and an empty key are left alone', () => {
  assert.equal(withAppKey('http://nextflix.local:8252/api/x?app_key=k3y', KEY), 'http://nextflix.local:8252/api/x?app_key=k3y');
  assert.equal(withAppKey('http://nextflix.local:8252/api/x', ''), 'http://nextflix.local:8252/api/x');
  assert.equal(withAppKey('https://example.com/other', KEY), 'https://example.com/other');
  assert.equal(withAppKey('/api/relative', KEY), '/api/relative');
  assert.equal(withAppKey('http://evil.com/x/api/y', KEY), 'http://evil.com/x/api/y');
});

test('the key is URL-encoded', () => {
  assert.equal(withAppKey('http://h:8252/api/x', 'a&b'), 'http://h:8252/api/x?app_key=a%26b');
});

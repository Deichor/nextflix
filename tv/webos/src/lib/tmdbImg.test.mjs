import { test } from 'node:test';
import assert from 'node:assert/strict';
import { tmdbImgUrl } from './tmdbImg.ts';

const S = 'http://192.168.1.221:8252';

test('a bare TMDB path goes through the server proxy', () => {
  assert.equal(tmdbImgUrl(S, '/abc.jpg', 'w500'), `${S}/tmdb-img/t/p/w500/abc.jpg`);
  assert.equal(tmdbImgUrl(S, 'abc.jpg', 'w500'), `${S}/tmdb-img/t/p/w500/abc.jpg`);
});

test('an image.tmdb.org URL is proxied in the asked size', () => {
  assert.equal(tmdbImgUrl(S, 'https://image.tmdb.org/t/p/w1280/xyz.png', 'w300'), `${S}/tmdb-img/t/p/w300/xyz.png`);
});

test("the server's relative /tmdb-img path gets the server address and the asked size", () => {
  assert.equal(tmdbImgUrl(S, '/tmdb-img/t/p/w500/poster.jpg', 'w342'), `${S}/tmdb-img/t/p/w342/poster.jpg`);
  assert.equal(tmdbImgUrl(S, '/tmdb-img/t/p/original/bg.jpg', 'w1280'), `${S}/tmdb-img/t/p/w1280/bg.jpg`);
});

test('no path, no URL', () => {
  assert.equal(tmdbImgUrl(S, '', 'w500'), '');
  assert.equal(tmdbImgUrl(S, null, 'w500'), '');
  assert.equal(tmdbImgUrl(S, undefined, 'w500'), '');
});

// Run in a node:24 container: node --test src/lib/homeMerge.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mergeHome } from './homeMerge.ts';

const t = key => ({ key });
const feeds = (movie, tv, missing) => ({
  trendingMovies: [t(movie)], trendingWeekMovies: [], nowPlaying: [], upcomingMovies: [],
  trendingTv: [t(tv)], popularTv: [], onTheAirTv: [], airingToday: [],
  ...(missing ? { missing } : {}),
});
const home = over => ({
  feeds: feeds('m1', 't1'), popular: [t('p1')], top10Movie: [t('r1')], top10Tv: [t('r2')],
  genres: { horror: [t('h1')], comedies: [t('c1')] }, complete: true, ...over,
});

test('no previous answer: the fresh one as it is', () => {
  const fresh = home({ popular: null, complete: false });
  assert.equal(mergeHome(fresh, undefined), fresh);
});

test('null parts keep the previous ones', () => {
  const prev = home({});
  const fresh = home({ feeds: null, top10Tv: null, genres: { horror: null, comedies: [t('c2')] }, complete: false });
  const out = mergeHome(fresh, prev);
  assert.equal(out.feeds, prev.feeds);
  assert.equal(out.top10Tv, prev.top10Tv);
  assert.equal(out.popular, fresh.popular);
  assert.deepEqual(out.genres, { horror: [t('h1')], comedies: [t('c2')] });
  assert.equal(out.complete, false);
});

test('a missing feeds half comes from the previous answer', () => {
  const out = mergeHome(home({ feeds: feeds('', 't2', 'movies') }), home({}));
  assert.deepEqual(out.feeds.trendingMovies, [t('m1')]);
  assert.deepEqual(out.feeds.trendingTv, [t('t2')]);
  assert.equal(out.feeds.missing, undefined);
});

test('the same half missing twice stays missing', () => {
  const out = mergeHome(home({ feeds: feeds('', 't2', 'tv') }), home({ feeds: feeds('m1', '', 'tv') }));
  assert.equal(out.feeds.missing, 'tv');
});

test('unknown previous genres are dropped, missing ones are null', () => {
  const out = mergeHome(home({ genres: { horror: null } }), home({ genres: { old: [t('o')] } }));
  assert.deepEqual(out.genres, { horror: null });
});

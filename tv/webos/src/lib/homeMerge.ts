// Merging a fresh home answer with the previous one. Pure, tested in homeMerge.test.mjs.
import type { HomeData, HomeFeeds } from './catalog';

const MOVIE_FEEDS = ['trendingMovies', 'trendingWeekMovies', 'nowPlaying', 'upcomingMovies'] as const;
const TV_FEEDS = ['trendingTv', 'popularTv', 'onTheAirTv', 'airingToday'] as const;

function mergeFeeds(fresh: HomeFeeds | null, prev: HomeFeeds | null | undefined): HomeFeeds | null {
  if (!fresh) return prev || null;
  if (!fresh.missing || !prev || prev.missing === fresh.missing) return fresh;
  const out: HomeFeeds = { ...fresh };
  delete out.missing;
  (fresh.missing === 'movies' ? MOVIE_FEEDS : TV_FEEDS).forEach(k => { out[k] = prev[k]; });
  return out;
}

/** A null part of `fresh` (failed, or not ready on the server) keeps the previous one. */
export function mergeHome(fresh: HomeData, prev: HomeData | null | undefined): HomeData {
  if (!prev) return fresh;
  const genres: HomeData['genres'] = {};
  Object.keys(fresh.genres).forEach(id => {
    const g = fresh.genres[id];
    genres[id] = g || (prev.genres && prev.genres[id]) || null;
  });
  return {
    feeds: mergeFeeds(fresh.feeds, prev.feeds),
    popular: fresh.popular || prev.popular || null,
    top10Movie: fresh.top10Movie || prev.top10Movie || null,
    top10Tv: fresh.top10Tv || prev.top10Tv || null,
    genres,
    complete: fresh.complete,
  };
}

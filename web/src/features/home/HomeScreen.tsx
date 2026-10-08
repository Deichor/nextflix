"use client";

import React, { useEffect, useMemo, useState } from "react";
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import Navbar from "@/features/shell/Navbar";
import ErrorBoundary from "@/shared/ui/ErrorBoundary";
import ContinueWatchingRow from '@/features/watch/components/ContinueWatchingRow';
import Billboard from '@/features/home/components/HomeBillboard';
import Row, { LazyRow } from '@/features/home/components/HomeRow';
import { TitleActionsProvider } from '@/features/home/components/TitleActions';
import {
  HomeFeeds,
  TitleItem,
  discoverMovies,
  fetchHomeFeeds,
  fetchPopularMovies,
  interleave,
} from '@/features/home/feeds';

const GENRE_ROWS: { title: string; params: Record<string, string> }[] = [
  { title: 'Action & Adventure Movies', params: { with_genres: '28|12', 'vote_count.gte': '300' } },
  { title: 'Comedies', params: { with_genres: '35', 'vote_count.gte': '300' } },
  { title: 'Sci-Fi & Fantasy', params: { with_genres: '878|14', 'vote_count.gte': '300' } },
  { title: 'Horror Movies', params: { with_genres: '27', 'vote_count.gte': '200' } },
  { title: 'Critically Acclaimed Movies', params: { sort_by: 'vote_average.desc', 'vote_count.gte': '5000' } },
];

// Billboard candidate: a well-known title with artwork and a synopsis
function pickFeatured(feeds: HomeFeeds): TitleItem | null {
  const pool = [...feeds.trendingMovies, ...feeds.trendingTv]
    .filter(t => t.backdrop_path && t.overview.length > 40)
    .slice(0, 6);
  if (pool.length === 0) return null;
  return pool[Math.floor(Math.random() * pool.length)];
}

export default function Home() {
  usePageTitle('Home');
  const [feeds, setFeeds] = useState<HomeFeeds | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    fetchHomeFeeds(ctrl.signal)
      .then(setFeeds)
      .catch(() => {
        if (!ctrl.signal.aborted) {
          setFeeds({ trendingMovies: [], trendingWeekMovies: [], upcomingMovies: [], trendingTv: [], popularTv: [], onTheAirTv: [] });
        }
      });
    return () => ctrl.abort();
  }, []);

  const featured = useMemo(() => (feeds ? pickFeatured(feeds) : null), [feeds]);
  const trending = useMemo(
    () => (feeds ? interleave(feeds.trendingMovies, feeds.trendingTv, 20) : []),
    [feeds],
  );
  const comingSoon = useMemo(() => {
    const list = feeds?.upcomingMovies ?? [];
    const today = new Date().toISOString().slice(0, 10);
    const future = list.filter(t => t.date >= today).sort((a, b) => a.date.localeCompare(b.date));
    // TMDB "upcoming" also lists recent releases; fall back to them only when few dates are ahead
    return future.length >= 5 ? future : list;
  }, [feeds]);
  const loading = !feeds;

  return (
    <TitleActionsProvider>
      <div className="min-h-screen bg-[#141414] text-white overflow-x-hidden">
        <Navbar />

        <ErrorBoundary>
          <Billboard item={featured} loading={loading} />
        </ErrorBoundary>

        <main className="relative z-10 -mt-6 sm:-mt-[7vw] pb-20 space-y-2 md:space-y-4">
          <ErrorBoundary>
            <ContinueWatchingRow />
          </ErrorBoundary>

          <Row title="Trending Now" items={trending} loading={loading} />
          <LazyRow title="Popular Movies" load={fetchPopularMovies} />
          <Row title="Popular TV Shows" items={feeds?.popularTv ?? []} loading={loading} />
          <Row title="Coming Soon" items={comingSoon} loading={loading} variant="poster" playable={false} />
          <Row title="New Episodes" items={feeds?.onTheAirTv ?? []} />

          {GENRE_ROWS.map(g => (
            <LazyRow key={g.title} title={g.title} load={signal => discoverMovies(g.params, signal)} />
          ))}
        </main>
      </div>
    </TitleActionsProvider>
  );
}

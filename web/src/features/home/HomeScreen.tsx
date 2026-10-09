"use client";

import React, { useEffect, useMemo, useState } from "react";
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import { useActiveProfile } from '@/shared/lib/profile';
import { useT, type MessageKey } from '@/shared/i18n';
import Navbar from "@/features/shell/Navbar";
import ErrorBoundary from "@/shared/ui/ErrorBoundary";
import ContinueWatchingRow from '@/features/watch/components/ContinueWatchingRow';
import Billboard from '@/features/home/components/HomeBillboard';
import HomeSurprise from '@/features/home/components/HomeSurprise';
import Row, { LazyRow } from '@/features/home/components/HomeRow';
import { TitleActionsProvider } from '@/features/home/components/TitleActions';
import { recommendationTitle, useRecommendations } from '@/features/home/useRecommendations';
import {
  HomeFeeds,
  TitleItem,
  discoverMovies,
  fetchHomeFeeds,
  fetchPopularMovies,
  fetchTop10,
  interleave,
} from '@/features/home/feeds';

// Keep the params in sync with homeGenreRows in server/internal/modules/images/warmer.go
const GENRE_ROWS: { title: MessageKey; params: Record<string, string> }[] = [
  { title: 'home.genreActionAdventure', params: { with_genres: '28|12', 'vote_count.gte': '300' } },
  { title: 'home.genreComedies', params: { with_genres: '35', 'vote_count.gte': '300' } },
  { title: 'home.genreSciFiFantasy', params: { with_genres: '878|14', 'vote_count.gte': '300' } },
  { title: 'home.genreHorror', params: { with_genres: '27', 'vote_count.gte': '200' } },
  { title: 'home.genreAcclaimedMovies', params: { sort_by: 'vote_average.desc', 'vote_count.gte': '5000' } },
];

const loadTop10Movies = (signal: AbortSignal) => fetchTop10('TR', 'movie', signal);
const loadTop10Tv = (signal: AbortSignal) => fetchTop10('TR', 'tv', signal);

// Billboard candidate: a well-known title with artwork and a synopsis
function pickFeatured(feeds: HomeFeeds): TitleItem | null {
  const pool = [...feeds.trendingMovies, ...feeds.trendingTv]
    .filter(t => t.backdrop_path && t.overview.length > 40)
    .slice(0, 6);
  if (pool.length === 0) return null;
  return pool[Math.floor(Math.random() * pool.length)];
}

export default function Home() {
  const t = useT();
  usePageTitle(t('nav.home'));
  const profile = useActiveProfile();
  const [feeds, setFeeds] = useState<HomeFeeds | null>(null);
  const recommendations = useRecommendations(profile?.id ?? null);

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
  // A profile without signals gets the server's "Trending Now" row; then the local one would repeat it
  const serverTrending = !!recommendations?.some(r => r.id === 'trending' || r.reason === 'popular');

  return (
    <TitleActionsProvider>
      <div className="min-h-screen bg-[#141414] text-white overflow-x-hidden">
        <Navbar />

        <div className="relative">
          <ErrorBoundary>
            <Billboard item={featured} loading={loading} />
          </ErrorBoundary>
          {!!profile && (
            <HomeSurprise className="absolute right-4 top-20 sm:top-auto sm:bottom-[30%] md:right-12 z-20 rounded-full border border-white/40 bg-black/40 px-4 py-2 text-sm md:text-base backdrop-blur-sm hover:bg-black/60 hover:border-white" />
          )}
        </div>

        <main className="relative z-10 -mt-6 sm:-mt-[7vw] pb-20 space-y-2 md:space-y-4">
          <ErrorBoundary>
            <ContinueWatchingRow />
          </ErrorBoundary>

          <ErrorBoundary>
            {recommendations === null && !!profile ? (
              <Row title="" items={[]} loading />
            ) : (
              (recommendations || []).map(r => (
                <Row key={r.id} title={recommendationTitle(t, r)} items={r.items} />
              ))
            )}
          </ErrorBoundary>

          {!serverTrending && <Row title={t('home.trendingNow')} items={trending} loading={loading} />}
          <LazyRow title={t('home.top10TrMovies')} load={loadTop10Movies} ranked />
          <LazyRow title={t('home.popularMovies')} load={fetchPopularMovies} />
          <LazyRow title={t('home.top10TrTv')} load={loadTop10Tv} ranked />
          <Row title={t('home.popularTv')} items={feeds?.popularTv ?? []} loading={loading} />
          <Row title={t('home.comingSoon')} items={comingSoon} loading={loading} variant="poster" playable={false} />
          <Row title={t('home.newEpisodes')} items={feeds?.onTheAirTv ?? []} />

          {GENRE_ROWS.map(g => (
            <LazyRow key={g.title} title={t(g.title)} load={signal => discoverMovies(g.params, signal)} />
          ))}
        </main>
      </div>
    </TitleActionsProvider>
  );
}

"use client";

import React, { useEffect, useMemo, useRef, useState } from "react";
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import { useActiveProfile } from '@/shared/lib/profile';
import { useLocale, useT, type MessageKey } from '@/shared/i18n';
import Navbar from "@/features/shell/Navbar";
import ErrorBoundary from "@/shared/ui/ErrorBoundary";
import ContinueWatchingRow from '@/features/watch/components/ContinueWatchingRow';
import { preloadPlayer } from '@/features/watch/components/LazyTorrentStreamPlayer';
import Billboard from '@/features/home/components/HomeBillboard';
import HomeSurprise from '@/features/home/components/HomeSurprise';
import Row, { NearRow } from '@/features/home/components/HomeRow';
import { TitleActionsProvider } from '@/features/home/components/TitleActions';
import { recommendationTitle, useRecommendations, whenPreviewClosed } from '@/features/home/useRecommendations';
import { readHomeSnapshot, writeHomeSnapshot } from '@/features/home/homeSnapshot';
import {
  HomeData,
  HomeFeeds,
  TitleItem,
  fetchHome,
  hasNullSection,
  interleave,
  mergeHome,
} from '@/features/home/feeds';

// Ids of GET /api/home's genre rows (params in HOME_GENRE_ROWS, feeds.ts); the titles are ours
const GENRE_ROWS: { id: string; title: MessageKey }[] = [
  { id: 'action_adventure', title: 'home.genreActionAdventure' },
  { id: 'comedies', title: 'home.genreComedies' },
  { id: 'scifi_fantasy', title: 'home.genreSciFiFantasy' },
  { id: 'horror', title: 'home.genreHorror' },
  { id: 'acclaimed', title: 'home.genreAcclaimedMovies' },
];

const EMPTY_HOME: HomeData = { feeds: null, popular: null, top10Movies: null, top10Tv: null, genres: {} };
const NO_TITLES: TitleItem[] = [];
// The player code loads in the background once Home has painted
const PLAYER_PRELOAD_MS = 3000;
// The server fills sections it could not answer within its deadline in the background
const HOME_RETRY_MS = 10000;
const HOME_RETRIES = 2;

const titleKey = (t: TitleItem) => `${t.media_type}-${t.id}`;

// Billboard candidate: a well-known title with artwork and a synopsis.
// The previous pick stays while it is still a candidate, so the billboard does not jump on a refresh.
function pickFeatured(feeds: HomeFeeds, prevKey: string | null): TitleItem | null {
  const candidates = [...feeds.trendingMovies, ...feeds.trendingTv]
    .filter(t => t.backdrop_path && t.overview.length > 40);
  const kept = prevKey ? candidates.find(t => titleKey(t) === prevKey) : undefined;
  if (kept) return kept;
  const pool = candidates.slice(0, 6);
  if (pool.length === 0) return null;
  return pool[Math.floor(Math.random() * pool.length)];
}

// The last launch's rows paint at once; Home only mounts on the client (after ProfileGate)
function snapshotOf(lang: string): HomeData | null {
  return typeof window === 'undefined' ? null : readHomeSnapshot(lang)?.data ?? null;
}

export default function Home() {
  const t = useT();
  usePageTitle(t('nav.home'));
  const profile = useActiveProfile();
  const lang = useLocale();
  // Rows per content language: null until the snapshot or the first answer is there
  const [home, setHome] = useState<{ lang: string; data: HomeData | null }>(() => ({ lang, data: snapshotOf(lang) }));
  if (home.lang !== lang) setHome({ lang, data: snapshotOf(lang) });
  const data = home.lang === lang ? home.data : null;
  const dataRef = useRef(data);
  useEffect(() => { dataRef.current = data; });
  const recommendations = useRecommendations(profile?.id ?? null);

  useEffect(() => {
    const ctrl = new AbortController();
    let cancelWait: (() => void) | undefined;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let retries = 0;
    // Missing sections (cold server cache, errors) are asked for again a few times
    const retry = () => {
      if (retries++ < HOME_RETRIES) retryTimer = setTimeout(load, HOME_RETRY_MS);
    };
    const load = () => fetchHome(lang, ctrl.signal)
      .then(fresh => {
        const apply = () => {
          // Sections the server could not fill this time keep the previous rows
          const merged = mergeHome(dataRef.current, fresh);
          setHome({ lang, data: merged });
          writeHomeSnapshot(lang, merged);
        };
        // Rows already on screen stay put under an open hover preview until it closes
        cancelWait?.();
        if (dataRef.current) cancelWait = whenPreviewClosed(apply);
        else apply();
        if (hasNullSection(fresh)) retry();
      })
      .catch(() => {
        if (ctrl.signal.aborted) return;
        // Keep what is shown; with nothing to show the rows give up until a retry answers
        setHome(prev => (prev.lang === lang && prev.data ? prev : { lang, data: EMPTY_HOME }));
        retry();
      });
    load();
    return () => {
      ctrl.abort();
      cancelWait?.();
      clearTimeout(retryTimer);
    };
  }, [lang]);

  useEffect(() => {
    // setTimeout, not requestIdleCallback: Safari has none
    const timer = setTimeout(preloadPlayer, PLAYER_PRELOAD_MS);
    return () => clearTimeout(timer);
  }, []);

  const feeds = data?.feeds ?? null;
  // The billboard keeps its title across refreshes (state adjusted during render)
  const [featuredFor, setFeaturedFor] = useState<{ feeds: HomeFeeds | null; item: TitleItem | null }>({ feeds: null, item: null });
  if (featuredFor.feeds !== feeds) {
    const prevKey = featuredFor.item ? titleKey(featuredFor.item) : null;
    setFeaturedFor({ feeds, item: feeds ? pickFeatured(feeds, prevKey) : null });
  }
  const featured = featuredFor.feeds === feeds ? featuredFor.item : null;

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
  const loading = !data;
  // A profile without signals gets the server's "Trending Now" row; then the local one would repeat it
  const serverTrending = !!recommendations?.some(r => r.id === 'trending' || r.reason === 'popular');

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
          <NearRow title={t('home.top10TrMovies')} items={data?.top10Movies ?? NO_TITLES} loading={loading} ranked />
          <NearRow title={t('home.popularMovies')} items={data?.popular ?? NO_TITLES} loading={loading} />
          <NearRow title={t('home.top10TrTv')} items={data?.top10Tv ?? NO_TITLES} loading={loading} ranked />
          <NearRow title={t('home.popularTv')} items={feeds?.popularTv ?? NO_TITLES} loading={loading} />
          <NearRow title={t('home.comingSoon')} items={comingSoon} loading={loading} variant="poster" playable={false} />
          <NearRow title={t('home.newEpisodes')} items={feeds?.onTheAirTv ?? NO_TITLES} />

          {GENRE_ROWS.map(g => (
            <NearRow key={g.id} title={t(g.title)} items={data?.genres[g.id] ?? NO_TITLES} loading={loading} />
          ))}

          {!!profile && <HomeSurprise />}
        </main>
      </div>
    </TitleActionsProvider>
  );
}

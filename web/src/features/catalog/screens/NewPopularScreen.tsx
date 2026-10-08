"use client";

import React, { useEffect, useMemo, useState } from 'react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import NetflixPage from '@/features/shell/PageShell';
import BrowseHeader from '@/features/catalog/components/BrowseHeader';
import TitleRow from '@/features/catalog/components/TitleRow';
import {
  MovieLists,
  NfTitle,
  TvLists,
  discoverMovies,
  fetchMovieLists,
  fetchTvLists,
  uniqueTitles,
} from '@/features/catalog/titles';

type Tab = 'all' | 'movies' | 'tv';

const TABS: { value: Tab; label: string }[] = [
  { value: 'all', label: 'Everything' },
  { value: 'movies', label: 'Movies' },
  { value: 'tv', label: 'TV Shows' },
];

export default function NewPopularPage() {
  usePageTitle('New & Popular');
  const [tab, setTab] = useState<Tab>('all');
  const [movies, setMovies] = useState<MovieLists | null>(null);
  const [tv, setTv] = useState<TvLists | null>(null);
  const [newReleases, setNewReleases] = useState<NfTitle[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    const since = new Date(Date.now() - 60 * 24 * 3600 * 1000).toISOString().slice(0, 10);

    Promise.all([
      fetchMovieLists().then(m => { if (!cancelled) setMovies(m); }),
      fetchTvLists().then(t => { if (!cancelled) setTv(t); }),
      discoverMovies({ sort_by: 'popularity.desc', 'primary_release_date.gte': since, 'vote_count.gte': 20 })
        .then(r => { if (!cancelled) setNewReleases(r); }),
    ]).finally(() => { if (!cancelled) setLoading(false); });

    return () => { cancelled = true; };
  }, []);

  const showMovies = tab !== 'tv';
  const showTv = tab !== 'movies';

  const comingSoon = useMemo(() => {
    const year = new Date().getFullYear();
    return (movies?.upcoming || []).filter(t => !t.year || t.year >= year);
  }, [movies]);

  return (
    <NetflixPage>
      <BrowseHeader
        title="New & Popular"
        right={
          <div className="flex gap-2">
            {TABS.map(t => (
              <button
                key={t.value}
                onClick={() => setTab(t.value)}
                className={`rounded-full px-3 py-1 text-xs font-semibold transition-colors md:text-sm ${
                  tab === t.value ? 'bg-white text-black' : 'border border-white/40 text-white hover:border-white'
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
        }
      />

      <div className="space-y-1 md:space-y-3">
        <TitleRow
          title="New on Nextflix"
          items={uniqueTitles(showMovies ? newReleases : [], showTv ? tv?.airingToday || [] : [])}
          variant="backdrop"
          loading={loading}
        />
        {showMovies && <TitleRow title="Top 10 Movies Today" items={movies?.trendingDaily || []} ranked loading={loading} />}
        {showTv && <TitleRow title="Top 10 TV Shows Today" items={tv?.trendingDaily || []} ranked loading={loading} />}
        {showMovies && <TitleRow title="Coming Soon" items={comingSoon} variant="backdrop" loading={loading} />}
        {showMovies && <TitleRow title="Worth the Wait" items={movies?.nowPlaying || []} loading={loading} />}
        {showTv && <TitleRow title="New Episodes This Week" items={tv?.onTheAir || []} variant="backdrop" loading={loading} />}
        {(showMovies || showTv) && (
          <TitleRow
            title="Everyone's Watching"
            items={uniqueTitles(showMovies ? movies?.trendingWeekly || [] : [], showTv ? tv?.trendingWeekly || [] : [])}
            loading={loading}
          />
        )}
      </div>
    </NetflixPage>
  );
}

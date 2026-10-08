"use client";

import React, { useEffect, useMemo, useState } from 'react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import NetflixPage from '@/features/shell/PageShell';
import BrowseHeader from '@/features/catalog/components/BrowseHeader';
import TitleRow from '@/features/catalog/components/TitleRow';
import { useT, type MessageKey } from '@/shared/i18n';
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

const TABS: { value: Tab; label: MessageKey }[] = [
  { value: 'all', label: 'catalog.everything' },
  { value: 'movies', label: 'common.movies' },
  { value: 'tv', label: 'common.tvShows' },
];

export default function NewPopularPage() {
  const t = useT();
  usePageTitle(t('nav.newPopular'));
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
        title={t('nav.newPopular')}
        right={
          <div className="flex gap-2">
            {TABS.map(opt => (
              <button
                key={opt.value}
                onClick={() => setTab(opt.value)}
                className={`rounded-full px-3 py-1 text-xs font-semibold transition-colors md:text-sm ${
                  tab === opt.value ? 'bg-white text-black' : 'border border-white/40 text-white hover:border-white'
                }`}
              >
                {t(opt.label)}
              </button>
            ))}
          </div>
        }
      />

      <div className="space-y-1 md:space-y-3">
        <TitleRow
          title={t('home.newOnNextflix')}
          items={uniqueTitles(showMovies ? newReleases : [], showTv ? tv?.airingToday || [] : [])}
          variant="backdrop"
          loading={loading}
        />
        {showMovies && <TitleRow title={t('home.top10Movies')} items={movies?.trendingDaily || []} ranked loading={loading} />}
        {showTv && <TitleRow title={t('home.top10Tv')} items={tv?.trendingDaily || []} ranked loading={loading} />}
        {showMovies && <TitleRow title={t('home.comingSoon')} items={comingSoon} variant="backdrop" loading={loading} />}
        {showMovies && <TitleRow title={t('home.worthTheWait')} items={movies?.nowPlaying || []} loading={loading} />}
        {showTv && <TitleRow title={t('home.newEpisodesThisWeek')} items={tv?.onTheAir || []} variant="backdrop" loading={loading} />}
        {(showMovies || showTv) && (
          <TitleRow
            title={t('catalog.everyonesWatching')}
            items={uniqueTitles(showMovies ? movies?.trendingWeekly || [] : [], showTv ? tv?.trendingWeekly || [] : [])}
            loading={loading}
          />
        )}
      </div>
    </NetflixPage>
  );
}

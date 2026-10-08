"use client";

import React, { Suspense, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from 'next/navigation';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import NetflixPage, { EmptyState } from '@/features/shell/PageShell';
import Billboard from '@/features/catalog/components/Billboard';
import BrowseHeader from '@/features/catalog/components/BrowseHeader';
import TitleRow from '@/features/catalog/components/TitleRow';
import TitleGrid from '@/features/catalog/components/TitleGrid';
import {
  NfTitle,
  TV_GENRES,
  TvLists,
  fetchProviderTv,
  fetchTvLists,
  pickBillboard,
  uniqueTitles,
} from '@/features/catalog/titles';

const PROVIDERS = [
  { id: 8, title: 'Popular on Netflix' },
  { id: 337, title: 'Disney+ Favorites' },
  { id: 1899, title: 'Max Originals & More' },
  { id: 9, title: 'Prime Video Picks' },
  { id: 350, title: 'Apple TV+ Series' },
];

// Genre rows built from the merged TV pool on the default page
const GENRE_ROWS = [10759, 35, 80, 10765, 18, 16, 99];

function TVShowsContent() {
  usePageTitle('TV Shows');
  const router = useRouter();
  const searchParams = useSearchParams();
  const genreParam = searchParams?.get('genre') || '';
  const genre = TV_GENRES.find(g => String(g.id) === genreParam) || null;

  const [lists, setLists] = useState<TvLists | null>(null);
  const [providers, setProviders] = useState<Record<number, NfTitle[]>>({});
  const [topRated, setTopRated] = useState<NfTitle[]>([]);
  const [loading, setLoading] = useState(true);

  // Lists render progressively; provider lists are slower on a cold backend cache
  useEffect(() => {
    let cancelled = false;
    fetchTvLists()
      .then(l => { if (!cancelled) setLists(l); })
      .finally(() => { if (!cancelled) setLoading(false); });
    PROVIDERS.forEach(p => {
      fetchProviderTv(p.id).then(items => {
        if (!cancelled) setProviders(prev => ({ ...prev, [p.id]: items }));
      });
    });
    fetchProviderTv(8, 'vote_average.desc').then(items => { if (!cancelled) setTopRated(items); });
    return () => { cancelled = true; };
  }, []);

  const pool = useMemo(() => uniqueTitles(
    lists?.trendingWeekly || [],
    lists?.trendingDaily || [],
    lists?.onTheAir || [],
    lists?.airingToday || [],
    ...PROVIDERS.map(p => providers[p.id] || []),
    topRated,
  ), [lists, providers, topRated]);

  const genreItems = useMemo(
    () => (genre ? pool.filter(t => t.genreIds?.includes(genre.id)) : []),
    [pool, genre]
  );

  const billboardSource = useMemo(
    () => (genre ? genreItems : uniqueTitles(lists?.trendingWeekly || [], lists?.trendingDaily || [])),
    [genre, genreItems, lists]
  );
  const [billboard, setBillboard] = useState<NfTitle | null>(null);
  const hasBillboardSource = billboardSource.length > 0;
  // Pick once per genre (not again on every late provider list)
  useEffect(() => {
    setBillboard(pickBillboard(billboardSource));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [genre?.id, hasBillboardSource]);

  const genreRows = useMemo(() => GENRE_ROWS.map(id => ({
    id,
    title: `${TV_GENRES.find(g => g.id === id)?.name} TV Shows`,
    items: pool.filter(t => t.genreIds?.includes(id)),
  })).filter(r => r.items.length >= 6), [pool]);

  const setGenre = (value: string) => {
    router.push(value ? `/tv-shows?genre=${value}` : '/tv-shows');
  };

  return (
    <NetflixPage>
      <div className="relative">
        <BrowseHeader
          overlay
          title="TV Shows"
          selectedLabel={genre?.name}
          options={TV_GENRES.map(g => ({ value: String(g.id), label: g.name }))}
          value={genre ? String(genre.id) : ''}
          onChange={setGenre}
        />
        <Billboard item={billboard} loading={loading} kicker="SERIES" />
      </div>

      {genre ? (
        <div className="relative z-10 -mt-[6vw] px-4 md:px-12">
          {genreItems.length > 0 ? (
            <TitleGrid items={genreItems} loading={loading} />
          ) : !loading ? (
            <EmptyState title={`No ${genre.name} shows yet`} text="Try another genre." />
          ) : (
            <TitleGrid items={[]} loading />
          )}
        </div>
      ) : (
        <div className="relative z-10 -mt-[8vw] space-y-1 md:space-y-3">
          <TitleRow title="Top 10 TV Shows Today" items={lists?.trendingDaily || []} ranked loading={loading} />
          <TitleRow title="Trending Now" items={lists?.trendingWeekly || []} variant="backdrop" loading={loading} />
          <TitleRow title={PROVIDERS[0].title} items={providers[PROVIDERS[0].id] || []} />
          <TitleRow title="New Episodes" items={lists?.airingToday || []} variant="backdrop" loading={loading} />
          {genreRows.slice(0, 2).map(r => <TitleRow key={r.id} title={r.title} items={r.items} />)}
          <TitleRow title="Currently Airing" items={lists?.onTheAir || []} />
          {PROVIDERS.slice(1).map(p => (
            <TitleRow key={p.id} title={p.title} items={providers[p.id] || []} variant={p.id === 337 ? 'backdrop' : 'poster'} />
          ))}
          <TitleRow title="Critically Acclaimed TV" items={topRated} variant="backdrop" />
          {genreRows.slice(2).map(r => <TitleRow key={r.id} title={r.title} items={r.items} />)}
        </div>
      )}
    </NetflixPage>
  );
}

export default function TVShowsPage() {
  return (
    <Suspense fallback={<div className="min-h-screen bg-[#141414]" />}>
      <TVShowsContent />
    </Suspense>
  );
}

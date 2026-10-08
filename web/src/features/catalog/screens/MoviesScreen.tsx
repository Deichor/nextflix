"use client";

import React, { Suspense, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from 'next/navigation';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import NetflixPage from '@/features/shell/PageShell';
import Billboard from '@/features/catalog/components/Billboard';
import BrowseHeader from '@/features/catalog/components/BrowseHeader';
import TitleRow from '@/features/catalog/components/TitleRow';
import {
  MOVIE_GENRES,
  NfTitle,
  discoverMovies,
  fetchMovieList,
  fetchMovieLists,
  pickBillboard,
  uniqueTitles,
} from '@/features/catalog/titles';

interface Row {
  title: string;
  items: NfTitle[];
  ranked?: boolean;
  variant?: 'poster' | 'backdrop';
}

// Genre rows shown on the default Movies page
const GENRE_ROWS = [28, 35, 878, 27, 10749, 16, 53, 99];

async function loadDefaultRows(): Promise<Row[]> {
  const year = new Date().getFullYear();
  const [lists, popular, topRated, recent, ...genreLists] = await Promise.all([
    fetchMovieLists(),
    fetchMovieList('popular'),
    discoverMovies({ sort_by: 'vote_average.desc', 'vote_count.gte': 3000 }),
    discoverMovies({ sort_by: 'popularity.desc', 'primary_release_date.gte': `${year - 1}-01-01`, 'vote_count.gte': 100 }),
    ...GENRE_ROWS.map(id => discoverMovies({ with_genres: id, sort_by: 'popularity.desc', 'vote_count.gte': 200 })),
  ]);
  const genreRows: Row[] = GENRE_ROWS.map((id, i) => ({
    title: `${MOVIE_GENRES.find(g => g.id === id)?.name ?? 'More'} Movies`,
    items: genreLists[i],
  }));
  return [
    { title: 'Top 10 Movies Today', items: lists.trendingDaily, ranked: true },
    { title: 'Trending Now', items: uniqueTitles(lists.trendingWeekly, lists.trendingDaily), variant: 'backdrop' },
    { title: 'Popular on Nextflix', items: popular },
    { title: 'New Releases', items: recent, variant: 'backdrop' },
    { title: 'Now Playing in Theaters', items: lists.nowPlaying },
    { title: 'Critically Acclaimed Movies', items: topRated },
    genreRows[0],
    genreRows[1],
    { title: 'Coming Soon', items: lists.upcoming, variant: 'backdrop' },
    ...genreRows.slice(2),
  ];
}

async function loadGenreRows(genreId: number): Promise<Row[]> {
  const year = new Date().getFullYear();
  const [popular, topRated, recent, classics, page2] = await Promise.all([
    discoverMovies({ with_genres: genreId, sort_by: 'popularity.desc', 'vote_count.gte': 100 }),
    discoverMovies({ with_genres: genreId, sort_by: 'vote_average.desc', 'vote_count.gte': 1500 }),
    discoverMovies({ with_genres: genreId, sort_by: 'popularity.desc', 'primary_release_date.gte': `${year - 2}-01-01`, 'vote_count.gte': 50 }),
    discoverMovies({ with_genres: genreId, sort_by: 'vote_count.desc', 'primary_release_date.lte': `${year - 15}-12-31` }),
    discoverMovies({ with_genres: genreId, sort_by: 'popularity.desc', 'vote_count.gte': 100, page: 2 }),
  ]);
  return [
    { title: 'Popular', items: popular, ranked: true },
    { title: 'New Releases', items: recent, variant: 'backdrop' },
    { title: 'Critically Acclaimed', items: topRated },
    { title: 'More to Explore', items: page2 },
    { title: 'Timeless Favorites', items: classics, variant: 'backdrop' },
  ];
}

function MoviesContent() {
  usePageTitle('Movies');
  const router = useRouter();
  const searchParams = useSearchParams();
  const genreParam = searchParams?.get('genre') || '';
  const genre = MOVIE_GENRES.find(g => String(g.id) === genreParam) || null;

  const [rows, setRows] = useState<Row[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setRows([]);
    (genre ? loadGenreRows(genre.id) : loadDefaultRows())
      .then(r => { if (!cancelled) setRows(r); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [genre?.id]);

  const billboard = useMemo(() => pickBillboard(rows.flatMap(r => r.items).slice(0, 40)), [rows]);

  const setGenre = (value: string) => {
    router.push(value ? `/movies?genre=${value}` : '/movies');
  };

  return (
    <NetflixPage>
      <div className="relative">
        <BrowseHeader
          overlay
          title="Movies"
          selectedLabel={genre?.name}
          options={MOVIE_GENRES.map(g => ({ value: String(g.id), label: g.name }))}
          value={genre ? String(genre.id) : ''}
          onChange={setGenre}
        />
        <Billboard item={billboard} loading={loading} kicker="FILM" />
      </div>

      <div className="relative z-10 -mt-[8vw] space-y-1 md:space-y-3">
        {loading && rows.length === 0
          ? Array.from({ length: 3 }).map((_, i) => <TitleRow key={i} title=" " items={[]} loading />)
          : rows.map(row => (
              <TitleRow key={row.title} title={row.title} items={row.items} ranked={row.ranked} variant={row.variant} />
            ))}
      </div>
    </NetflixPage>
  );
}

export default function MoviesPage() {
  return (
    <Suspense fallback={<div className="min-h-screen bg-[#141414]" />}>
      <MoviesContent />
    </Suspense>
  );
}

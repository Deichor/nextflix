"use client";

import React, { Suspense, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from 'next/navigation';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import NetflixPage from '@/features/shell/PageShell';
import Billboard from '@/features/catalog/components/Billboard';
import BrowseHeader from '@/features/catalog/components/BrowseHeader';
import TitleRow from '@/features/catalog/components/TitleRow';
import { useT, type TFunction } from '@/shared/i18n';
import {
  MOVIE_GENRES,
  NfTitle,
  discoverMovies,
  localGenre,
  fetchMovieList,
  fetchMovieLists,
  pickBillboard,
  uniqueTitles,
} from '@/features/catalog/titles';

interface Row {
  id: string;
  title: (t: TFunction) => string;
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
    id: `genre-${id}`,
    title: t => t('catalog.genreMovies', { genre: localGenre(t, id) ?? '' }),
    items: genreLists[i],
  }));
  return [
    { id: 'top10', title: t => t('home.top10Movies'), items: lists.trendingDaily, ranked: true },
    { id: 'trending', title: t => t('home.trendingNow'), items: uniqueTitles(lists.trendingWeekly, lists.trendingDaily), variant: 'backdrop' },
    { id: 'popular', title: t => t('home.popularOnNextflix'), items: popular },
    { id: 'new', title: t => t('home.newReleases'), items: recent, variant: 'backdrop' },
    { id: 'theaters', title: t => t('home.nowPlaying'), items: lists.nowPlaying },
    { id: 'acclaimed', title: t => t('home.genreAcclaimedMovies'), items: topRated },
    genreRows[0],
    genreRows[1],
    { id: 'coming', title: t => t('home.comingSoon'), items: lists.upcoming, variant: 'backdrop' },
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
    { id: 'popular', title: t => t('catalog.sortPopular'), items: popular, ranked: true },
    { id: 'new', title: t => t('home.newReleases'), items: recent, variant: 'backdrop' },
    { id: 'acclaimed', title: t => t('home.criticallyAcclaimed'), items: topRated },
    { id: 'more', title: t => t('home.moreToExplore'), items: page2 },
    { id: 'classics', title: t => t('catalog.timelessFavorites'), items: classics, variant: 'backdrop' },
  ];
}

function MoviesContent() {
  const t = useT();
  usePageTitle(t('nav.movies'));
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
          title={t('nav.movies')}
          selectedLabel={genre ? localGenre(t, genre.id) : undefined}
          options={MOVIE_GENRES.map(g => ({ value: String(g.id), label: localGenre(t, g.id) ?? g.name }))}
          value={genre ? String(genre.id) : ''}
          onChange={setGenre}
        />
        <Billboard item={billboard} loading={loading} kicker={t('catalog.kickerFilm')} />
      </div>

      <div className="relative z-10 -mt-6 space-y-1 sm:-mt-[7vw] md:space-y-3">
        {loading && rows.length === 0
          ? Array.from({ length: 3 }).map((_, i) => <TitleRow key={i} title=" " items={[]} loading />)
          : rows.map(row => (
              <TitleRow key={row.id} title={row.title(t)} items={row.items} ranked={row.ranked} variant={row.variant} />
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

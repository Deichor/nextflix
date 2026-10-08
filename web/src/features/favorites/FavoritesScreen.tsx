"use client";

import React, { useEffect, useMemo, useState } from 'react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { useActiveProfile, profileHeaders } from '@/shared/lib/profile';
import { getApiUrl } from '@/shared/lib/api';
import { useT } from '@/shared/i18n';
import NetflixPage, { EmptyState } from '@/features/shell/PageShell';
import BrowseHeader, { NfSelect } from '@/features/catalog/components/BrowseHeader';
import TitleGrid from '@/features/catalog/components/TitleGrid';
import { NfTitle, fromTmdb, getJson, tmdbDetailHref } from '@/features/catalog/titles';
import { tmdbImage } from '@/shared/lib/image';
import { Rating, useRatings } from '@/features/favorites/store';

type TypeFilter = 'all' | 'movie' | 'tv';
type SortBy = 'added' | 'title' | 'rating';

const keyOf = (r: { media_type: string; tmdb_id: number }) => `${r.media_type}-${r.tmdb_id}`;

// Favorite -> card. The title comes from the details in the profile language (the stored one
// is the language it was saved in); migrated rows may also lack artwork until the server's
// TMDB backfill ran.
function toTitle(r: Rating, details?: NfTitle | null): NfTitle {
  const posterPath = r.poster_path || details?.posterPath;
  const backdropPath = r.backdrop_path || details?.backdropPath;
  return {
    key: keyOf(r),
    id: r.tmdb_id,
    mediaType: r.media_type,
    title: details?.title || r.title || '',
    overview: details?.overview,
    posterPath,
    backdropPath,
    poster: tmdbImage(posterPath, 'w342'),
    backdrop: tmdbImage(backdropPath, 'w780'),
    year: details?.year,
    rating: details?.rating,
    genreIds: details?.genreIds,
    genreNames: details?.genreNames,
    href: tmdbDetailHref(r.tmdb_id, r.media_type),
    playHref: tmdbDetailHref(r.tmdb_id, r.media_type, true),
  };
}

async function fetchDetails(r: Rating): Promise<NfTitle | null> {
  const data = await getJson<any>(`/api/tmdb-movie/${r.tmdb_id}?type=${r.media_type}`);
  if (!data) return null;
  const body = data.data || data;
  return fromTmdb({ ...body, media_type: r.media_type }, r.media_type);
}

export default function FavoritesScreen() {
  const t = useT();
  usePageTitle(t('favorites.title'));
  const navigate = useNavigate();
  const profile = useActiveProfile();
  const { list, loaded } = useRatings();
  // Server order: most recently favorited first (rating a favorite later does not move it)
  const [order, setOrder] = useState<string[] | null>(null);
  const [details, setDetails] = useState<Record<string, NfTitle | null>>({});
  const [typeFilter, setTypeFilter] = useState<TypeFilter>('all');
  const [sortBy, setSortBy] = useState<SortBy>('added');

  const profileId = profile?.id ?? null;
  useEffect(() => {
    if (!profileId) return;
    let cancelled = false;
    setOrder(null);
    fetch(`${getApiUrl()}/api/favorites?profile_id=${profileId}`, { headers: profileHeaders(), cache: 'no-store' })
      .then(res => (res.ok ? res.json() : []))
      .then((rows: Rating[]) => { if (!cancelled) setOrder(Array.isArray(rows) ? rows.map(keyOf) : []); })
      .catch(() => { if (!cancelled) setOrder([]); });
    return () => { cancelled = true; };
  }, [profileId]);

  const favorites = useMemo(() => {
    const favs = list.filter(r => r.favorite);
    const pos = new Map((order || []).map((k, i) => [k, i]));
    // Added on this page (not in the server order yet) go first
    return favs.sort((a, b) => (pos.get(keyOf(a)) ?? -1) - (pos.get(keyOf(b)) ?? -1));
  }, [list, order]);

  // Details for rows without artwork / title, plus year and genres for every card (cached by getJson)
  useEffect(() => {
    const missing = favorites.filter(r => !(keyOf(r) in details));
    if (missing.length === 0) return;
    let cancelled = false;
    (async () => {
      const found: Record<string, NfTitle | null> = {};
      // A few at a time; large lists never fire dozens of requests at once
      for (let i = 0; i < missing.length; i += 6) {
        const chunk = missing.slice(i, i + 6);
        const res = await Promise.all(chunk.map(fetchDetails));
        if (cancelled) return;
        chunk.forEach((r, j) => { found[keyOf(r)] = res[j]; });
        setDetails(prev => ({ ...prev, ...found }));
      }
    })();
    return () => { cancelled = true; };
  }, [favorites, details]);

  const items = useMemo(() => {
    let out = favorites
      .map(r => toTitle(r, details[keyOf(r)]))
      .filter(x => !!x.title);
    if (typeFilter !== 'all') out = out.filter(x => x.mediaType === typeFilter);
    if (sortBy === 'title') out = [...out].sort((a, b) => a.title.localeCompare(b.title));
    if (sortBy === 'rating') out = [...out].sort((a, b) => (b.rating || 0) - (a.rating || 0));
    return out;
  }, [favorites, details, typeFilter, sortBy]);

  const waiting = !loaded || (favorites.length > 0 && items.length === 0 && favorites.some(r => !(keyOf(r) in details)));

  return (
    <NetflixPage>
      <BrowseHeader
        title={t('favorites.title')}
        right={
          favorites.length > 0 ? (
            <>
              <NfSelect
                label={t('search.type')}
                value={typeFilter}
                onChange={v => setTypeFilter(v as TypeFilter)}
                options={[
                  { value: 'all', label: t('common.all') },
                  { value: 'movie', label: t('common.movies') },
                  { value: 'tv', label: t('common.tvShows') },
                ]}
              />
              <NfSelect
                label={t('catalog.sort')}
                value={sortBy}
                onChange={v => setSortBy(v as SortBy)}
                options={[
                  { value: 'added', label: t('catalog.sortRecent') },
                  { value: 'title', label: t('catalog.sortAZ') },
                  { value: 'rating', label: t('catalog.sortRating') },
                ]}
              />
            </>
          ) : undefined
        }
      />

      <div className="px-4 pt-6 md:px-12">
        {loaded && favorites.length === 0 ? (
          <EmptyState
            title={t('favorites.empty')}
            text={t('favorites.emptyHint')}
            action={
              <button
                onClick={() => navigate.push('/')}
                className="rounded bg-white px-6 py-2 text-sm font-bold text-black transition-colors hover:bg-white/80"
              >
                {t('notifications.browseTitles')}
              </button>
            }
          />
        ) : items.length === 0 && !waiting ? (
          <EmptyState title={t('catalog.nothingHere')} text={t('catalog.noMatchFilter')} />
        ) : (
          <TitleGrid items={items} loading={waiting && items.length === 0} />
        )}
      </div>
    </NetflixPage>
  );
}

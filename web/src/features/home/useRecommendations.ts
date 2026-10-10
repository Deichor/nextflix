"use client";

import { useEffect, useMemo, useState } from 'react';
import { useLocale, type TFunction } from '@/shared/i18n';
import { getTitleRating, onRatingsChanged, useRatingsVersion } from '@/features/favorites/store';
import { RecommendationRow, fetchRecommendations } from '@/features/home/feeds';

// Last rows per profile and language, so coming back to Home paints them at once
const lastRows = new Map<string, RecommendationRow[]>();
const NO_ROWS: RecommendationRow[] = [];

// A rating change refreshes the rows after a short pause (the server recomputes them).
// The swap waits while a hover preview is open, so the card under the mouse does not vanish.
const REFRESH_DELAY_MS = 1500;

function previewOpen(): boolean {
  return !!document.querySelector('.nf-preview[data-expanded="true"]');
}

/** Personal rows of the profile; null while the first answer is pending. Disliked titles are hidden. */
export function useRecommendations(profileId: number | null): RecommendationRow[] | null {
  // Titles come in the profile language (X-Lang), so rows are kept and refetched per language
  const locale = useLocale();
  const rowsKey = `${profileId}:${locale}`;
  // Answer for one key; until the current key has one, the cached rows (or null) are shown
  const [fetched, setFetched] = useState<{ key: string; rows: RecommendationRow[] } | null>(null);
  const [refresh, setRefresh] = useState(0);
  const ratingsVersion = useRatingsVersion();
  const rows = !profileId ? NO_ROWS
    : fetched?.key === rowsKey ? fetched.rows
      : lastRows.get(rowsKey) ?? null;

  useEffect(() => {
    if (!profileId) return;
    const ctrl = new AbortController();
    fetchRecommendations(profileId, ctrl.signal, locale)
      .then(r => { lastRows.set(rowsKey, r); setFetched({ key: rowsKey, rows: r }); })
      .catch(err => {
        if (err?.name === 'AbortError') return;
        setFetched(prev => (prev?.key === rowsKey ? prev : { key: rowsKey, rows: lastRows.get(rowsKey) ?? [] }));
      });
    return () => ctrl.abort();
  }, [profileId, rowsKey, locale, refresh]);

  useEffect(() => {
    if (!profileId) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const schedule = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        if (previewOpen()) schedule();
        else setRefresh(n => n + 1);
      }, REFRESH_DELAY_MS);
    };
    const off = onRatingsChanged(r => { if (r.profile_id === profileId) schedule(); });
    return () => {
      off();
      if (timer) clearTimeout(timer);
    };
  }, [profileId]);

  return useMemo(() => {
    if (!rows) return null;
    return rows
      .map(r => ({ ...r, items: r.items.filter(i => getTitleRating(i.media_type, i.id)?.rating !== 'dislike') }))
      .filter(r => r.items.length > 0);
    // ratingsVersion: re-filter when a title gets disliked
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, ratingsVersion]);
}

const BECAUSE = /^Because you (?:loved|liked) (.+)$/;
const BECAUSE_FAVORITE = /^Because you added (.+) to Favorites$/;

/** Row title in the UI language; the title name inside comes from the server. */
export function recommendationTitle(t: TFunction, row: RecommendationRow): string {
  const name = (row.title.match(BECAUSE) || row.title.match(BECAUSE_FAVORITE))?.[1];
  switch (row.reason) {
    case 'top_picks':
      return t('home.recs.topPicks');
    case 'popular':
      return t('home.trendingNow');
    case 'because_loved':
      return name ? t('home.recs.becauseLoved', { title: name }) : row.title;
    case 'because_liked':
      return name ? t('home.recs.becauseLiked', { title: name }) : row.title;
    case 'because_favorite':
      return name ? t('home.recs.becauseFavorite', { title: name }) : row.title;
  }
  if (row.id === 'top-picks') return t('home.recs.topPicks');
  if (row.id === 'trending') return t('home.trendingNow');
  return row.title;
}

"use client";

import { useEffect, useMemo, useState } from 'react';
import { useLocale, type TFunction } from '@/shared/i18n';
import { getTitleRating, onRatingsChanged, useRatingsVersion } from '@/features/favorites/store';
import { RecommendationRow, fetchRecommendations } from '@/features/home/feeds';
import { readRecsSnapshot, writeRecsSnapshot } from '@/features/home/homeSnapshot';

// Last rows per profile and language, so coming back to Home paints them at once
const lastRows = new Map<string, RecommendationRow[]>();
const NO_ROWS: RecommendationRow[] = [];

// A rating change refreshes the rows after a short pause (the server recomputes them).
// The swap waits while a hover preview is open, so the card under the mouse does not vanish.
const REFRESH_DELAY_MS = 1500;
const PREVIEW_POLL_MS = 1000;

export function previewOpen(): boolean {
  return !!document.querySelector('.nf-preview[data-expanded="true"]');
}

/** Runs fn once no hover preview is open; returns a cancel function. */
export function whenPreviewClosed(fn: () => void): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const tryRun = () => {
    if (previewOpen()) timer = setTimeout(tryRun, PREVIEW_POLL_MS);
    else fn();
  };
  tryRun();
  return () => clearTimeout(timer);
}

// Memory first, then the snapshot of the last launch
// Keys whose snapshot was already read (or missing), so renders do not parse storage again
const snapshotRead = new Set<string>();

function cachedRows(profileId: number, locale: string, key: string): RecommendationRow[] | null {
  if (!lastRows.has(key) && !snapshotRead.has(key)) {
    snapshotRead.add(key);
    const snap = readRecsSnapshot(profileId, locale);
    if (snap) lastRows.set(key, snap);
  }
  return lastRows.get(key) ?? null;
}

/** Personal rows of the profile; null while the first answer is pending. Disliked titles are hidden. */
export function useRecommendations(profileId: number | null): RecommendationRow[] | null {
  // Titles come in the profile language (x_lang), so rows are kept and refetched per language
  const locale = useLocale();
  const rowsKey = `${profileId}:${locale}`;
  // Answer for one key; until the current key has one, the cached rows (or null) are shown
  const [fetched, setFetched] = useState<{ key: string; rows: RecommendationRow[] } | null>(null);
  const [refresh, setRefresh] = useState(0);
  const ratingsVersion = useRatingsVersion();
  const rows = !profileId ? NO_ROWS
    : fetched?.key === rowsKey ? fetched.rows
      : cachedRows(profileId, locale, rowsKey);

  useEffect(() => {
    if (!profileId) return;
    const ctrl = new AbortController();
    let cancelWait: (() => void) | undefined;
    fetchRecommendations(profileId, ctrl.signal, locale)
      .then(r => {
        const shown = lastRows.has(rowsKey);
        writeRecsSnapshot(profileId, locale, r);
        const apply = () => {
          lastRows.set(rowsKey, r);
          setFetched({ key: rowsKey, rows: r });
        };
        // Rows already on screen are swapped only while no preview is open
        if (shown) cancelWait = whenPreviewClosed(apply);
        else apply();
      })
      .catch(err => {
        if (err?.name === 'AbortError') return;
        setFetched(prev => (prev?.key === rowsKey ? prev : { key: rowsKey, rows: lastRows.get(rowsKey) ?? [] }));
      });
    return () => {
      ctrl.abort();
      cancelWait?.();
    };
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

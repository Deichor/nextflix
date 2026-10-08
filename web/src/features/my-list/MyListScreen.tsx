"use client";

import React, { useEffect, useMemo, useState } from "react";
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { useActiveProfile } from '@/shared/lib/profile';
import NetflixPage, { EmptyState } from '@/features/shell/PageShell';
import BrowseHeader, { NfSelect } from '@/features/catalog/components/BrowseHeader';
import TitleGrid from '@/features/catalog/components/TitleGrid';
import { MyListEntry, tmdbIdFromListId, useMyList } from '@/features/my-list/store';
import { NfTitle, fromTmdb, getJson } from '@/features/catalog/titles';

type TypeFilter = 'all' | 'movie' | 'tv';

const TYPE_OPTIONS = [
  { value: 'all', label: 'All' },
  { value: 'movie', label: 'Movies' },
  { value: 'tv', label: 'TV Shows' },
];

const SORT_OPTIONS = [
  { value: 'added', label: 'Recently Added' },
  { value: 'title', label: 'A-Z' },
  { value: 'rating', label: 'Highest Rated' },
];

async function resolveEntry(entry: MyListEntry): Promise<NfTitle | null> {
  const tmdbId = tmdbIdFromListId(entry.media_id);
  if (!tmdbId) return null;
  const type = entry.media_type === 'tv' ? 'tv' : 'movie';
  const data = await getJson<any>(`/api/tmdb-movie/${tmdbId}?type=${type}`);
  if (!data) return null;
  const body = data.data || data;
  return fromTmdb({ ...body, media_type: data.media_type || type }, type);
}

export default function MyListPage() {
  usePageTitle('My List');
  const navigate = useNavigate();
  const profile = useActiveProfile();
  const { entries, loaded } = useMyList();
  const [resolved, setResolved] = useState<Record<number, NfTitle | null>>({});
  const [typeFilter, setTypeFilter] = useState<TypeFilter>('all');
  const [sortBy, setSortBy] = useState('added');

  // Resolve TMDB metadata for entries we have not seen yet
  useEffect(() => {
    const missing = entries.filter(e => !(e.media_id in resolved));
    if (missing.length === 0) return;
    let cancelled = false;
    Promise.all(missing.map(async e => [e.media_id, await resolveEntry(e)] as const)).then(pairs => {
      if (cancelled) return;
      setResolved(prev => {
        const next = { ...prev };
        pairs.forEach(([id, t]) => { next[id] = t; });
        return next;
      });
    });
    return () => { cancelled = true; };
  }, [entries, resolved]);

  const items = useMemo(() => {
    let list = entries
      .map(e => resolved[e.media_id])
      .filter((t): t is NfTitle => !!t);
    if (typeFilter !== 'all') list = list.filter(t => t.mediaType === typeFilter);
    if (sortBy === 'title') list = [...list].sort((a, b) => a.title.localeCompare(b.title));
    if (sortBy === 'rating') list = [...list].sort((a, b) => (b.rating || 0) - (a.rating || 0));
    return list;
  }, [entries, resolved, typeFilter, sortBy]);

  const resolving = !loaded || entries.some(e => !(e.media_id in resolved));

  return (
    <NetflixPage>
      <BrowseHeader
        title="My List"
        right={
          entries.length > 0 ? (
            <>
              <NfSelect label="Type" value={typeFilter} onChange={v => setTypeFilter(v as TypeFilter)} options={TYPE_OPTIONS} />
              <NfSelect label="Sort" value={sortBy} onChange={setSortBy} options={SORT_OPTIONS} />
            </>
          ) : undefined
        }
      />
      {!!profile && (
        <p className="px-4 text-sm text-[#b3b3b3] md:px-12">Saved by {profile.name}</p>
      )}

      <div className="px-4 pt-6 md:px-12">
        {loaded && entries.length === 0 ? (
          <EmptyState
            title="You haven't added any titles to your list yet."
            text="Use the + button on any movie or show to save it here for later."
            action={
              <button
                onClick={() => navigate.push('/')}
                className="rounded bg-white px-6 py-2 text-sm font-bold text-black transition-colors hover:bg-white/80"
              >
                Browse titles
              </button>
            }
          />
        ) : items.length === 0 && !resolving ? (
          <EmptyState title="Nothing here" text="No titles match this filter." />
        ) : (
          <TitleGrid items={items} loading={resolving && items.length === 0} />
        )}
      </div>
    </NetflixPage>
  );
}

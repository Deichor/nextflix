"use client";

import React, { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useRouter, useSearchParams } from 'next/navigation';
import { Search, X } from 'lucide-react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import NetflixPage, { EmptyState } from '@/features/shell/PageShell';
import { NfSelect } from '@/features/catalog/components/BrowseHeader';
import TitleGrid from '@/features/catalog/components/TitleGrid';
import TitleRow from '@/features/catalog/components/TitleRow';
import {
  NfTitle,
  fetchMovieLists,
  fetchTvLists,
  fromTmdbList,
  getJson,
  uniqueTitles,
} from '@/features/catalog/titles';

type TypeFilter = 'all' | 'movie' | 'tv';

const TYPE_OPTIONS = [
  { value: 'all', label: 'All' },
  { value: 'movie', label: 'Movies' },
  { value: 'tv', label: 'TV Shows' },
];

function SearchContent() {
  usePageTitle('Search');
  const router = useRouter();
  const searchParams = useSearchParams();
  const urlQuery = searchParams?.get('q') || '';

  const [query, setQuery] = useState(urlQuery);
  const [results, setResults] = useState<NfTitle[]>([]);
  const [loading, setLoading] = useState(false);
  const [searched, setSearched] = useState(false);
  const [typeFilter, setTypeFilter] = useState<TypeFilter>('all');
  const [suggestions, setSuggestions] = useState<NfTitle[]>([]);
  const inputRef = useRef<HTMLInputElement>(null);

  const syncedQuery = useRef(urlQuery);

  // Navbar searches land here as ?q= (ignore our own URL updates)
  useEffect(() => {
    if (urlQuery !== syncedQuery.current) {
      syncedQuery.current = urlQuery;
      setQuery(urlQuery);
    }
  }, [urlQuery]);

  // Popular titles shown before typing
  useEffect(() => {
    Promise.all([fetchMovieLists(), fetchTvLists()]).then(([m, t]) => {
      setSuggestions(uniqueTitles(m.trendingDaily, t.trendingDaily, m.trendingWeekly, t.trendingWeekly).slice(0, 28));
    });
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setResults([]);
      setSearched(false);
      return;
    }
    let cancelled = false;
    const t = setTimeout(async () => {
      setLoading(true);
      const tmdb = await getJson<any>(`/api/tmdb/search?q=${encodeURIComponent(q)}&page=1`, { cache: false });
      if (cancelled) return;
      setResults(fromTmdbList(tmdb?.results));
      setSearched(true);
      setLoading(false);
      // Keep the URL shareable without adding history entries
      syncedQuery.current = q;
      router.replace(`/search?q=${encodeURIComponent(q)}`, { scroll: false });
    }, 300);
    return () => { cancelled = true; clearTimeout(t); };
  }, [query, router]);

  const filtered = useMemo(
    () => (typeFilter === 'all' ? results : results.filter(r => r.mediaType === typeFilter)),
    [results, typeFilter]
  );

  const related = useMemo(() => {
    const names = new Set<string>();
    results.slice(0, 12).forEach(r => r.genreNames?.forEach(n => names.add(n)));
    return Array.from(names).slice(0, 8);
  }, [results]);

  return (
    <NetflixPage>
      <div className="px-4 pt-20 md:px-12 md:pt-28">
        <div className="relative max-w-3xl">
          <Search className="pointer-events-none absolute left-4 top-1/2 h-6 w-6 -translate-y-1/2 text-[#b3b3b3]" />
          <input
            ref={inputRef}
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder="Titles, people, genres"
            className="w-full border border-white/30 bg-black/60 py-3.5 pl-14 pr-12 text-lg text-white placeholder-[#8c8c8c] focus:border-white focus:outline-none md:text-xl"
          />
          {!!query && (
            <button
              onClick={() => { setQuery(''); inputRef.current?.focus(); syncedQuery.current = ''; router.replace('/search', { scroll: false }); }}
              className="absolute right-4 top-1/2 -translate-y-1/2 text-[#b3b3b3] hover:text-white"
              aria-label="Clear search"
            >
              <X className="h-6 w-6" />
            </button>
          )}
        </div>

        {searched && (
          <div className="mt-6 flex flex-wrap items-center gap-x-4 gap-y-2">
            {related.length > 0 && (
              <p className="text-sm text-[#808080]">
                Explore titles related to:{' '}
                {related.map((g, i) => (
                  <span key={g} className="text-white">
                    {g}{i < related.length - 1 && <span className="mx-2 text-[#808080]">|</span>}
                  </span>
                ))}
              </p>
            )}
            <div className="ml-auto">
              <NfSelect label="Type" value={typeFilter} onChange={v => setTypeFilter(v as TypeFilter)} options={TYPE_OPTIONS} />
            </div>
          </div>
        )}
      </div>

      <div className="mt-6">
        {!query.trim() ? (
          <TitleRow title="Popular Searches" items={suggestions} variant="backdrop" loading={suggestions.length === 0} />
        ) : loading && !searched ? (
          <div className="px-4 md:px-12"><TitleGrid items={[]} loading /></div>
        ) : filtered.length > 0 ? (
          <div className={`px-4 transition-opacity md:px-12 ${loading ? 'opacity-60' : ''}`}>
            <TitleGrid items={filtered} />
          </div>
        ) : searched ? (
          <EmptyState
            title={`Your search for "${query.trim()}" did not have any matches.`}
            text="Try different keywords, or search for a movie or TV show title."
          />
        ) : null}
      </div>
    </NetflixPage>
  );
}

export default function SearchPage() {
  return (
    <Suspense fallback={<div className="min-h-screen bg-[#141414]" />}>
      <SearchContent />
    </Suspense>
  );
}

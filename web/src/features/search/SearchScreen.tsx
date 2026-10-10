"use client";

import React, { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useRouter, useSearchParams } from 'next/navigation';
import { Check, Search, X } from 'lucide-react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import NetflixPage, { EmptyState } from '@/features/shell/PageShell';
import TitleGrid from '@/features/catalog/components/TitleGrid';
import TitleRow from '@/features/catalog/components/TitleRow';
import {
  NfTitle,
  fetchMovieLists,
  fetchTvLists,
  fromTmdbList,
  localGenre,
  uniqueTitles,
} from '@/features/catalog/titles';
import SurpriseButton from '@/features/surprise/SurpriseButton';
import {
  GENRE_CHIPS,
  NO_FILTERS,
  SearchFilters,
  TypeFilter,
  chipFits,
  discover,
  filtersQuery,
  hasFilters,
  parseFilters,
} from '@/features/search/filters';
import { Completion, searchTitles, suggestTitles } from '@/features/search/api';
import { useT } from '@/shared/i18n';

const chipBase = 'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-3.5 py-1.5 text-sm font-medium transition-colors pointer-coarse:min-h-11';
const chipOn = 'border-white bg-white text-black';
const chipOff = 'border-white/30 bg-black/40 text-white/85 hover:border-white/70 hover:text-white';

function SearchContent() {
  const t = useT();
  usePageTitle(t('search.title'));
  const router = useRouter();
  const searchParams = useSearchParams();
  const urlQuery = searchParams?.get('q') || '';

  const [query, setQuery] = useState(urlQuery);
  const [filters, setFilters] = useState<SearchFilters>(() => parseFilters(searchParams));
  const [results, setResults] = useState<NfTitle[]>([]);
  const [loading, setLoading] = useState(false);
  const [searched, setSearched] = useState(false);
  const [page, setPage] = useState(1);
  const [totalPages, setTotalPages] = useState(1);
  const [suggestions, setSuggestions] = useState<NfTitle[]>([]);
  const [didYouMean, setDidYouMean] = useState('');
  const [completions, setCompletions] = useState<Completion[]>([]);
  const [activeCompletion, setActiveCompletion] = useState(-1);
  const [inputFocused, setInputFocused] = useState(false);
  // Completions only open while the user types, not for a query from the URL
  const [typing, setTyping] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const loadingMore = useRef(false);
  // Bumped on every new search so late "load more" pages of old filters are dropped
  const searchId = useRef(0);

  const syncedQuery = useRef(urlQuery);
  const q = query.trim();
  const filtered = hasFilters(filters);
  // Filters alone browse TMDB discover page by page; with a query they narrow the search results
  const browsing = !q && filtered;
  const idle = !q && !filtered;

  // Nothing to search any more: drop the old results (state adjusted during render)
  const [wasIdle, setWasIdle] = useState(idle);
  if (idle !== wasIdle) {
    setWasIdle(idle);
    if (idle) {
      setResults([]);
      setDidYouMean('');
      setSearched(false);
    }
  }
  // Too short to complete: close the list
  const short = q.length < 2;
  const [wasShort, setWasShort] = useState(short);
  if (short !== wasShort) {
    setWasShort(short);
    if (short) setCompletions([]);
  }

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

  // Keep the URL shareable without adding history entries
  useEffect(() => {
    const params = new URLSearchParams(filtersQuery(filters));
    if (q) params.set('q', q);
    const s = params.toString();
    const timer = setTimeout(() => {
      syncedQuery.current = q;
      router.replace(s ? `/search?${s}` : '/search', { scroll: false });
    }, 300);
    return () => clearTimeout(timer);
  }, [q, filters, router]);

  useEffect(() => {
    searchId.current += 1;
    loadingMore.current = false;
    if (!q && !filtered) return;
    let cancelled = false;
    const timer = setTimeout(async () => {
      setLoading(true);
      let items: NfTitle[] = [];
      let pages = 1;
      let corrected = '';
      if (filtered) {
        const res = await discover(filters, q, 1);
        items = fromTmdbList(res?.results);
        pages = res?.totalPages || 1;
        corrected = res?.didYouMean || '';
      } else {
        const res = await searchTitles(q);
        items = fromTmdbList(res?.results);
        corrected = res?.didYouMean || '';
      }
      if (cancelled) return;
      // Pages loaded before this first page landed belong to the old list
      searchId.current += 1;
      loadingMore.current = false;
      setResults(items);
      setDidYouMean(corrected);
      setPage(1);
      setTotalPages(pages);
      setSearched(true);
      setLoading(false);
    }, q ? 300 : 0);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [q, filters, filtered]);

  // Title completions while typing (server typo index, debounced 150 ms)
  useEffect(() => {
    if (short) return;
    const ctrl = new AbortController();
    const timer = setTimeout(async () => {
      const list = await suggestTitles(q, ctrl.signal);
      if (ctrl.signal.aborted) return;
      // Nothing to complete when the query already is the title
      setCompletions(list.filter(c => c.title.toLowerCase() !== q.toLowerCase()));
      setActiveCompletion(-1);
    }, 150);
    return () => { clearTimeout(timer); ctrl.abort(); };
  }, [q, short]);

  const pickQuery = (text: string) => {
    setQuery(text);
    setTyping(false);
    setCompletions([]);
    setActiveCompletion(-1);
  };

  const onInputKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (!completions.length) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const n = completions.length;
      setActiveCompletion(i => (e.key === 'ArrowDown' ? (i + 1) % n : (i - 1 + n) % n));
    } else if (e.key === 'Enter' && activeCompletion >= 0) {
      e.preventDefault();
      pickQuery(completions[activeCompletion].title);
    } else if (e.key === 'Escape') {
      setCompletions([]);
    }
  };

  const showCompletions = inputFocused && typing && completions.length > 0;

  // Infinite scroll while browsing by filters
  useEffect(() => {
    const el = sentinelRef.current;
    if (!el || !browsing || !searched || page >= totalPages) return;
    const io = new IntersectionObserver(async entries => {
      if (!entries.some(e => e.isIntersecting) || loadingMore.current) return;
      loadingMore.current = true;
      const id = searchId.current;
      const next = page + 1;
      const res = await discover(filters, '', next);
      if (id !== searchId.current) return;
      loadingMore.current = false;
      if (!res) return;
      setResults(prev => {
        const seen = new Set(prev.map(r => r.key));
        return [...prev, ...fromTmdbList(res.results).filter(r => !seen.has(r.key))];
      });
      setPage(next);
      setTotalPages(res.totalPages);
    }, { rootMargin: '800px 0px' });
    io.observe(el);
    return () => io.disconnect();
  }, [browsing, searched, page, totalPages, filters]);

  const setType = (type: TypeFilter) => setFilters(f => ({
    ...f,
    type,
    // Drop genres that cannot match the new type
    genres: f.genres.filter(id => chipFits(GENRE_CHIPS.find(g => g.id === id)?.types || [], type)),
  }));
  const toggleGenre = (id: number) => setFilters(f => ({
    ...f,
    genres: f.genres.includes(id) ? f.genres.filter(g => g !== id) : [...f.genres, id],
  }));

  const chips = useMemo(
    () => GENRE_CHIPS
      .filter(g => chipFits(g.types, filters.type))
      .map(g => ({ id: g.id, label: localGenre(t, g.id) || String(g.id) }))
      .sort((a, b) => a.label.localeCompare(b.label)),
    [t, filters.type],
  );

  return (
    <NetflixPage>
      <div className="px-4 pt-20 md:px-12 md:pt-28">
        <div className="flex flex-wrap items-center gap-3 md:gap-5">
          <div className="relative w-full max-w-3xl flex-1">
            <Search className="pointer-events-none absolute left-4 top-1/2 h-6 w-6 -translate-y-1/2 text-[#b3b3b3]" />
            <input
              ref={inputRef}
              value={query}
              onChange={e => { setQuery(e.target.value); setTyping(true); }}
              onKeyDown={onInputKey}
              onFocus={() => setInputFocused(true)}
              // Let a click on a completion land before the list closes
              onBlur={() => setTimeout(() => setInputFocused(false), 150)}
              placeholder={t('nav.searchPlaceholder')}
              aria-label={t('nav.search')}
              role="combobox"
              aria-autocomplete="list"
              aria-expanded={showCompletions}
              aria-controls="search-completions"
              className="w-full border border-white/30 bg-black/60 py-3.5 pl-14 pr-12 text-lg text-white placeholder-[#8c8c8c] focus:border-white focus:outline-none md:text-xl"
            />
            {showCompletions && (
              <ul
                id="search-completions"
                role="listbox"
                aria-label={t('search.suggestions')}
                className="absolute inset-x-0 top-full z-30 mt-1 border border-[#333] bg-[#141414] py-1 shadow-2xl"
              >
                {completions.map((c, i) => (
                  <li key={`${c.mediaType}-${c.id}`} role="option" aria-selected={i === activeCompletion}>
                    <button
                      type="button"
                      onMouseDown={e => e.preventDefault()}
                      onClick={() => pickQuery(c.title)}
                      className={`flex w-full items-center gap-3 px-4 py-2.5 text-left text-base text-white/90 hover:bg-white/10 ${i === activeCompletion ? 'bg-white/10' : ''}`}
                    >
                      <Search className="h-4 w-4 shrink-0 text-[#8c8c8c]" />
                      <span className="truncate">{c.title}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {!!query && (
              <button
                onClick={() => { setQuery(''); inputRef.current?.focus(); }}
                className="absolute right-4 top-1/2 -translate-y-1/2 text-[#b3b3b3] hover:text-white"
                aria-label={t('search.clear')}
              >
                <X className="h-6 w-6" />
              </button>
            )}
          </div>
          <SurpriseButton className="rounded border border-white/30 bg-black/40 px-4 py-3.5 hover:border-white" />
        </div>

        <div className="mt-5 flex flex-wrap items-center gap-2" role="group" aria-label={t('search.type')}>
          {(['all', 'movie', 'tv'] as TypeFilter[]).map(type => (
            <button
              key={type}
              onClick={() => setType(type)}
              aria-pressed={filters.type === type}
              className={`${chipBase} ${filters.type === type ? chipOn : chipOff}`}
            >
              {t(type === 'all' ? 'common.all' : type === 'movie' ? 'common.movies' : 'common.tvShows')}
            </button>
          ))}
          {filtered && (
            <button
              onClick={() => setFilters(NO_FILTERS)}
              className="ml-1 text-sm text-[#b3b3b3] underline-offset-2 hover:text-white hover:underline"
            >
              {t('search.clearFilters')}
            </button>
          )}
        </div>

        <div className="scrollbar-hide mt-3 flex flex-wrap gap-2 max-md:-mx-4 max-md:flex-nowrap max-md:overflow-x-auto max-md:px-4" role="group" aria-label={t('search.genres')}>
          <button
            onClick={() => setFilters(f => ({ ...f, anime: !f.anime }))}
            aria-pressed={filters.anime}
            className={`${chipBase} ${filters.anime ? chipOn : chipOff}`}
          >
            {filters.anime && <Check className="h-3.5 w-3.5" strokeWidth={3} />}
            {t('search.anime')}
          </button>
          {chips.map(g => {
            const on = filters.genres.includes(g.id);
            return (
              <button
                key={g.id}
                onClick={() => toggleGenre(g.id)}
                aria-pressed={on}
                className={`${chipBase} ${on ? chipOn : chipOff}`}
              >
                {on && <Check className="h-3.5 w-3.5" strokeWidth={3} />}
                {g.label}
              </button>
            );
          })}
        </div>
      </div>

      <div className="mt-6">
        {!q && !filtered ? (
          <TitleRow title={t('search.popular')} items={suggestions} variant="backdrop" loading={suggestions.length === 0} />
        ) : loading && !searched ? (
          <div className="px-4 md:px-12"><TitleGrid items={[]} loading /></div>
        ) : results.length > 0 ? (
          <div className={`px-4 transition-opacity md:px-12 ${loading ? 'opacity-60' : ''}`}>
            {!!q && !!didYouMean && (
              <p className="mb-4 text-lg text-[#b3b3b3]">
                {t.rich('search.didYouMean', {
                  title: (
                    <button
                      key="dym"
                      type="button"
                      onClick={() => pickQuery(didYouMean)}
                      className="font-semibold italic text-white underline-offset-4 hover:underline"
                    >
                      {didYouMean}
                    </button>
                  ),
                })}
              </p>
            )}
            <TitleGrid items={results} />
            {browsing && page < totalPages && (
              <div ref={sentinelRef} className="py-8 text-center text-sm text-[#808080]">{t('search.loadingMore')}</div>
            )}
          </div>
        ) : searched ? (
          q ? (
            <EmptyState
              title={t('search.noMatches', { query: q })}
              text={filtered ? t('search.noFilterMatchesHint') : t('search.noMatchesHint')}
            />
          ) : (
            <EmptyState title={t('search.noFilterMatches')} text={t('search.noFilterMatchesHint')} />
          )
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

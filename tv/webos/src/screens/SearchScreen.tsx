import { useEffect, useMemo, useRef, useState } from 'react';
import { Check, Search } from 'lucide-react';
import Keyboard from '../components/Keyboard';
import SideNav from '../components/SideNav';
import Spinner from '../components/Spinner';
import { CardImage } from '../components/Row';
import { homeFeedsCached, unique, type Title } from '../lib/catalog';
import {
  GENRE_CHIPS, NO_FILTERS, chipFits, discover, genreName, hasFilters,
  type SearchFilters, type TypeFilter,
} from '../lib/discover';
import { img, scoped } from '../lib/http';
import { searchFiltered, searchRanked, suggestTitles } from '../lib/search';
import { useT } from '../lib/i18n';
import { useQuery } from '../lib/useQuery';
import { useEntryState, useFocusRestore, useRouter } from '../nav/router';

const TYPES: TypeFilter[] = ['all', 'movie', 'tv'];
// Load the next browse page when focus is this close to the end of the grid
const MORE_AHEAD = 9;

const filtersKey = (f: SearchFilters) => `${f.type}|${f.genres.join(',')}|${f.anime ? 1 : 0}`;

export default function SearchScreen() {
  const t = useT();
  const router = useRouter();
  const [query, setQuery] = useEntryState('query', '');
  const [filters, setFilters] = useEntryState<SearchFilters>('filters', NO_FILTERS);
  const [results, setResults] = useEntryState<Title[] | null>('results', null);
  const [resultsFor, setResultsFor] = useEntryState('resultsFor', '');
  const [pages, setPages] = useEntryState('pages', { page: 1, total: 1 });
  const [didYouMean, setDidYouMean] = useEntryState('didYouMean', '');
  const [completions, setCompletions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const moreRef = useRef(false);
  const feeds = useQuery(scoped('feeds'), homeFeedsCached);
  const popular = useMemo(
    () => (feeds.data ? unique(feeds.data.trendingMovies, feeds.data.trendingTv, feeds.data.trendingWeekMovies, feeds.data.popularTv).slice(0, 24) : null),
    [feeds.data],
  );

  const q = query.trim();
  const filtered = hasFilters(filters);
  const browsing = !q && filtered;
  const wanted = `${q}#${filtersKey(filters)}`;

  useEffect(() => {
    if (!q && !filtered) { setResults(null); setDidYouMean(''); setLoading(false); return; }
    if (wanted === resultsFor && results) return;
    const ctrl = new AbortController();
    const timer = setTimeout(() => {
      setLoading(true);
      const load = !q
        ? discover(filters, '', 1, ctrl.signal).then(r => { setPages({ page: 1, total: r.totalPages }); return { items: r.items, didYouMean: '' }; })
        : filtered ? searchFiltered(filters, q, ctrl.signal) : searchRanked(q, ctrl.signal);
      load
        .then(r => { setResults(r.items); setDidYouMean(r.didYouMean); setResultsFor(wanted); })
        .catch(() => { if (!ctrl.signal.aborted) setResults([]); })
        .finally(() => { if (!ctrl.signal.aborted) setLoading(false); });
    }, q ? 400 : 150);
    return () => { clearTimeout(timer); ctrl.abort(); };
    // setters are stable for this entry
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wanted]);

  // Title completions while typing (server typo index, debounced 150 ms)
  useEffect(() => {
    if (q.length < 2) { setCompletions([]); return; }
    const ctrl = new AbortController();
    const timer = setTimeout(() => {
      suggestTitles(q, ctrl.signal)
        .then(list => setCompletions(list.filter(s => s.toLowerCase() !== q.toLowerCase())))
        .catch(() => { if (!ctrl.signal.aborted) setCompletions([]); });
    }, 150);
    return () => { clearTimeout(timer); ctrl.abort(); };
  }, [q]);

  const loadMore = () => {
    if (!browsing || moreRef.current || !results || pages.page >= pages.total || resultsFor !== wanted) return;
    moreRef.current = true;
    const next = pages.page + 1;
    discover(filters, '', next)
      .then(r => {
        const seen: Record<string, boolean> = {};
        results.forEach(it => { seen[it.key] = true; });
        setResults(results.concat(r.items.filter(it => !seen[it.key])));
        setPages({ page: next, total: r.totalPages });
      })
      .catch(() => {})
      .finally(() => { moreRef.current = false; });
  };

  const setType = (type: TypeFilter) => setFilters({
    ...filters,
    type,
    genres: filters.genres.filter(id => {
      const chip = GENRE_CHIPS.find(g => g.id === id);
      return !!chip && chipFits(chip.types, type);
    }),
  });
  const toggleGenre = (id: number) => setFilters({
    ...filters,
    genres: filters.genres.indexOf(id) >= 0 ? filters.genres.filter(g => g !== id) : filters.genres.concat(id),
  });

  const chips = useMemo(
    () => GENRE_CHIPS
      .filter(g => chipFits(g.types, filters.type))
      .map(g => ({ id: g.id, label: genreName(t, g.id) }))
      .sort((a, b) => a.label.localeCompare(b.label)),
    [t, filters.type],
  );

  useFocusRestore(true, 'kbd-a');

  const shown = q || filtered ? results : popular;
  const heading = q ? t('search.results') : filtered ? t('search.browse') : t('search.popular');

  return (
    <div className="screen search-screen">
      <SideNav active="search" />
      <div className="search-left">
        <div className="search-field">
          <Search className="search-field-icon" />
          {query ? <span>{query}</span> : <span className="placeholder">{t('nav.searchPlaceholder')}</span>}
          <span className="caret" />
        </div>
        <Keyboard layout="search" value={query} onChange={setQuery} max={60} />
        {completions.length > 0 && (
          <div className="search-suggest" data-nav-group="search-suggest">
            {completions.map((s, i) => (
              <button
                key={`suggest-${i}`}
                type="button"
                className="search-suggest-item"
                data-nav
                data-nav-id={`suggest-${i}`}
                onClick={() => setQuery(s)}
              >
                <Search className="search-suggest-icon" />
                <span>{s}</span>
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="search-results" data-scroll-y="edge" data-scroll-pad="3">
        <div data-scroll-content>
          <div className="search-chips" data-scroll-x="edge" data-scroll-pad="4" data-nav-group="search-chips">
            <div className="search-chips-content" data-scroll-content>
              {TYPES.map(type => (
                <button
                  key={type}
                  type="button"
                  className={`chip ${filters.type === type ? 'is-active' : ''}`}
                  data-nav
                  data-nav-id={`filter-type-${type}`}
                  data-nav-default={type === 'all' ? '' : undefined}
                  onClick={() => setType(type)}
                >
                  {t(type === 'all' ? 'common.all' : type === 'movie' ? 'common.movies' : 'common.tvShows')}
                </button>
              ))}
              <span className="search-chips-sep" />
              <button
                type="button"
                className={`chip ${filters.anime ? 'is-active' : ''}`}
                data-nav
                data-nav-id="filter-anime"
                onClick={() => setFilters({ ...filters, anime: !filters.anime })}
              >
                {filters.anime && <Check className="chip-check" strokeWidth={3} />}
                {t('search.anime')}
              </button>
              {chips.map(g => {
                const on = filters.genres.indexOf(g.id) >= 0;
                return (
                  <button
                    key={g.id}
                    type="button"
                    className={`chip ${on ? 'is-active' : ''}`}
                    data-nav
                    data-nav-id={`filter-genre-${g.id}`}
                    onClick={() => toggleGenre(g.id)}
                  >
                    {on && <Check className="chip-check" strokeWidth={3} />}
                    {g.label}
                  </button>
                );
              })}
            </div>
          </div>
          <div key={wanted}>
            {!!q && !!didYouMean && !loading && (
              <button
                type="button"
                className="chip did-you-mean"
                data-nav
                data-nav-id="did-you-mean"
                onClick={() => setQuery(didYouMean)}
              >
                {t('search.didYouMean', { title: didYouMean })}
              </button>
            )}
            <h2 className="row-title">{heading}</h2>
            {(loading || !shown) && <Spinner className="search-spinner" />}
            {!!shown && shown.length === 0 && !loading && (
              <div className="empty">
                <p className="empty-title">{q ? t('search.noMatches', { query: q }) : t('search.noFilterMatches')}</p>
                <p>{filtered ? t('search.noFilterMatchesHint') : t('search.noMatchesHint')}</p>
              </div>
            )}
            {!!shown && shown.length > 0 && (
              <div className="grid" data-nav-group="search-grid">
                {shown.map((item, i) => (
                  <button
                    key={item.key}
                    type="button"
                    className="card card-backdrop grid-card"
                    data-nav
                    data-nav-id={`search:${item.key}`}
                    onFocus={() => { if (i >= shown.length - MORE_AHEAD) loadMore(); }}
                    onClick={() => router.push({ name: 'detail', id: item.id, type: item.type })}
                  >
                    <CardImage src={img(item.backdrop || item.poster, 'w300')} />
                    <span className="card-caption">{item.title}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

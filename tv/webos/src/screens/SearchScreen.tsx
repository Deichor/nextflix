import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
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
import { useEntry, useEntryState, useFocusRestore, useRouter } from '../nav/router';
import { ensureVisible, focusById, focusElement, onFocusChange, pushKeyHandler } from '../nav/spatial';
import { direction, type Direction } from '../lib/webos';

const TYPES: TypeFilter[] = ['all', 'movie', 'tv'];
// Load the next browse page when focus is this close to the end of the grid
const MORE_AHEAD = 12;

const filtersKey = (f: SearchFilters) => `${f.type}|${f.genres.join(',')}|${f.anime ? 1 : 0}`;

// Nearest focusable in `dir` among `list` (Up / Down inside the left column, without group memory)
function nearest(cur: HTMLElement, dir: Direction, list: HTMLElement[]): HTMLElement | null {
  const f = cur.getBoundingClientRect();
  const fcx = (f.left + f.right) / 2;
  const fcy = (f.top + f.bottom) / 2;
  let pick: HTMLElement | null = null;
  let min = Infinity;
  list.forEach(el => {
    if (el === cur) return;
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return;
    const cy = (r.top + r.bottom) / 2;
    if (dir === 'down' ? cy <= fcy + 1 : cy >= fcy - 1) return;
    const along = dir === 'down' ? Math.max(0, r.top - f.bottom) : Math.max(0, f.top - r.bottom);
    const ortho = Math.max(0, r.left - f.right, f.left - r.right);
    const score = along * 2 + ortho * 3 + Math.abs((r.left + r.right) / 2 - fcx) * 0.5;
    if (score < min) { min = score; pick = el; }
  });
  return pick;
}

// Shows the fade under the left column while part of it is below the visible area
function updateFade(box: HTMLElement | null) {
  const content = box && (box.firstElementChild as HTMLElement | null);
  if (!box || !content) return;
  const m = /translate3d\([^,]+,\s*(-?[\d.]+)px/.exec(content.style.transform || '');
  const off = m ? -parseFloat(m[1]) : 0;
  box.classList.toggle('has-more', content.offsetTop + content.offsetHeight - off > box.clientHeight + 4);
}

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
  const entry = useEntry();
  const leftRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);
  const feeds = useQuery(scoped('feeds'), homeFeedsCached);
  const popular = useMemo(
    () => (feeds.data ? unique(feeds.data.trendingMovies, feeds.data.trendingTv, feeds.data.trendingWeekMovies, feeds.data.popularTv).slice(0, 24) : null),
    [feeds.data],
  );

  const q = query.trim();
  const filtered = hasFilters(filters);
  const browsing = !q && filtered;
  const wanted = `${q}#${filtersKey(filters)}`;
  const wantedRef = useRef(wanted);
  wantedRef.current = wanted;
  const moreCtrlRef = useRef<AbortController | null>(null);
  useEffect(() => () => { moreCtrlRef.current?.abort(); }, []);

  useEffect(() => {
    // A pending next page belongs to the old query
    if (moreCtrlRef.current) {
      moreCtrlRef.current.abort();
      moreCtrlRef.current = null;
      moreRef.current = false;
    }
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
    const forWanted = wanted;
    const ctrl = new AbortController();
    moreCtrlRef.current = ctrl;
    discover(filters, '', next, ctrl.signal)
      .then(r => {
        // A page of older filters must not land in the new results
        if (ctrl.signal.aborted || wantedRef.current !== forWanted) return;
        const seen: Record<string, boolean> = {};
        results.forEach(it => { seen[it.key] = true; });
        setResults(results.concat(r.items.filter(it => !seen[it.key])));
        setPages({ page: next, total: r.totalPages });
      })
      .catch(() => {})
      .finally(() => {
        if (moreCtrlRef.current !== ctrl) return;
        moreCtrlRef.current = null;
        moreRef.current = false;
      });
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

  // Up / Down stay in the left column and go to the nearest element (no jump back to a remembered key);
  // Left from the first card of a results row returns to the element last focused in the left column
  useEffect(() => {
    let lastMove = 0;
    return pushKeyHandler(e => {
      const dir = direction(e);
      const cur = document.activeElement as HTMLElement | null;
      if (!dir || !cur || !cur.hasAttribute('data-nav')) return false;
      const left = leftRef.current;
      const results = resultsRef.current;
      if (left && left.contains(cur) && (dir === 'up' || dir === 'down')) {
        const now = Date.now();
        if (e.repeat && now - lastMove < 70) return true;
        lastMove = now;
        const next = nearest(cur, dir, Array.prototype.slice.call(left.querySelectorAll('[data-nav]')));
        if (next) focusElement(next);
        return true;
      }
      if (results && results.contains(cur) && dir === 'left') {
        const f = cur.getBoundingClientRect();
        const list: HTMLElement[] = Array.prototype.slice.call(results.querySelectorAll('[data-nav]'));
        const sameRow = list.some(el => {
          const r = el.getBoundingClientRect();
          return el !== cur && r.right <= f.left + 1 && r.bottom > f.top && r.top < f.bottom;
        });
        if (sameRow) return false;
        return focusById(entry.state.leftFocus as string | undefined, true) || focusById('kbd-a', true);
      }
      return false;
    });
  }, [entry]);

  // Fade under the left column: after every focus move (the scroll is set by then) and content change
  useEffect(() => onFocusChange(() => updateFade(scrollRef.current)), []);
  useLayoutEffect(() => {
    const cur = document.activeElement as HTMLElement | null;
    if (cur && scrollRef.current && scrollRef.current.contains(cur)) ensureVisible(cur);
    updateFade(scrollRef.current);
  }, [completions, chips]);

  const shown = q || filtered ? results : popular;

  // New results start at the top unless the focus is in them (loading the next page)
  useLayoutEffect(() => {
    const box = resultsRef.current;
    const top = box && box.querySelector<HTMLElement>('[data-scroll-content] > div');
    if (top && !box.contains(document.activeElement)) ensureVisible(top, false);
  }, [shown]);

  const heading = q ? t('search.results') : filtered ? t('search.browse') : t('search.popular');

  return (
    <div className="screen search-screen">
      <SideNav active="search" />
      <div
        className="search-left"
        ref={leftRef}
        onFocus={e => {
          const id = (e.target as HTMLElement).getAttribute('data-nav-id');
          if (id) entry.state.leftFocus = id;
        }}
      >
        <div className="search-field">
          <Search className="search-field-icon" />
          {query ? <span>{query}</span> : <span className="placeholder">{t('nav.searchPlaceholder')}</span>}
          <span className="caret" />
        </div>
        <div className="search-left-scroll" ref={scrollRef} data-scroll-y="edge" data-scroll-pad="4.5">
          <div data-scroll-content>
            <Keyboard layout="search" value={query} onChange={setQuery} max={60} />
            {completions.length > 0 && (
              <div className="search-suggest">
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
            <div className="search-types">
              {TYPES.map(type => (
                <button
                  key={type}
                  type="button"
                  className={`search-type ${filters.type === type ? 'is-active' : ''}`}
                  data-nav
                  data-nav-id={`filter-type-${type}`}
                  onClick={() => setType(type)}
                >
                  {t(type === 'all' ? 'common.all' : type === 'movie' ? 'common.movies' : 'common.tvShows')}
                </button>
              ))}
            </div>
            <div className="search-genres">
              {[{ id: 0, label: t('search.anime') }].concat(chips).map(g => {
                const on = g.id === 0 ? filters.anime : filters.genres.indexOf(g.id) >= 0;
                return (
                  <button
                    key={g.id}
                    type="button"
                    className={`search-genre ${on ? 'is-active' : ''}`}
                    data-nav
                    data-nav-id={g.id === 0 ? 'filter-anime' : `filter-genre-${g.id}`}
                    onClick={() => (g.id === 0 ? setFilters({ ...filters, anime: !filters.anime }) : toggleGenre(g.id))}
                  >
                    {on && <Check className="chip-check" strokeWidth={3} />}
                    <span className="search-genre-label">{g.label}</span>
                  </button>
                );
              })}
            </div>
          </div>
        </div>
      </div>
      <div className="search-results" ref={resultsRef} data-scroll-y="edge" data-scroll-pad="3">
        <div data-scroll-content>
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

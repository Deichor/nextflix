import { useEffect, useMemo, useState } from 'react';
import { Search } from 'lucide-react';
import Keyboard from '../components/Keyboard';
import SideNav from '../components/SideNav';
import Spinner from '../components/Spinner';
import { CardImage } from '../components/Row';
import { search, homeFeedsCached, unique, type Title } from '../lib/catalog';
import { img, scoped } from '../lib/http';
import { useT } from '../lib/i18n';
import { useQuery } from '../lib/useQuery';
import { useEntryState, useFocusRestore, useRouter } from '../nav/router';

export default function SearchScreen() {
  const t = useT();
  const router = useRouter();
  const [query, setQuery] = useEntryState('query', '');
  const [results, setResults] = useEntryState<Title[] | null>('results', null);
  const [resultsFor, setResultsFor] = useEntryState('resultsFor', '');
  const [loading, setLoading] = useState(false);
  const feeds = useQuery(scoped('feeds'), homeFeedsCached);
  const popular = useMemo(
    () => (feeds.data ? unique(feeds.data.trendingMovies, feeds.data.trendingTv, feeds.data.trendingWeekMovies, feeds.data.popularTv).slice(0, 24) : null),
    [feeds.data],
  );

  useEffect(() => {
    const q = query.trim();
    if (!q) { setResults(null); setLoading(false); return; }
    if (q === resultsFor && results) return;
    const ctrl = new AbortController();
    const timer = setTimeout(() => {
      setLoading(true);
      search(q, ctrl.signal)
        .then(r => { setResults(r); setResultsFor(q); })
        .catch(() => { if (!ctrl.signal.aborted) setResults([]); })
        .finally(() => { if (!ctrl.signal.aborted) setLoading(false); });
    }, 400);
    return () => { clearTimeout(timer); ctrl.abort(); };
    // setResults is stable for this entry
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);

  useFocusRestore(true, 'kbd-a');

  const shown = query.trim() ? results : popular;
  const heading = query.trim() ? t('search.results') : t('search.popular');

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
      </div>
      <div className="search-results" data-scroll-y="edge" data-scroll-pad="3">
        <div key={query.trim()} data-scroll-content>
          <h2 className="row-title">{heading}</h2>
          {(loading || !shown) && <Spinner className="search-spinner" />}
          {!!shown && shown.length === 0 && !loading && (
            <div className="empty">
              <p className="empty-title">{t('search.noMatches', { query: query.trim() })}</p>
              <p>{t('search.noMatchesHint')}</p>
            </div>
          )}
          {!!shown && shown.length > 0 && (
            <div className="grid" data-nav-group="search-grid">
              {shown.map(item => (
                <button
                  key={item.key}
                  type="button"
                  className="card card-backdrop grid-card"
                  data-nav
                  data-nav-id={`search:${item.key}`}
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
  );
}

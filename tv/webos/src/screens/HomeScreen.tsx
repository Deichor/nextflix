import { useEffect, useMemo, useRef, useState } from 'react';
import RowsScreen from '../components/RowsScreen';
import type { RowDef } from '../components/Row';
import { invalidate } from '../lib/http';
import { useI18n, type I18n, type MessageKey } from '../lib/i18n';
import { getProfile, getProfileId, profileLocale } from '../lib/profile';
import { useQuery } from '../lib/useQuery';
import { useEntry } from '../nav/router';
import { homeQuery, interleave, type HomeData, type RecommendationRow, type Title } from '../lib/catalog';
import { readHomeSnapshot } from '../lib/homeSnapshot';
import { personalQueries } from '../lib/homeQueries';

// Ids of HOME_GENRES (lib/catalog.ts); keep in sync with GENRE_ROWS in web/src/features/home/HomeScreen.tsx
const GENRE_ROWS: { id: string; title: MessageKey }[] = [
  { id: 'action_adventure', title: 'home.genreActionAdventure' },
  { id: 'comedies', title: 'home.genreComedies' },
  { id: 'scifi_fantasy', title: 'home.genreSciFiFantasy' },
  { id: 'horror', title: 'home.genreHorror' },
  { id: 'acclaimed', title: 'home.genreAcclaimedMovies' },
];

const HOME_RETRIES = 3;
const HOME_RETRY_MS = 10 * 1000;

export function pickFeatured(pool: Title[], key: string | null): Title | null {
  const good = pool.filter(t => t.backdrop && t.overview.length > 40).slice(0, 6);
  return good.find(t => t.key === key) || good[Math.floor(Math.random() * good.length)] || null;
}

const BECAUSE = /^Because you (?:loved|liked) (.+)$/;
const BECAUSE_FAVORITE = /^Because you added (.+) to Favorites$/;

function recommendationTitle({ t }: I18n, row: RecommendationRow): string {
  const name = (row.title.match(BECAUSE) || row.title.match(BECAUSE_FAVORITE) || [])[1];
  switch (row.reason) {
    case 'top_picks': return t('home.recs.topPicks');
    case 'popular': return t('home.trendingNow');
    case 'because_loved': return name ? t('home.recs.becauseLoved', { title: name }) : row.title;
    case 'because_liked': return name ? t('home.recs.becauseLiked', { title: name }) : row.title;
    case 'because_favorite': return name ? t('home.recs.becauseFavorite', { title: name }) : row.title;
  }
  return row.id === 'trending' ? t('home.trendingNow') : row.title;
}

// Same content in a new answer (the snapshot replaced by the fresh one): the row is not re-rendered
function sameItems(a: unknown[] | undefined, b: unknown[] | undefined): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  try {
    return JSON.stringify(a) === JSON.stringify(b);
  } catch {
    return false;
  }
}

export default function HomeScreen() {
  const i18n = useI18n();
  const { t } = i18n;
  const pid = getProfileId() ?? 0;
  const locale = profileLocale();
  // Bumped after playback: Continue Watching and recommendations load again
  const [reload, setReload] = useState(0);
  const hq = useMemo(() => homeQuery(locale), [locale]);
  const pq = useMemo(() => personalQueries(pid, locale), [pid, locale]);
  const [homeRetry, setHomeRetry] = useState(0);
  const home = useQuery<HomeData>(hq.key, hq.load, hq.ttl, homeRetry, { initial: () => readHomeSnapshot(locale), keep: hq.keep });
  // A part the server had not ready (or that failed) is asked again a few times while Home is open
  const incomplete = !!home.data && !home.data.complete;
  useEffect(() => {
    if (!incomplete || homeRetry >= HOME_RETRIES) return;
    const id = window.setTimeout(() => setHomeRetry(n => n + 1), HOME_RETRY_MS * (homeRetry + 1));
    return () => window.clearTimeout(id);
  }, [incomplete, homeRetry]);
  const cw = useQuery(pq.cw.key, pq.cw.load, pq.cw.ttl, reload, { initial: pq.cw.initial });
  const recs = useQuery(pq.recs.key, pq.recs.load, pq.recs.ttl, reload, { initial: pq.recs.initial });
  const favs = useQuery(pq.favs.key, pq.favs.load, pq.favs.ttl, 0, { initial: pq.favs.initial });

  const f = home.data ? home.data.feeds : null;
  const trending = useMemo(() => (f ? interleave(f.trendingMovies, f.trendingTv) : undefined), [f]);

  // The billboard title stays the same when coming back to this screen (and from the snapshot to the fresh answer)
  const entry = useEntry();
  const featured = useMemo(() => {
    if (!f) return null;
    const pick = pickFeatured([...f.trendingMovies, ...f.trendingTv], (entry.state.featured as string) || null);
    if (pick) entry.state.featured = pick.key;
    return pick;
  }, [f, entry]);

  // Rows that did not change keep their object, so the memoized rows skip rendering
  const prevRows = useRef(new Map<string, RowDef>());
  const rows = useMemo<RowDef[]>(() => {
    const h = home.data;
    // Loading: placeholders; failed or missing: the row is hidden
    const part = (items: Title[] | null | undefined) => (h ? items || [] : home.failed ? [] : undefined);
    const recRows = recs.data || [];
    const serverTrending = recRows.some(r => r.id === 'trending' || r.reason === 'popular');
    const out: RowDef[] = [
      { id: 'cw', title: t('home.continueWatching', { name: getProfile()?.name || '' }), variant: 'continue', items: cw.failed ? [] : cw.data },
    ];
    if (!recs.data && !recs.failed) out.push({ id: 'recs-loading', title: '', variant: 'backdrop', items: undefined });
    recRows.forEach(r => out.push({ id: `rec-${r.id}`, title: recommendationTitle(i18n, r), variant: 'backdrop', items: r.items }));
    if (!serverTrending) out.push({ id: 'trending', title: t('home.trendingNow'), variant: 'backdrop', items: part(trending) });
    out.push(
      { id: 'top10-tr-movie', title: t('home.top10TrMovies'), variant: 'rank', items: part(h?.top10Movie) },
      { id: 'favorites', title: t('favorites.title'), variant: 'backdrop', items: favs.failed ? [] : favs.data },
      { id: 'popular-movies', title: t('home.popularMovies'), variant: 'backdrop', items: part(h?.popular) },
      { id: 'top10-tr-tv', title: t('home.top10TrTv'), variant: 'rank', items: part(h?.top10Tv) },
      { id: 'popular-tv', title: t('home.popularTv'), variant: 'backdrop', items: part(f?.popularTv) },
      { id: 'coming-soon', title: t('home.comingSoon'), variant: 'backdrop', items: part(f?.upcomingMovies) },
      { id: 'new-episodes', title: t('home.newEpisodes'), variant: 'backdrop', items: part(f?.onTheAirTv) },
    );
    GENRE_ROWS.forEach(g => out.push({ id: `genre-${g.id}`, title: t(g.title), variant: 'backdrop', items: part(h?.genres[g.id]) }));

    const prev = prevRows.current;
    const next = new Map<string, RowDef>();
    const stable = out.map(r => {
      const p = prev.get(r.id);
      const keep = p && p.title === r.title && p.variant === r.variant && sameItems(p.items, r.items) ? p : r;
      next.set(r.id, keep);
      return keep;
    });
    prevRows.current = next;
    return stable;
  }, [home, f, trending, cw, recs, favs, t, i18n]);

  return (
    <RowsScreen
      tab="home"
      featured={featured}
      rows={rows}
      ready={!!home.data || home.failed}
      surprise
      onPlayed={() => {
        invalidate('|cw');
        invalidate('|recs');
        setReload(n => n + 1);
      }}
    />
  );
}

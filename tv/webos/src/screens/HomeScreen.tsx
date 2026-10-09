import { useMemo, useState } from 'react';
import RowsScreen from '../components/RowsScreen';
import type { RowDef } from '../components/Row';
import { invalidate, scoped } from '../lib/http';
import { useI18n, type I18n, type MessageKey } from '../lib/i18n';
import { getProfile, getProfileId } from '../lib/profile';
import { progressList } from '../lib/stream';
import { useQuery } from '../lib/useQuery';
import { useEntry } from '../nav/router';
import {
  discoverMovies, favorites, homeFeedsCached, interleave, popularMovies, recommendations, top10,
  type HomeFeeds, type RecommendationRow, type Title,
} from '../lib/catalog';

// Keep in sync with GENRE_ROWS in web/src/features/home/HomeScreen.tsx (the server warms their images)
const GENRE_ROWS: { title: MessageKey; params: Record<string, string> }[] = [
  { title: 'home.genreActionAdventure', params: { with_genres: '28|12', 'vote_count.gte': '300' } },
  { title: 'home.genreComedies', params: { with_genres: '35', 'vote_count.gte': '300' } },
  { title: 'home.genreSciFiFantasy', params: { with_genres: '878|14', 'vote_count.gte': '300' } },
  { title: 'home.genreHorror', params: { with_genres: '27', 'vote_count.gte': '200' } },
  { title: 'home.genreAcclaimedMovies', params: { sort_by: 'vote_average.desc', 'vote_count.gte': '5000' } },
];

const MIN = 60 * 1000;

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

export default function HomeScreen() {
  const i18n = useI18n();
  const { t } = i18n;
  const pid = getProfileId() ?? 0;
  // Bumped after playback: Continue Watching and recommendations load again
  const [reload, setReload] = useState(0);
  const feeds = useQuery<HomeFeeds>(scoped('feeds'), homeFeedsCached);
  const cw = useQuery(scoped('cw'), () => progressList(pid), 15 * 1000, reload);
  const recs = useQuery(scoped('recs'), () => recommendations(pid), 5 * MIN, reload);
  const favs = useQuery(scoped('favs'), () => favorites(pid), MIN);
  const trMovies = useQuery(scoped('top10-tr-movie'), () => top10('TR', 'movie'));
  const trTv = useQuery(scoped('top10-tr-tv'), () => top10('TR', 'tv'));
  const popular = useQuery(scoped('popular-movies'), popularMovies);
  const genres = useQuery(scoped('home-genres'), () => Promise.all(GENRE_ROWS.map(g => discoverMovies(g.params).catch(() => []))));

  // The billboard title stays the same when coming back to this screen
  const entry = useEntry();
  const featured = useMemo(() => {
    if (!feeds.data) return null;
    const f = pickFeatured([...feeds.data.trendingMovies, ...feeds.data.trendingTv], (entry.state.featured as string) || null);
    if (f) entry.state.featured = f.key;
    return f;
  }, [feeds.data, entry]);

  const rows = useMemo<RowDef[]>(() => {
    const f = feeds.data;
    const failed = (q: { failed: boolean }) => q.failed;
    const list = (items: Title[] | undefined, q: { failed: boolean }) => (failed(q) ? [] : items);
    const recRows = recs.data || [];
    const serverTrending = recRows.some(r => r.id === 'trending' || r.reason === 'popular');
    const out: RowDef[] = [
      { id: 'cw', title: t('home.continueWatching', { name: getProfile()?.name || '' }), variant: 'continue', items: cw.failed ? [] : cw.data },
    ];
    if (!recs.data && !recs.failed) out.push({ id: 'recs-loading', title: '', variant: 'backdrop', items: undefined });
    recRows.forEach(r => out.push({ id: `rec-${r.id}`, title: recommendationTitle(i18n, r), variant: 'backdrop', items: r.items }));
    if (!serverTrending) out.push({ id: 'trending', title: t('home.trendingNow'), variant: 'backdrop', items: f ? interleave(f.trendingMovies, f.trendingTv) : list(undefined, feeds) });
    out.push(
      { id: 'top10-tr-movie', title: t('home.top10TrMovies'), variant: 'rank', items: list(trMovies.data, trMovies) },
      { id: 'favorites', title: t('favorites.title'), variant: 'backdrop', items: list(favs.data, favs) },
      { id: 'popular-movies', title: t('home.popularMovies'), variant: 'backdrop', items: list(popular.data, popular) },
      { id: 'top10-tr-tv', title: t('home.top10TrTv'), variant: 'rank', items: list(trTv.data, trTv) },
      { id: 'popular-tv', title: t('home.popularTv'), variant: 'backdrop', items: f ? f.popularTv : list(undefined, feeds) },
      { id: 'coming-soon', title: t('home.comingSoon'), variant: 'backdrop', items: f ? f.upcomingMovies : list(undefined, feeds) },
      { id: 'new-episodes', title: t('home.newEpisodes'), variant: 'backdrop', items: f ? f.onTheAirTv : list(undefined, feeds) },
    );
    GENRE_ROWS.forEach((g, i) => out.push({ id: `genre-${i}`, title: t(g.title), variant: 'backdrop', items: genres.data ? genres.data[i] : list(undefined, genres) }));
    return out;
  }, [feeds, cw, recs, favs, trMovies, trTv, popular, genres, t, i18n]);

  return (
    <RowsScreen
      tab="home"
      featured={featured}
      rows={rows}
      ready={!!feeds.data || feeds.failed}
      surprise
      onPlayed={() => {
        invalidate('|cw');
        invalidate('|recs');
        setReload(n => n + 1);
      }}
    />
  );
}

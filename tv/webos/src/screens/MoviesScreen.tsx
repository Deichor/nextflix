import { useMemo } from 'react';
import RowsScreen from '../components/RowsScreen';
import type { RowDef } from '../components/Row';
import { langScoped } from '../lib/http';
import { useT } from '../lib/i18n';
import { useQuery } from '../lib/useQuery';
import { useEntry } from '../nav/router';
import { discoverMovies, homeFeedsCached, popularMovies, unique, type Title } from '../lib/catalog';
import { pickFeatured } from './HomeScreen';

async function loadMovieRows() {
  const year = new Date().getFullYear();
  const [popular, acclaimed, recent, action, comedy, scifi, horror] = await Promise.all([
    popularMovies().catch(() => [] as Title[]),
    discoverMovies({ sort_by: 'vote_average.desc', 'vote_count.gte': '3000' }).catch(() => [] as Title[]),
    discoverMovies({ 'primary_release_date.gte': `${year - 1}-01-01`, 'vote_count.gte': '100' }).catch(() => [] as Title[]),
    discoverMovies({ with_genres: '28|12', 'vote_count.gte': '300' }).catch(() => [] as Title[]),
    discoverMovies({ with_genres: '35', 'vote_count.gte': '300' }).catch(() => [] as Title[]),
    discoverMovies({ with_genres: '878|14', 'vote_count.gte': '300' }).catch(() => [] as Title[]),
    discoverMovies({ with_genres: '27', 'vote_count.gte': '200' }).catch(() => [] as Title[]),
  ]);
  return { popular, acclaimed, recent, action, comedy, scifi, horror };
}

export default function MoviesScreen() {
  const t = useT();
  const entry = useEntry();
  const feeds = useQuery(langScoped('feeds'), homeFeedsCached);
  const more = useQuery(langScoped('movie-rows'), loadMovieRows);

  const featured = useMemo(() => {
    if (!feeds.data) return null;
    const f = pickFeatured(feeds.data.trendingMovies, (entry.state.featured as string) || null);
    if (f) entry.state.featured = f.key;
    return f;
  }, [feeds.data, entry]);

  const rows = useMemo<RowDef[]>(() => {
    const f = feeds.data;
    const m = more.data;
    const pending = (q: { failed: boolean }) => (q.failed ? [] : undefined);
    return [
      { id: 'top10', title: t('home.top10Movies'), variant: 'rank', items: f ? f.trendingMovies.slice(0, 10).map((x, i) => ({ ...x, rank: i + 1 })) : pending(feeds) },
      { id: 'trending', title: t('home.trendingNow'), variant: 'backdrop', items: f ? unique(f.trendingWeekMovies, f.trendingMovies) : pending(feeds) },
      { id: 'popular', title: t('home.popularOnNextflix'), variant: 'backdrop', items: m ? m.popular : pending(more) },
      { id: 'new', title: t('home.newReleases'), variant: 'backdrop', items: m ? m.recent : pending(more) },
      { id: 'theaters', title: t('home.nowPlaying'), variant: 'backdrop', items: f ? f.nowPlaying : pending(feeds) },
      { id: 'acclaimed', title: t('home.genreAcclaimedMovies'), variant: 'backdrop', items: m ? m.acclaimed : pending(more) },
      { id: 'action', title: t('home.genreActionAdventure'), variant: 'backdrop', items: m ? m.action : pending(more) },
      { id: 'comedy', title: t('home.genreComedies'), variant: 'backdrop', items: m ? m.comedy : pending(more) },
      { id: 'coming', title: t('home.comingSoon'), variant: 'backdrop', items: f ? f.upcomingMovies : pending(feeds) },
      { id: 'scifi', title: t('home.genreSciFiFantasy'), variant: 'backdrop', items: m ? m.scifi : pending(more) },
      { id: 'horror', title: t('home.genreHorror'), variant: 'backdrop', items: m ? m.horror : pending(more) },
    ];
  }, [feeds, more, t]);

  return <RowsScreen tab="movies" featured={featured} kicker={t('catalog.kickerFilm')} rows={rows} ready={!!feeds.data || feeds.failed} />;
}

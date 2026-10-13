import { useMemo } from 'react';
import RowsScreen from '../components/RowsScreen';
import type { RowDef } from '../components/Row';
import { langScoped } from '../lib/http';
import { useT } from '../lib/i18n';
import { useQuery } from '../lib/useQuery';
import { useEntry } from '../nav/router';
import { homeFeedsCached, providerTv, unique, type Title } from '../lib/catalog';
import { pickFeatured } from './HomeScreen';

async function loadProviderRows() {
  const [netflix, disney, max, acclaimed] = await Promise.all([
    providerTv(8).catch(() => [] as Title[]),
    providerTv(337).catch(() => [] as Title[]),
    providerTv(1899).catch(() => [] as Title[]),
    providerTv(8, 'vote_average.desc').catch(() => [] as Title[]),
  ]);
  return { netflix, disney, max, acclaimed };
}

export default function TvShowsScreen() {
  const t = useT();
  const entry = useEntry();
  const feeds = useQuery(langScoped('feeds'), homeFeedsCached);
  const providers = useQuery(langScoped('tv-providers'), loadProviderRows);

  const featured = useMemo(() => {
    if (!feeds.data) return null;
    const f = pickFeatured(feeds.data.trendingTv, (entry.state.featured as string) || null);
    if (f) entry.state.featured = f.key;
    return f;
  }, [feeds.data, entry]);

  const rows = useMemo<RowDef[]>(() => {
    const f = feeds.data;
    const p = providers.data;
    const pending = (q: { failed: boolean }) => (q.failed ? [] : undefined);
    return [
      { id: 'top10', title: t('home.top10Tv'), variant: 'rank', items: f ? f.trendingTv.slice(0, 10).map((x, i) => ({ ...x, rank: i + 1 })) : pending(feeds) },
      { id: 'trending', title: t('home.trendingNow'), variant: 'backdrop', items: f ? unique(f.popularTv, f.trendingTv) : pending(feeds) },
      { id: 'netflix', title: t('catalog.providerNetflix'), variant: 'backdrop', items: p ? p.netflix : pending(providers) },
      { id: 'new', title: t('home.newEpisodes'), variant: 'backdrop', items: f ? f.airingToday : pending(feeds) },
      { id: 'airing', title: t('home.currentlyAiring'), variant: 'backdrop', items: f ? f.onTheAirTv : pending(feeds) },
      { id: 'disney', title: t('catalog.providerDisney'), variant: 'backdrop', items: p ? p.disney : pending(providers) },
      { id: 'max', title: t('catalog.providerMax'), variant: 'backdrop', items: p ? p.max : pending(providers) },
      { id: 'acclaimed', title: t('catalog.acclaimedTv'), variant: 'backdrop', items: p ? p.acclaimed : pending(providers) },
    ];
  }, [feeds, providers, t]);

  return <RowsScreen tab="tv" featured={featured} kicker={t('catalog.kickerSeries')} rows={rows} ready={!!feeds.data || feeds.failed} />;
}

import { useEffect, useState } from 'react';
import { useI18n } from '../lib/i18n';
import { pauseInfo, type PauseInfo } from '../lib/catalog';
import { episodeLabel, type StreamMeta } from '../lib/stream';

// Netflix-style "You're watching" screen over the picture after a while paused
export default function PauseScreen({ meta, shown }: { meta: StreamMeta; shown: boolean }) {
  const i18n = useI18n();
  const { t, locale } = i18n;
  const [info, setInfo] = useState<PauseInfo | null>(null);
  const key = `${meta.media_type}|${meta.tmdb_id}|${meta.season || 0}|${meta.episode || 0}`;

  useEffect(() => {
    let cancelled = false;
    setInfo(null);
    pauseInfo(meta).then(i => { if (!cancelled) setInfo(i); }).catch(() => {});
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, locale]);

  const isTv = meta.media_type === 'tv';
  const episodeTitle = meta.episode_title || info?.episodeTitle || '';
  const facts = isTv ? [] : [info?.year, info?.runtime ? i18n.duration(info.runtime) : ''].filter(Boolean);

  return (
    <div className={`pause-screen ${shown ? 'is-visible' : ''}`}>
      <div className="pause-screen-body">
        <p className="pause-screen-kicker">{t('player.youreWatching')}</p>
        <h1 className="pause-screen-title">{meta.title}</h1>
        {isTv && !!meta.season && !!meta.episode && (
          <p className="pause-screen-episode">
            {episodeLabel(locale, meta.season, meta.episode)}
            {!!episodeTitle && <span className="pause-screen-episode-name"> “{episodeTitle}”</span>}
          </p>
        )}
        {facts.length > 0 && <p className="pause-screen-facts">{facts.join(' · ')}</p>}
        {!!info?.overview && <p className="pause-screen-overview">{info.overview}</p>}
      </div>
      <p className="pause-screen-paused">{t('player.paused')}</p>
    </div>
  );
}

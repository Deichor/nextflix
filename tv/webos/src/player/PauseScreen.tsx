import { useEffect, useRef, useState } from 'react';
import { useI18n } from '../lib/i18n';
import { pauseInfo, type PauseInfo } from '../lib/catalog';
import { episodeLabel, type StreamMeta } from '../lib/stream';

// Netflix-style "You're watching" screen over the picture after a while paused
export default function PauseScreen({ meta, shown }: { meta: StreamMeta; shown: boolean }) {
  const i18n = useI18n();
  const { t, locale } = i18n;
  const [info, setInfo] = useState<PauseInfo | null>(null);
  const key = `${meta.media_type}|${meta.tmdb_id}|${meta.season || 0}|${meta.episode || 0}|${locale}`;
  // Key whose info is loaded or loading ('' = not yet)
  const loadedRef = useRef('');

  useEffect(() => {
    setInfo(null);
    loadedRef.current = '';
  }, [key]);

  // Loaded only once the pause screen first shows; a failed load is tried again on the next pause
  useEffect(() => {
    if (!shown || loadedRef.current === key) return;
    loadedRef.current = key;
    pauseInfo(meta).then(r => {
      if (loadedRef.current !== key) return;
      if (r.info) setInfo(r.info);
      if (!r.complete) loadedRef.current = '';
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, shown]);

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

'use client';

import { useEffect, useState } from 'react';
import { useFormat, useLocale, useT } from '@/shared/i18n';
import { episodeLabel, getPauseInfo, type PauseInfo, type StreamMeta } from '@/features/watch/api';

// Netflix-style "You're watching" screen over the picture after a while paused.
// It takes no input: clicks and taps reach the video below.
export default function PauseScreen({ meta, title, shown }: { meta?: StreamMeta; title: string; shown: boolean }) {
  const t = useT();
  const f = useFormat();
  const locale = useLocale();
  const key = meta ? `${meta.media_type}|${meta.tmdb_id}|${meta.season || 0}|${meta.episode || 0}` : '';
  // Tagged with what it was loaded for: a stale answer never shows
  const [loaded, setLoaded] = useState<{ key: string; info: PauseInfo | null } | null>(null);
  const info = loaded && loaded.key === `${key}|${locale}` ? loaded.info : null;

  useEffect(() => {
    if (!meta) return;
    let cancelled = false;
    getPauseInfo(meta).then(i => { if (!cancelled) setLoaded({ key: `${key}|${locale}`, info: i }); });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, locale]);

  const isTv = meta?.media_type === 'tv';
  const episodeTitle = meta?.episode_title || info?.episodeTitle || '';
  const facts = isTv ? [] : [info?.year, info?.runtime ? f.duration(info.runtime) : ''].filter(Boolean);

  return (
    <div
      aria-hidden={!shown}
      className={`pointer-events-none absolute inset-0 z-[7] bg-gradient-to-r from-black/90 via-black/60 to-black/20 transition-opacity duration-700 ${shown ? 'opacity-100' : 'opacity-0'}`}
    >
      <div className="absolute top-1/2 left-[max(6%,env(safe-area-inset-left))] w-[46%] -translate-y-1/2 portrait:w-[86%]">
        <p className="text-[clamp(0.8rem,1.4vw,1.4rem)] text-[#b3b3b3]">{t('player.youreWatching')}</p>
        <h2 className="mt-[0.3em] line-clamp-2 text-[clamp(1.5rem,4vw,4.25rem)] font-extrabold leading-tight text-white">{title}</h2>
        {isTv && !!meta?.season && !!meta.episode && (
          <p className="mt-[0.8em] text-[clamp(0.95rem,1.7vw,1.75rem)] font-semibold text-white">
            {episodeLabel(meta.season, meta.episode, locale)}
            {!!episodeTitle && <span className="font-normal"> “{episodeTitle}”</span>}
          </p>
        )}
        {facts.length > 0 && <p className="mt-[0.8em] text-[clamp(0.85rem,1.4vw,1.4rem)] text-[#b3b3b3]">{facts.join(' · ')}</p>}
        {!!info?.overview && (
          <p className="mt-[0.9em] line-clamp-4 text-[clamp(0.85rem,1.4vw,1.4rem)] leading-relaxed text-[#d2d2d2] [@media(max-height:500px)]:line-clamp-3">
            {info.overview}
          </p>
        )}
      </div>
      <p className="absolute right-[max(6%,env(safe-area-inset-right))] bottom-[max(8%,env(safe-area-inset-bottom))] text-[clamp(0.9rem,1.6vw,1.6rem)] text-[#b3b3b3]">
        {t('player.paused')}
      </p>
    </div>
  );
}

"use client";

import React, { useEffect, useState } from 'react';
import { Info } from 'lucide-react';
import WatchButton, { formatRemaining, useAvailability } from '@/features/watch/components/WatchButton';
import { episodeLabel, progressPercent } from '@/features/watch/api';
import { useTitleActions } from '@/features/home/components/TitleActions';
import { TitleItem, fetchMovieLogo, img, matchPercent, yearOf } from '@/features/home/feeds';
import { localGenres } from '@/features/catalog/titles';
import RatingButtons from '@/features/favorites/components/RatingButtons';
import { useLocale, useT } from '@/shared/i18n';
import TmdbImage from '@/shared/ui/TmdbImage';
import { BILLBOARD_SIZES } from '@/shared/lib/image';

interface BillboardProps {
  item: TitleItem | null;
  loading?: boolean;
}

// Full-bleed featured title at the top of the home page
export default function Billboard({ item, loading = false }: BillboardProps) {
  const t = useT();
  const locale = useLocale();
  const { play, openInfo, progressFor } = useTitleActions();
  const [logo, setLogo] = useState('');
  const [logoFailed, setLogoFailed] = useState(false);

  useEffect(() => {
    setLogo('');
    setLogoFailed(false);
    if (!item || item.media_type !== 'movie') return;
    const ctrl = new AbortController();
    fetchMovieLogo(item.id, ctrl.signal).then(path => { if (!ctrl.signal.aborted) setLogo(path); });
    return () => ctrl.abort();
  }, [item]);

  // Is what Play starts out yet? (the movie, or the episode it resumes)
  const resumeAt = item ? progressFor(item) : undefined;
  const availability = useAvailability(
    !item ? null
      : item.media_type === 'movie' ? { media_type: 'movie', tmdb_id: item.id }
        : { media_type: 'tv', tmdb_id: item.id, season: resumeAt?.season || 1, episode: resumeAt?.episode || 1 },
  );

  const shell = 'relative w-full h-[125vw] sm:h-[56.25vw] sm:portrait:h-[75vw] max-h-[92vh] min-h-[420px] overflow-hidden bg-[#141414]';

  if (!item) {
    return (
      <div className={shell}>
        {loading && (
          <>
            <div className="nf-img" />
            <div className="absolute inset-x-0 bottom-0 h-[38%] bg-gradient-to-t from-[#141414] via-[#141414]/60 to-transparent" />
          </>
        )}
      </div>
    );
  }

  const saved = progressFor(item);
  const pct = saved ? progressPercent(saved) : 0;
  const year = yearOf(item);
  const match = matchPercent(item);
  const genres = localGenres(t, item.genre_ids);
  const backdrop = item.backdrop_path || item.poster_path;

  return (
    <section className={shell} aria-label={item.title}>
      {!!backdrop && (
        <TmdbImage
          key={backdrop}
          src={backdrop}
          kind="billboard"
          sizes={BILLBOARD_SIZES}
          priority="high"
          className="object-top"
        />
      )}

      {/* Readability: left fade, top fade under the nav, bottom vignette into the page */}
      <div className="absolute inset-0 bg-gradient-to-r from-black/80 via-black/30 to-transparent" />
      <div className="absolute inset-x-0 top-0 h-32 bg-gradient-to-b from-black/60 to-transparent" />
      <div className="absolute inset-x-0 bottom-0 h-[38%] bg-gradient-to-t from-[#141414] via-[#141414]/60 to-transparent" />

      <div className="absolute left-4 md:left-12 right-4 bottom-[14%] sm:bottom-[30%] sm:portrait:bottom-[22%] max-w-[92%] sm:max-w-[48%] sm:portrait:max-w-[64%] lg:max-w-[38%]">
        {!!logo && !logoFailed ? (
          <img
            src={img('w500', logo)}
            alt={item.title}
            onError={() => setLogoFailed(true)}
            className="max-h-[90px] sm:max-h-[120px] md:max-h-[160px] max-w-full w-auto object-contain object-left mb-3 md:mb-5 drop-shadow-[0_4px_12px_rgba(0,0,0,0.6)]"
          />
        ) : (
          <h1 className="text-white font-black tracking-tight leading-[1.1] pb-[0.08em] text-3xl sm:text-5xl lg:text-6xl mb-3 md:mb-5 drop-shadow-[0_2px_8px_rgba(0,0,0,0.7)] line-clamp-3">
            {item.title}
          </h1>
        )}

        <div className="flex items-center gap-2 text-xs sm:text-sm font-semibold mb-2 md:mb-3">
          {match > 0 && <span className="text-[#46d369]">{t('detail.match', { n: match })}</span>}
          {!!year && <span className="text-white/90">{year}</span>}
          <span className="text-white/90">{t(item.media_type === 'tv' ? 'detail.series' : 'common.movie')}</span>
          {genres.length > 0 && <span className="hidden sm:inline text-white/70">· {genres.join(' • ')}</span>}
        </div>

        {!!item.overview && (
          <p className="text-white text-sm md:text-base lg:text-lg leading-snug line-clamp-2 sm:line-clamp-3 drop-shadow-[0_2px_4px_rgba(0,0,0,0.8)] mb-4 md:mb-6">
            {item.overview}
          </p>
        )}

        <div className="flex flex-wrap items-start gap-2 md:gap-3">
          <WatchButton
            className="flex-1 sm:flex-none [&>button]:min-h-11 md:[&>button]:min-h-0"
            onClick={() => play(item)}
            availability={availability}
            resume={!!saved}
            detail={saved && item.media_type === 'tv' ? episodeLabel(saved.season, saved.episode, locale) : undefined}
            progress={pct}
            remainingText={saved && saved.duration > 0 ? formatRemaining(saved.position, saved.duration, locale) : undefined}
          />
          <button
            onClick={() => openInfo(item)}
            className="flex flex-1 sm:flex-none min-h-11 md:min-h-0 items-center justify-center gap-2 whitespace-nowrap px-4 sm:px-5 py-2 md:px-7 md:py-2.5 rounded bg-[#6d6d6e]/70 hover:bg-[#6d6d6e]/50 text-white font-bold text-sm md:text-lg transition-colors"
          >
            <Info className="w-5 h-5 md:w-6 md:h-6" />
            <span className="sm:hidden">{t('detail.moreInfoShort')}</span>
            <span className="hidden sm:inline">{t('detail.moreInfo')}</span>
          </button>
          <div className="flex items-center gap-2">
            <RatingButtons
              size="md"
              item={{ id: item.id, mediaType: item.media_type, title: item.title, posterPath: item.poster_path || undefined, backdropPath: item.backdrop_path || undefined }}
            />
          </div>
        </div>
      </div>
    </section>
  );
}

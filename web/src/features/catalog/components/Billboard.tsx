"use client";

import React, { useEffect, useState } from 'react';
import { CalendarClock, Info, Play } from 'lucide-react';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { fetchLogo, matchPercent, NfTitle } from '@/features/catalog/titles';
import TmdbImage from '@/shared/ui/TmdbImage';
import { BILLBOARD_SIZES } from '@/shared/lib/image';
import { useT } from '@/shared/i18n';
import RatingButtons from '@/features/favorites/components/RatingButtons';
import { useAvailability, useUnavailableLabel } from '@/features/watch/components/WatchButton';

interface BillboardProps {
  item: NfTitle | null;
  loading?: boolean;
  onPlay?: (item: NfTitle) => void;
  /** Small label above the title, e.g. "SERIES" or "FILM" */
  kicker?: string;
}

// Full-bleed billboard: backdrop, logo or big title, short overview, Play + More Info, vignette into #141414
export default function Billboard({ item, loading, onPlay, kicker }: BillboardProps) {
  const t = useT();
  const navigate = useNavigate();
  const [logo, setLogo] = useState<string | null>(null);
  const [logoFailed, setLogoFailed] = useState(false);

  useEffect(() => {
    setLogo(null);
    setLogoFailed(false);
    if (!item) return;
    let cancelled = false;
    fetchLogo(item.id, item.mediaType).then(l => { if (!cancelled) setLogo(l); });
    return () => { cancelled = true; };
  }, [item]);

  const backdrop = item?.backdropPath || item?.backdrop;
  const match = matchPercent(item?.rating);
  // Play would start the movie or S1:E1
  const availability = useAvailability(item ? { media_type: item.mediaType, tmdb_id: item.id, season: item.mediaType === 'tv' ? 1 : undefined, episode: item.mediaType === 'tv' ? 1 : undefined } : null);
  const unavailable = useUnavailableLabel(availability);

  return (
    <section className="relative h-[130vw] max-h-[88vh] min-h-[440px] w-full overflow-hidden bg-[#141414] sm:h-[56.25vw] sm:portrait:h-[75vw]">
      {loading && !item && <div className="nf-img" />}
      {!!backdrop && (
        <TmdbImage key={backdrop} src={backdrop} kind="billboard" sizes={BILLBOARD_SIZES} priority="high" className="object-top" />
      )}
      {/* Left and bottom vignettes, like the Netflix billboard */}
      <div className="absolute inset-0 bg-gradient-to-r from-black/75 via-black/30 to-transparent" />
      <div className="absolute inset-x-0 bottom-0 h-[30%] bg-gradient-to-b from-transparent to-[#141414]" />
      <div className="absolute inset-x-0 top-0 h-32 bg-gradient-to-b from-black/60 to-transparent" />

      {!!item && (
        <div className="absolute bottom-[16%] left-4 right-4 z-10 sm:bottom-[30%] md:left-12 md:right-auto md:w-[42%] md:portrait:w-[60%]">
          {!!kicker && (
            <p className="mb-2 flex items-center gap-2 text-xs font-bold tracking-[0.3em] text-[#e5e5e5] md:text-sm">
              <span className="text-xl font-black tracking-normal text-[#E50914] md:text-2xl">N</span>
              {kicker}
            </p>
          )}
          {logo && !logoFailed ? (
            <img
              src={logo}
              alt={item.title}
              onError={() => setLogoFailed(true)}
              className="mb-4 max-h-28 w-auto max-w-[80%] object-contain object-left drop-shadow-[0_4px_20px_rgba(0,0,0,0.6)] md:mb-6 md:max-h-40"
            />
          ) : (
            <h1 className="mb-3 text-3xl font-black leading-tight text-white drop-shadow-lg sm:text-5xl md:mb-5 lg:text-6xl">
              {item.title}
            </h1>
          )}
          <div className="mb-2 flex items-center gap-2 text-sm font-semibold">
            {match !== null && <span className="text-[#46d369]">{t('detail.match', { n: match })}</span>}
            {!!item.year && <span className="text-[#e5e5e5]">{item.year}</span>}
          </div>
          {!!item.overview && (
            <p className="mb-4 line-clamp-2 text-sm leading-snug text-white drop-shadow-md sm:mb-5 sm:line-clamp-3 md:text-lg md:leading-normal">
              {item.overview}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-2 md:gap-3">
            {unavailable ? (
              <button
                disabled
                aria-disabled
                className="flex min-h-11 flex-1 cursor-default items-center justify-center gap-2 rounded bg-white/25 px-5 py-2 text-sm font-bold text-white/85 sm:flex-none md:min-h-0 md:px-7 md:py-2.5 md:text-lg"
              >
                <CalendarClock className="h-5 w-5 shrink-0 md:h-6 md:w-6" />
                <span className="text-left leading-tight">{unavailable}</span>
              </button>
            ) : (
              <button
                onClick={() => (onPlay ? onPlay(item) : navigate.push(item.playHref || item.href))}
                className="flex min-h-11 flex-1 items-center justify-center gap-2 rounded bg-white px-5 py-2 text-sm font-bold text-black transition-colors hover:bg-white/75 sm:flex-none md:min-h-0 md:px-8 md:py-2.5 md:text-lg"
              >
                <Play className="h-5 w-5 fill-current md:h-7 md:w-7" />
                {t('player.play')}
              </button>
            )}
            <button
              onClick={() => navigate.push(item.href)}
              className="flex min-h-11 flex-1 items-center justify-center gap-2 whitespace-nowrap rounded bg-[rgba(109,109,110,0.7)] px-5 py-2 text-sm font-bold text-white transition-colors hover:bg-[rgba(109,109,110,0.4)] sm:flex-none md:min-h-0 md:px-7 md:py-2.5 md:text-lg"
            >
              <Info className="h-5 w-5 md:h-7 md:w-7" />
              <span className="sm:hidden">{t('detail.moreInfoShort')}</span>
              <span className="hidden sm:inline">{t('detail.moreInfo')}</span>
            </button>
            <RatingButtons
              size="md"
              item={{ id: item.id, mediaType: item.mediaType, title: item.title, posterPath: item.posterPath, backdropPath: item.backdropPath }}
            />
          </div>
        </div>
      )}
    </section>
  );
}

"use client";

import React, { memo, useState } from 'react';
import { useTitleActions } from '@/features/home/components/TitleActions';
import { HoverPreview, useHoverPreview } from '@/features/catalog/components/TitleCard';
import { localGenres } from '@/features/catalog/titles';
import { TitleItem, img, matchPercent, yearOf } from '@/features/home/feeds';
import { progressPercent } from '@/features/watch/api';
import { useProgressFor } from '@/features/watch/progressStore';
import { useFormat, useT } from '@/shared/i18n';
import TmdbImage from '@/shared/ui/TmdbImage';
import InstantChip from '@/shared/ui/InstantChip';

export type CardVariant = 'backdrop' | 'poster';

interface TitleCardProps {
  item: TitleItem;
  variant?: CardVariant;
  /** false for titles that are not released yet (Coming Soon) */
  playable?: boolean;
}

export const CARD_WIDTH: Record<CardVariant, string> = {
  backdrop: 'w-[37vw] sm:w-[30vw] md:w-[23vw] lg:w-[18vw] xl:w-[15.2vw]',
  poster: 'w-[30vw] sm:w-[22vw] md:w-[17vw] lg:w-[13vw] xl:w-[11.2vw]',
};

// CSS widths of CARD_WIDTH, for the srcset
export const CARD_SIZES: Record<CardVariant, string> = {
  backdrop: '(min-width: 1280px) 15.2vw, (min-width: 1024px) 18vw, (min-width: 768px) 23vw, (min-width: 640px) 30vw, 37vw',
  poster: '(min-width: 1280px) 11.2vw, (min-width: 1024px) 13vw, (min-width: 768px) 17vw, (min-width: 640px) 22vw, 30vw',
};

function TitleCard({ item, variant = 'backdrop', playable = true }: TitleCardProps) {
  const t = useT();
  const f = useFormat();
  const { play, openInfo } = useTitleActions();
  const [imgFailed, setImgFailed] = useState(false);

  const isPoster = variant === 'poster';
  const path = isPoster ? item.poster_path || item.backdrop_path : item.backdrop_path || item.poster_path;
  // What the card usually loads at 1x (placeholder for the hover preview) and the preview's large image
  const src = isPoster ? img('w342', path) : img('w300', path);
  const src2x = isPoster ? img('w500', path) : img('w780', path);
  const saved = useProgressFor(item.media_type, item.id);
  const pct = saved ? progressPercent(saved) : 0;
  const year = yearOf(item);

  const { anchorRef, anchorProps, preview } = useHoverPreview();

  const coming = !playable && item.date ? f.shortDate(item.date) : '';

  return (
    <>
      <div
        ref={anchorRef}
        {...anchorProps}
        role="button"
        tabIndex={0}
        aria-label={item.title}
        onClick={() => openInfo(item)}
        onKeyDown={e => { if (e.key === 'Enter') openInfo(item); }}
        className={`nf-card relative shrink-0 ${CARD_WIDTH[variant]} rounded`}
      >
        <div className={`relative ${isPoster ? 'aspect-[2/3]' : 'aspect-video'} rounded overflow-hidden bg-[#2f2f2f] shadow-md`}>
          {!!path && !imgFailed ? (
            <TmdbImage
              src={path}
              kind={isPoster ? 'poster' : 'backdrop'}
              sizes={CARD_SIZES[variant]}
              alt={item.title}
              placeholder="none"
              onError={() => setImgFailed(true)}
            />
          ) : (
            <div className="w-full h-full flex items-center justify-center p-3 text-center text-sm font-semibold text-[#b3b3b3]">
              {item.title}
            </div>
          )}

          {/* Title text (backdrops have no baked-in title) */}
          {!isPoster && !!src && !imgFailed && (
            <div className="absolute inset-x-0 bottom-0 px-2.5 pb-2 pt-8 bg-gradient-to-t from-black/85 via-black/40 to-transparent">
              <p className="text-white text-xs sm:text-sm font-bold leading-tight line-clamp-2 drop-shadow">{item.title}</p>
            </div>
          )}

          {playable && <InstantChip item={{ type: item.media_type, tmdb_id: item.id, title: item.title, year }} />}

          {pct > 0 && (
            <div className="absolute bottom-0 inset-x-0 h-[3px] bg-[#5a5a5a]/80">
              <div className="h-full bg-[#E50914]" style={{ width: `${pct}%` }} />
            </div>
          )}
        </div>
      </div>

      {!!preview && (
        <HoverPreview
          state={preview}
          variant={variant}
          info={{
            title: item.title,
            image: (isPoster ? src2x : img('w780', item.backdrop_path || item.poster_path)) || undefined,
            placeholder: !imgFailed ? src || undefined : undefined,
            match: playable ? matchPercent(item) : null,
            note: coming ? t('home.comingOn', { date: coming }) : undefined,
            year,
            isTv: item.media_type === 'tv',
            genres: localGenres(t, item.genre_ids),
            progress: pct,
            rateItem: {
              id: item.id,
              mediaType: item.media_type,
              title: item.title,
              posterPath: item.poster_path || undefined,
              backdropPath: item.backdrop_path || undefined,
            },
            playable,
            resume: !!saved,
            source: item.media_type === 'tv'
              ? { type: 'tv', title: item.title, tmdb_id: item.id, season: saved?.season || 1, episode: saved?.episode || 1 }
              : { type: 'movie', title: item.title, tmdb_id: item.id, year },
          }}
          onPlay={() => play(item)}
          onOpen={() => openInfo(item)}
        />
      )}
    </>
  );
}

export default memo(TitleCard);

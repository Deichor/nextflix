"use client";

import React, { memo, useState } from 'react';
import { Check, ChevronDown, Play, Plus } from 'lucide-react';
import { useTitleActions } from '@/features/home/components/TitleActions';
import { TitleItem, genreLabels, img, matchPercent, yearOf } from '@/features/home/feeds';
import { progressPercent } from '@/features/watch/api';

export type CardVariant = 'backdrop' | 'poster';

interface TitleCardProps {
  item: TitleItem;
  variant?: CardVariant;
  /** false for titles that are not released yet (Coming Soon) */
  playable?: boolean;
}

export const CARD_WIDTH: Record<CardVariant, string> = {
  backdrop: 'w-[42vw] sm:w-[30vw] md:w-[23vw] lg:w-[18vw] xl:w-[15.2vw]',
  poster: 'w-[30vw] sm:w-[22vw] md:w-[17vw] lg:w-[13vw] xl:w-[11.2vw]',
};

const releaseLabel = (date: string) => {
  const d = new Date(date);
  return isNaN(d.getTime()) ? '' : d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
};

function TitleCard({ item, variant = 'backdrop', playable = true }: TitleCardProps) {
  const { play, openInfo, progressFor, inMyList, toggleMyList } = useTitleActions();
  const [imgFailed, setImgFailed] = useState(false);

  const isPoster = variant === 'poster';
  const path = isPoster ? item.poster_path || item.backdrop_path : item.backdrop_path || item.poster_path;
  // Backdrop sizes: w300/w780; poster sizes: w342/w500
  const src = isPoster ? img('w342', path) : img('w300', path);
  const src2x = isPoster ? img('w500', path) : img('w780', path);
  const saved = progressFor(item);
  const pct = saved ? progressPercent(saved) : 0;
  const listed = inMyList(item);
  const year = yearOf(item);
  const match = matchPercent(item);
  const genres = genreLabels(item);
  const stop = (fn: () => void) => (e: React.MouseEvent) => { e.stopPropagation(); fn(); };

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={() => openInfo(item)}
      onKeyDown={e => { if (e.key === 'Enter') openInfo(item); }}
      className={`group/card relative shrink-0 ${CARD_WIDTH[variant]} transition-transform duration-300 ease-out hover:z-20 hover:scale-[1.08] focus-visible:outline focus-visible:outline-2 focus-visible:outline-white rounded`}
      title={item.title}
    >
      <div className={`relative ${isPoster ? 'aspect-[2/3]' : 'aspect-video'} rounded overflow-hidden bg-[#2f2f2f] shadow-md group-hover/card:shadow-[0_8px_24px_rgba(0,0,0,0.7)]`}>
        {!!src && !imgFailed ? (
          <img
            src={src}
            srcSet={`${src} 1x, ${src2x} 2x`}
            alt={item.title}
            loading="lazy"
            decoding="async"
            draggable={false}
            onError={() => setImgFailed(true)}
            className="w-full h-full object-cover"
          />
        ) : (
          <div className="w-full h-full flex items-center justify-center p-3 text-center text-sm font-semibold text-[#b3b3b3]">
            {item.title}
          </div>
        )}

        {/* Title text while idle (backdrops have no baked-in title) */}
        {!isPoster && !!src && !imgFailed && (
          <div className="absolute inset-x-0 bottom-0 px-2.5 pb-2 pt-8 bg-gradient-to-t from-black/85 via-black/40 to-transparent transition-opacity duration-200 group-hover/card:opacity-0">
            <p className="text-white text-xs sm:text-sm font-bold leading-tight line-clamp-2 drop-shadow">{item.title}</p>
          </div>
        )}

        {/* Info panel on hover */}
        <div className="absolute inset-x-0 bottom-0 px-2.5 pb-2 pt-10 bg-gradient-to-t from-[#141414] via-[#141414]/90 to-transparent opacity-0 group-hover/card:opacity-100 transition-opacity duration-200 pointer-events-none group-hover/card:pointer-events-auto">
          <div className="flex items-center gap-1.5 mb-1.5">
            {playable && (
              <button
                onClick={stop(() => play(item))}
                className="w-7 h-7 md:w-8 md:h-8 rounded-full bg-white hover:bg-white/80 flex items-center justify-center"
                aria-label={`Play ${item.title}`}
                title={saved ? 'Resume' : 'Play'}
              >
                <Play className="w-3.5 h-3.5 md:w-4 md:h-4 text-black fill-current translate-x-[1px]" />
              </button>
            )}
            <button
              onClick={stop(() => toggleMyList(item))}
              className="w-7 h-7 md:w-8 md:h-8 rounded-full border-2 border-white/60 hover:border-white bg-[#2a2a2a]/70 flex items-center justify-center"
              aria-label={listed ? 'Remove from My List' : 'Add to My List'}
              title={listed ? 'Remove from My List' : 'Add to My List'}
            >
              {listed ? <Check className="w-4 h-4 text-white" /> : <Plus className="w-4 h-4 text-white" />}
            </button>
            <button
              onClick={stop(() => openInfo(item))}
              className="ml-auto w-7 h-7 md:w-8 md:h-8 rounded-full border-2 border-white/60 hover:border-white bg-[#2a2a2a]/70 flex items-center justify-center"
              aria-label="More info"
              title="More info"
            >
              <ChevronDown className="w-4 h-4 text-white" />
            </button>
          </div>
          <p className="text-white text-xs font-bold truncate">{item.title}</p>
          <div className="flex items-center gap-1.5 text-[10px] md:text-[11px] font-semibold whitespace-nowrap overflow-hidden">
            {playable ? (
              match > 0 && <span className="text-[#46d369]">{match}% Match</span>
            ) : (
              !!releaseLabel(item.date) && <span className="text-[#46d369]">Coming {releaseLabel(item.date)}</span>
            )}
            {!!year && <span className="text-[#bcbcbc]">{year}</span>}
            <span className="border border-white/40 text-white/80 px-1 leading-tight rounded-sm text-[9px]">HD</span>
          </div>
          {genres.length > 0 && (
            <p className="text-[10px] md:text-[11px] text-white/80 truncate">{genres.join(' • ')}</p>
          )}
        </div>

        {pct > 0 && (
          <div className="absolute bottom-0 inset-x-0 h-[3px] bg-[#5a5a5a]/80">
            <div className="h-full bg-[#E50914]" style={{ width: `${pct}%` }} />
          </div>
        )}
      </div>
    </div>
  );
}

export default memo(TitleCard);

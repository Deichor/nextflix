"use client";

import React, { useState } from 'react';
import { Check, ChevronDown, Play, Plus } from 'lucide-react';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { matchPercent, NfTitle } from '@/features/catalog/titles';
import { useMyList } from '@/features/my-list/store';

export type CardVariant = 'poster' | 'backdrop';

interface TitleCardProps {
  item: NfTitle;
  variant?: CardVariant;
  /** Overrides the default Play action (TMDB titles open the detail page with ?play=1) */
  onPlay?: (item: NfTitle) => void;
  /** 0-100 red progress bar at the bottom of the image */
  progress?: number;
  className?: string;
}

// Netflix-style card: image, hover scale ~1.08 with a small info panel (Play, + My List, More Info)
export default function TitleCard({ item, variant = 'poster', onPlay, progress = 0, className = '' }: TitleCardProps) {
  const navigate = useNavigate();
  const { has, toggle } = useMyList();
  const [imgFailed, setImgFailed] = useState(false);
  const inList = has(item);
  const match = matchPercent(item.rating);
  const img = variant === 'poster' ? item.poster || item.backdrop : item.backdrop || item.poster;

  const open = () => navigate.push(item.href);
  const play = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (onPlay) onPlay(item);
    else navigate.push(item.playHref || item.href);
  };

  return (
    <div
      role="link"
      tabIndex={0}
      onClick={open}
      onKeyDown={e => { if (e.key === 'Enter') open(); }}
      className={`group/card relative cursor-pointer rounded-md bg-[#181818] shadow-black/60 transition-[transform,box-shadow] duration-300 ease-out hover:z-20 hover:scale-[1.08] hover:shadow-2xl focus-visible:z-20 focus-visible:scale-[1.08] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white ${className}`}
    >
      <div className={`relative overflow-hidden rounded-md ${variant === 'poster' ? 'aspect-[2/3]' : 'aspect-video'}`}>
        {img && !imgFailed ? (
          <img
            src={img}
            alt={item.title}
            loading="lazy"
            draggable={false}
            onError={() => setImgFailed(true)}
            className="h-full w-full object-cover"
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center bg-[#2f2f2f] p-3 text-center text-sm font-semibold text-[#b3b3b3]">
            {item.title}
          </div>
        )}

        {/* Backdrop cards carry their title on the image, like Netflix rows */}
        {variant === 'backdrop' && !!img && !imgFailed && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/80 to-transparent px-2 pb-1.5 pt-6 transition-opacity group-hover/card:opacity-0">
            <p className="line-clamp-1 text-xs font-bold text-white drop-shadow md:text-sm">{item.title}</p>
          </div>
        )}

        {progress > 0 && (
          <div className="absolute inset-x-2 bottom-1.5 h-[3px] bg-[#5a5a5a]/90">
            <div className="h-full bg-[#E50914]" style={{ width: `${Math.min(100, progress)}%` }} />
          </div>
        )}

        {/* Hover info panel */}
        <div className="pointer-events-none absolute inset-x-0 bottom-0 translate-y-2 bg-gradient-to-t from-[#141414] via-[#141414]/95 to-transparent px-2.5 pb-2.5 pt-10 opacity-0 transition-all duration-300 group-hover/card:pointer-events-auto group-hover/card:translate-y-0 group-hover/card:opacity-100 group-focus-visible/card:pointer-events-auto group-focus-visible/card:opacity-100">
          <div className="mb-2 flex items-center gap-1.5">
            <button
              onClick={play}
              title="Play"
              className="flex h-7 w-7 items-center justify-center rounded-full bg-white text-black transition-colors hover:bg-white/80 md:h-8 md:w-8"
            >
              <Play className="h-3.5 w-3.5 translate-x-px fill-current md:h-4 md:w-4" />
            </button>
            <button
              onClick={e => { e.stopPropagation(); toggle(item); }}
              title={inList ? 'Remove from My List' : 'Add to My List'}
              className="flex h-7 w-7 items-center justify-center rounded-full border-2 border-white/60 bg-[#2a2a2a]/70 text-white transition-colors hover:border-white md:h-8 md:w-8"
            >
              {inList ? <Check className="h-3.5 w-3.5 md:h-4 md:w-4" /> : <Plus className="h-3.5 w-3.5 md:h-4 md:w-4" />}
            </button>
            <button
              onClick={e => { e.stopPropagation(); open(); }}
              title="More Info"
              className="ml-auto flex h-7 w-7 items-center justify-center rounded-full border-2 border-white/60 bg-[#2a2a2a]/70 text-white transition-colors hover:border-white md:h-8 md:w-8"
            >
              <ChevronDown className="h-4 w-4" />
            </button>
          </div>
          <p className="line-clamp-1 text-xs font-bold text-white md:text-sm">{item.title}</p>
          <div className="mt-0.5 flex items-center gap-1.5 text-[10px] font-semibold md:text-xs">
            {match !== null && <span className="text-[#46d369]">{match}% Match</span>}
            {!!item.year && <span className="text-[#bcbcbc]">{item.year}</span>}
            <span className="rounded-sm border border-white/40 px-1 text-[9px] leading-tight text-white/80">HD</span>
          </div>
          {!!item.genreNames && item.genreNames.length > 0 && (
            <p className="mt-0.5 line-clamp-1 text-[10px] text-white/80 md:text-xs">
              {item.genreNames.slice(0, 3).join(' • ')}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

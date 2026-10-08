"use client";

import React, { useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import TitleCard, { CardVariant } from '@/features/catalog/components/TitleCard';
import type { NfTitle } from '@/features/catalog/titles';

interface TitleRowProps {
  title: string;
  items: NfTitle[];
  variant?: CardVariant;
  loading?: boolean;
  /** Shows big rank numbers next to the posters ("Top 10" rows) */
  ranked?: boolean;
  /** "Explore All" link next to the title */
  exploreHref?: string;
  onPlay?: (item: NfTitle) => void;
  className?: string;
}

const cardWidth: Record<CardVariant, string> = {
  poster: 'w-[30vw] sm:w-[22vw] md:w-[17vw] lg:w-[13.5vw] xl:w-[11.5vw] max-w-[230px]',
  backdrop: 'w-[44vw] sm:w-[32vw] md:w-[24vw] lg:w-[19vw] xl:w-[16vw] max-w-[320px]',
};

// Netflix row: bold title, horizontal scroller, chevron buttons on hover
export default function TitleRow({ title, items, variant = 'poster', loading, ranked, exploreHref, onPlay, className = '' }: TitleRowProps) {
  const scroller = useRef<HTMLDivElement>(null);
  const [canLeft, setCanLeft] = useState(false);
  const [canRight, setCanRight] = useState(false);

  const update = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    setCanLeft(el.scrollLeft > 4);
    setCanRight(el.scrollLeft + el.clientWidth < el.scrollWidth - 4);
  }, []);

  useEffect(() => {
    update();
    window.addEventListener('resize', update);
    return () => window.removeEventListener('resize', update);
  }, [items, update]);

  const page = (dir: 1 | -1) => {
    const el = scroller.current;
    if (!el) return;
    el.scrollBy({ left: dir * el.clientWidth * 0.85, behavior: 'smooth' });
  };

  if (!loading && items.length === 0) return null;
  const list = ranked ? items.slice(0, 10) : items;

  return (
    <section className={`group/row relative ${className}`}>
      <div className="mb-1 flex items-baseline gap-3 px-4 md:px-12">
        <h2 className="text-base font-bold text-[#e5e5e5] sm:text-lg md:text-[1.3rem]">{title}</h2>
        {!!exploreHref && (
          <Link
            href={exploreHref}
            className="flex items-center text-xs font-semibold text-[#54b9c5] opacity-0 transition-opacity group-hover/row:opacity-100"
          >
            Explore All <ChevronRight className="h-3.5 w-3.5" />
          </Link>
        )}
      </div>

      <div className="relative">
        <div
          ref={scroller}
          onScroll={update}
          className="scrollbar-hide flex gap-1.5 overflow-x-auto scroll-smooth px-4 py-5 md:gap-2 md:px-12"
          style={{ scrollbarWidth: 'none' }}
        >
          {loading && items.length === 0
            ? Array.from({ length: 8 }).map((_, i) => (
                <div
                  key={i}
                  className={`shrink-0 animate-pulse rounded-md bg-[#2a2a2a] ${cardWidth[variant]} ${variant === 'poster' ? 'aspect-[2/3]' : 'aspect-video'}`}
                />
              ))
            : list.map((item, i) =>
                ranked ? (
                  <div key={item.key} className="flex shrink-0 items-end">
                    <span
                      aria-hidden
                      className="-mr-3 select-none text-[22vw] font-black leading-[0.8] text-[#141414] sm:text-[14vw] md:text-[10vw] lg:text-[8vw]"
                      style={{ WebkitTextStroke: '3px #595959' }}
                    >
                      {i + 1}
                    </span>
                    <TitleCard item={item} variant="poster" onPlay={onPlay} className={cardWidth.poster} />
                  </div>
                ) : (
                  <TitleCard key={item.key} item={item} variant={variant} onPlay={onPlay} className={`shrink-0 ${cardWidth[variant]}`} />
                )
              )}
        </div>

        {canLeft && (
          <button
            aria-label="Scroll left"
            onClick={() => page(-1)}
            className="absolute inset-y-5 left-0 z-30 hidden w-4 items-center justify-center bg-black/50 text-white opacity-0 transition-opacity hover:bg-black/70 group-hover/row:opacity-100 md:flex md:w-12"
          >
            <ChevronLeft className="h-8 w-8 transition-transform hover:scale-125" />
          </button>
        )}
        {canRight && (
          <button
            aria-label="Scroll right"
            onClick={() => page(1)}
            className="absolute inset-y-5 right-0 z-30 hidden w-4 items-center justify-center bg-black/50 text-white opacity-0 transition-opacity hover:bg-black/70 group-hover/row:opacity-100 md:flex md:w-12"
          >
            <ChevronRight className="h-8 w-8 transition-transform hover:scale-125" />
          </button>
        )}
      </div>
    </section>
  );
}

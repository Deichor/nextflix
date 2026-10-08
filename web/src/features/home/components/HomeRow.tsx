"use client";

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import TitleCard, { CARD_WIDTH, CardVariant } from '@/features/home/components/HomeTitleCard';
import { TitleItem } from '@/features/home/feeds';
import { ImageRowGate, useImageRowGate } from '@/shared/ui/TmdbImage';
import { useT } from '@/shared/i18n';

interface RowProps {
  title: string;
  items: TitleItem[];
  variant?: CardVariant;
  playable?: boolean;
  /** Top 10 row: posters with big rank numbers (item.rank, else the position) */
  ranked?: boolean;
  /** Shows placeholder cards instead of items */
  loading?: boolean;
}

// Netflix-style horizontal row with chevron paging on hover
export default function Row({ title, items, variant = 'backdrop', playable = true, loading = false, ranked = false }: RowProps) {
  const t = useT();
  const cardVariant: CardVariant = ranked ? 'poster' : variant;
  const scrollRef = useRef<HTMLDivElement>(null);
  const gate = useImageRowGate(scrollRef);
  const [canLeft, setCanLeft] = useState(false);
  const [canRight, setCanRight] = useState(false);

  const updateArrows = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    setCanLeft(el.scrollLeft > 4);
    setCanRight(el.scrollLeft + el.clientWidth < el.scrollWidth - 4);
  }, []);

  useEffect(() => {
    updateArrows();
    window.addEventListener('resize', updateArrows);
    return () => window.removeEventListener('resize', updateArrows);
  }, [items, loading, updateArrows]);

  const page = (dir: 1 | -1) => {
    const el = scrollRef.current;
    if (el) el.scrollBy({ left: dir * el.clientWidth * 0.9, behavior: 'smooth' });
  };

  if (!loading && items.length === 0) return null;

  const arrow = 'absolute top-3 bottom-3 z-30 w-10 md:w-12 flex items-center justify-center bg-[#141414]/60 hover:bg-[#141414]/85 opacity-0 group-hover/row:opacity-100 focus:opacity-100 transition-opacity';

  return (
    <section ref={gate.sectionRef} className="relative group/row" aria-label={title}>
      <h2 className="text-[#e5e5e5] hover:text-white font-bold text-base sm:text-lg md:text-[1.3rem] leading-tight px-4 md:px-12 mb-0.5">
        {title}
      </h2>

      <div className="relative">
        {canLeft && (
          <button onClick={() => page(-1)} className={`${arrow} left-0 rounded-r`} aria-label={`${t('common.scrollLeft')}: ${title}`}>
            <ChevronLeft className="w-8 h-8 md:w-10 md:h-10 text-white" />
          </button>
        )}
        {canRight && (
          <button onClick={() => page(1)} className={`${arrow} right-0 rounded-l`} aria-label={`${t('common.scrollRight')}: ${title}`}>
            <ChevronRight className="w-8 h-8 md:w-10 md:h-10 text-white" />
          </button>
        )}

        <div
          ref={gate.scrollerRef}
          onScroll={updateArrows}
          className="nf-card-row flex overflow-x-auto px-4 md:px-12 py-3 md:py-4 scroll-smooth scrollbar-hide"
        >
          <ImageRowGate value={gate.value}>
            {loading
              ? Array.from({ length: 8 }, (_, i) => (
                  <div
                    key={i}
                    className={`relative shrink-0 ${CARD_WIDTH[cardVariant]} ${cardVariant === 'poster' ? 'aspect-[2/3]' : 'aspect-video'} rounded overflow-hidden`}
                  >
                    <div className="nf-img" />
                  </div>
                ))
              : items.map((item, i) =>
                  ranked ? (
                    // Same look as the catalog Top 10 rows: outlined rank number behind the poster
                    <div key={`${item.media_type}-${item.id}`} className="flex shrink-0 items-end">
                      <span
                        aria-hidden
                        className="-mr-3 select-none text-[22vw] font-black leading-[0.8] text-[#141414] sm:text-[14vw] md:text-[10vw] lg:text-[8vw]"
                        style={{ WebkitTextStroke: '3px #595959' }}
                      >
                        {item.rank ?? i + 1}
                      </span>
                      <TitleCard item={item} variant="poster" playable={playable} />
                    </div>
                  ) : (
                    <TitleCard key={`${item.media_type}-${item.id}`} item={item} variant={variant} playable={playable} />
                  )
                )}
          </ImageRowGate>
        </div>
      </div>
    </section>
  );
}

interface LazyRowProps extends Omit<RowProps, 'items' | 'loading'> {
  load: (signal: AbortSignal) => Promise<TitleItem[]>;
}

// Fetches its items only when the row gets close to the viewport
export function LazyRow({ load, ...rowProps }: LazyRowProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  const [items, setItems] = useState<TitleItem[] | null>(null);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    const el = ref.current;
    if (!el || visible) return;
    if (typeof IntersectionObserver === 'undefined') { setVisible(true); return; }
    const io = new IntersectionObserver(entries => {
      if (entries.some(e => e.isIntersecting)) {
        setVisible(true);
        io.disconnect();
      }
    }, { rootMargin: '150% 0px' });
    io.observe(el);
    return () => io.disconnect();
  }, [visible]);

  useEffect(() => {
    if (!visible) return;
    const ctrl = new AbortController();
    loadRef.current(ctrl.signal)
      .then(list => setItems(list.slice(0, 20)))
      .catch(err => { if (err?.name !== 'AbortError') setItems([]); });
    return () => ctrl.abort();
  }, [visible]);

  // A loaded but empty row disappears
  if (items && items.length === 0) return null;

  return (
    <div ref={ref} className="min-h-[120px]">
      <Row {...rowProps} items={items || []} loading={!items} />
    </div>
  );
}

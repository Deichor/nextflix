"use client";

import React, { useState } from 'react';
import { Play } from 'lucide-react';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { useT } from '@/shared/i18n';
import { matchPercent, NfTitle } from '@/features/catalog/titles';
import RatingButtons from '@/features/favorites/components/RatingButtons';
import TmdbImage from '@/shared/ui/TmdbImage';

// Round Netflix icon button (mute, official site, ...)
export function RoundButton({
  onClick, title, children, className = '',
}: { onClick: () => void; title: string; children: React.ReactNode; className?: string }) {
  return (
    <button
      onClick={onClick}
      title={title}
      aria-label={title}
      className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-full border-2 border-white/50 bg-[#2a2a2a]/60 text-white transition-colors hover:border-white hover:bg-white/10 md:h-11 md:w-11 ${className}`}
    >
      {children}
    </button>
  );
}

const ratable = (item: Pick<NfTitle, 'id' | 'mediaType' | 'title' | 'posterPath' | 'backdropPath'>) => ({
  id: item.id,
  mediaType: item.mediaType,
  title: item.title,
  posterPath: item.posterPath,
  backdropPath: item.backdropPath,
});

// "98% Match 2023 [4K] [HDR] 2h 10m" line (badges show the real source quality once known)
export function MetaLine({
  rating, year, extra, adult, badges = [], className = '',
}: { rating?: number; year?: number; extra?: string; adult?: boolean; badges?: string[]; className?: string }) {
  const t = useT();
  const match = matchPercent(rating);
  return (
    <div className={`flex flex-wrap items-center gap-x-2 gap-y-1 text-sm font-semibold md:text-base ${className}`}>
      {match !== null && <span className="text-[#46d369]">{t('detail.match', { n: match })}</span>}
      {!!year && <span className="text-[#bcbcbc]">{year}</span>}
      {!!adult && <span className="border border-white/40 px-1.5 text-xs text-white/90">18+</span>}
      {!!extra && <span className="text-[#bcbcbc]">{extra}</span>}
      {badges.map(b => (
        <span key={b} className="rounded-[3px] border border-white/40 px-1.5 text-[11px] leading-[1.35] text-white/90">{b}</span>
      ))}
    </div>
  );
}

// Right-hand "Cast: ... / Genres: ..." lines
export function InfoLine({ label, values, max = 4 }: { label: string; values: string[]; max?: number }) {
  const t = useT();
  if (values.length === 0) return null;
  const shown = values.slice(0, max);
  return (
    <p className="text-sm leading-relaxed">
      <span className="text-[#777]">{label}: </span>
      <span className="text-[#ddd]">{shown.join(', ')}{values.length > max ? `, ${t('detail.more')}` : ''}</span>
    </p>
  );
}

// "More Like This" card: 16:9 image, match/year, overview, Favorite + rating
export function MoreLikeThisCard({ item }: { item: NfTitle }) {
  const t = useT();
  const navigate = useNavigate();
  const [failed, setFailed] = useState(false);
  const match = matchPercent(item.rating);
  const img = item.backdrop || item.poster;

  return (
    <div
      onClick={() => navigate.push(item.href)}
      className="group flex cursor-pointer flex-col rounded-md bg-[#2f2f2f] transition-colors hover:bg-[#3a3a3a]"
    >
      <div className="relative aspect-video overflow-hidden rounded-t-md bg-[#1f1f1f]">
        {img && !failed ? (
          <TmdbImage
            src={img}
            kind="backdrop"
            sizes="(min-width: 1280px) 22vw, (min-width: 768px) 30vw, 50vw"
            alt={item.title}
            onError={() => setFailed(true)}
          />
        ) : (
          <div className="flex h-full items-center justify-center p-3 text-center text-sm font-semibold text-[#b3b3b3]">{item.title}</div>
        )}
        <p className="absolute left-2 top-2 line-clamp-1 max-w-[85%] text-xs font-bold text-white drop-shadow-[0_1px_4px_rgba(0,0,0,0.9)]">
          {item.title}
        </p>
        <button
          onClick={e => { e.stopPropagation(); navigate.push(item.playHref || item.href); }}
          aria-label={`${t('player.play')}: ${item.title}`}
          className="absolute inset-0 m-auto flex h-11 w-11 items-center justify-center rounded-full border-2 border-white bg-black/50 text-white opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
        >
          <Play className="h-5 w-5 translate-x-px fill-current" />
        </button>
      </div>
      <div className="flex flex-1 flex-col p-3 md:p-4">
        <div className="mb-2 flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2 text-sm font-semibold">
            {match !== null && <span className="truncate text-[#46d369]">{t('detail.match', { n: match })}</span>}
            {!!item.year && <span className="text-[#bcbcbc]">{item.year}</span>}
          </div>
          <div className="flex shrink-0 items-center gap-1.5" onClick={e => e.stopPropagation()}>
            <RatingButtons item={ratable(item)} size="sm" />
          </div>
        </div>
        <p className="line-clamp-4 text-xs leading-relaxed text-[#d2d2d2] md:text-sm">{item.overview || t('detail.noDescription')}</p>
      </div>
    </div>
  );
}

export function MoreLikeThis({ items, title }: { items: NfTitle[]; title?: string }) {
  const t = useT();
  if (items.length === 0) return null;
  return (
    <section>
      <h2 className="mb-4 text-xl font-bold text-white md:text-2xl">{title || t('detail.moreLikeThis')}</h2>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 md:gap-4 xl:grid-cols-4">
        {items.map(item => <MoreLikeThisCard key={item.key} item={item} />)}
      </div>
    </section>
  );
}

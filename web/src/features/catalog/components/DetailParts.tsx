"use client";

import React, { useState } from 'react';
import { Check, Play, Plus } from 'lucide-react';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { matchPercent, NfTitle } from '@/features/catalog/titles';
import { useMyList } from '@/features/my-list/store';

// Round Netflix icon button (+ My List, mute, ...)
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

export function MyListButton({ item }: { item: Pick<NfTitle, 'id' | 'mediaType'> }) {
  const { has, toggle } = useMyList();
  const inList = has(item);
  return (
    <RoundButton onClick={() => toggle(item)} title={inList ? 'Remove from My List' : 'Add to My List'}>
      {inList ? <Check className="h-5 w-5" /> : <Plus className="h-5 w-5" />}
    </RoundButton>
  );
}

// "98% Match 2023 [HD] 2h 10m" line
export function MetaLine({
  rating, year, extra, adult, className = '',
}: { rating?: number; year?: number; extra?: string; adult?: boolean; className?: string }) {
  const match = matchPercent(rating);
  return (
    <div className={`flex flex-wrap items-center gap-x-2 gap-y-1 text-sm font-semibold md:text-base ${className}`}>
      {match !== null && <span className="text-[#46d369]">{match}% Match</span>}
      {!!year && <span className="text-[#bcbcbc]">{year}</span>}
      {!!adult && <span className="border border-white/40 px-1.5 text-xs text-white/90">18+</span>}
      {!!extra && <span className="text-[#bcbcbc]">{extra}</span>}
      <span className="rounded-[3px] border border-white/40 px-1.5 text-[11px] leading-[1.35] text-white/90">HD</span>
    </div>
  );
}

// Right-hand "Cast: ... / Genres: ..." lines
export function InfoLine({ label, values, max = 4 }: { label: string; values: string[]; max?: number }) {
  if (values.length === 0) return null;
  const shown = values.slice(0, max);
  return (
    <p className="text-sm leading-relaxed">
      <span className="text-[#777]">{label}: </span>
      <span className="text-[#ddd]">{shown.join(', ')}{values.length > max ? ', more' : ''}</span>
    </p>
  );
}

// "More Like This" card: 16:9 image, match/year, overview, + My List
export function MoreLikeThisCard({ item }: { item: NfTitle }) {
  const navigate = useNavigate();
  const { has, toggle } = useMyList();
  const [failed, setFailed] = useState(false);
  const inList = has(item);
  const match = matchPercent(item.rating);
  const img = item.backdrop || item.poster;

  return (
    <div
      onClick={() => navigate.push(item.href)}
      className="group flex cursor-pointer flex-col overflow-hidden rounded-md bg-[#2f2f2f] transition-colors hover:bg-[#3a3a3a]"
    >
      <div className="relative aspect-video bg-[#1f1f1f]">
        {img && !failed ? (
          <img src={img} alt={item.title} loading="lazy" onError={() => setFailed(true)} className="h-full w-full object-cover" />
        ) : (
          <div className="flex h-full items-center justify-center p-3 text-center text-sm font-semibold text-[#b3b3b3]">{item.title}</div>
        )}
        <p className="absolute left-2 top-2 line-clamp-1 max-w-[85%] text-xs font-bold text-white drop-shadow-[0_1px_4px_rgba(0,0,0,0.9)]">
          {item.title}
        </p>
        <button
          onClick={e => { e.stopPropagation(); navigate.push(item.playHref || item.href); }}
          aria-label={`Play ${item.title}`}
          className="absolute inset-0 m-auto flex h-11 w-11 items-center justify-center rounded-full border-2 border-white bg-black/50 text-white opacity-0 transition-opacity group-hover:opacity-100"
        >
          <Play className="h-5 w-5 translate-x-px fill-current" />
        </button>
      </div>
      <div className="flex flex-1 flex-col p-3 md:p-4">
        <div className="mb-2 flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 text-sm font-semibold">
            {match !== null && <span className="text-[#46d369]">{match}% Match</span>}
            {!!item.year && <span className="text-[#bcbcbc]">{item.year}</span>}
          </div>
          <button
            onClick={e => { e.stopPropagation(); toggle(item); }}
            title={inList ? 'Remove from My List' : 'Add to My List'}
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full border-2 border-white/50 text-white hover:border-white"
          >
            {inList ? <Check className="h-4 w-4" /> : <Plus className="h-4 w-4" />}
          </button>
        </div>
        <p className="line-clamp-4 text-xs leading-relaxed text-[#d2d2d2] md:text-sm">{item.overview || 'No description available.'}</p>
      </div>
    </div>
  );
}

export function MoreLikeThis({ items, title = 'More Like This' }: { items: NfTitle[]; title?: string }) {
  if (items.length === 0) return null;
  return (
    <section>
      <h2 className="mb-4 text-xl font-bold text-white md:text-2xl">{title}</h2>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 md:gap-4 xl:grid-cols-4">
        {items.map(item => <MoreLikeThisCard key={item.key} item={item} />)}
      </div>
    </section>
  );
}

export function formatRuntime(minutes?: number): string {
  if (!minutes || minutes <= 0) return '';
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

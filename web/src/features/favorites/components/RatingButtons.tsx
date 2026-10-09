"use client";

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Heart, ThumbsDown, ThumbsUp } from 'lucide-react';
import DoubleThumbsUp from '@/features/favorites/components/DoubleThumbsUp';
import { useT, type MessageKey } from '@/shared/i18n';
import {
  RatableTitle,
  RatingValue,
  setTitleFavorite,
  setTitleRating,
  useTitleRating,
} from '@/features/favorites/store';

export type RatingButtonSize = 'sm' | 'md';

const SIZE: Record<RatingButtonSize, { btn: string; icon: string; option: string }> = {
  // Touch screens get 44px targets
  sm: { btn: 'h-8 w-8 pointer-coarse:h-11 pointer-coarse:w-11', icon: 'h-4 w-4', option: 'h-9 w-9 pointer-coarse:h-11 pointer-coarse:w-11' },
  md: { btn: 'h-11 w-11', icon: 'h-5 w-5', option: 'h-11 w-11 md:h-12 md:w-12' },
};

const circle =
  'relative flex shrink-0 items-center justify-center rounded-full border-2 border-white/50 bg-[#2a2a2a]/60 text-white transition-colors hover:border-white hover:bg-white/10 focus-visible:border-white focus-visible:outline-none';

const OPTIONS: { value: RatingValue; label: MessageKey }[] = [
  { value: 'dislike', label: 'rating.dislike' },
  { value: 'like', label: 'rating.like' },
  { value: 'love', label: 'rating.love' },
];

const OPEN_DELAY_MS = 180;
const CLOSE_GRACE_MS = 220;

export function RatingIcon({ value, className, filled }: { value: RatingValue | null; className: string; filled?: boolean }) {
  if (value === 'love') return <DoubleThumbsUp className={className} filled={!!filled} />;
  if (value === 'dislike') return <ThumbsDown className={`${className} ${filled ? 'fill-current' : ''}`} aria-hidden />;
  return <ThumbsUp className={`${className} ${filled ? 'fill-current' : ''}`} aria-hidden />;
}

// Small white Netflix tooltip above a control
function Tip({ text, show }: { text: string; show: boolean }) {
  return (
    <span
      role="tooltip"
      className={`pointer-events-none absolute bottom-full left-1/2 z-20 mb-2 -translate-x-1/2 whitespace-nowrap rounded bg-[#e6e6e6] px-2.5 py-1 text-xs font-bold text-black shadow-lg transition-opacity duration-150 md:text-sm ${show ? 'opacity-100' : 'opacity-0'}`}
    >
      {text}
    </span>
  );
}

const stop = (e: React.SyntheticEvent) => e.stopPropagation();

/** Heart toggle: filled when the title is a favorite. */
export function FavoriteButton({ item, size = 'sm' }: { item: RatableTitle; size?: RatingButtonSize }) {
  const t = useT();
  const r = useTitleRating(item);
  const fav = !!r?.favorite;
  const [hover, setHover] = useState(false);
  const label = t(fav ? 'rating.unfavorite' : 'rating.favorite');
  const s = SIZE[size];
  return (
    <button
      type="button"
      onClick={e => { e.stopPropagation(); setTitleFavorite(item, !fav); }}
      onPointerEnter={e => { if (e.pointerType === 'mouse') setHover(true); }}
      onPointerLeave={() => setHover(false)}
      // Keyboard focus only: a tap must not leave the tooltip on screen
      onFocus={e => { if (e.currentTarget.matches(':focus-visible')) setHover(true); }}
      onBlur={() => setHover(false)}
      onKeyDown={stop}
      aria-label={label}
      aria-pressed={fav}
      className={`${circle} ${s.btn}`}
    >
      <Heart className={`${s.icon} transition-transform ${fav ? 'scale-110 fill-[#E50914] text-[#E50914]' : ''}`} aria-hidden />
      <Tip text={label} show={hover} />
    </button>
  );
}

/** Thumbs button; hover or click opens the Not for me / I like this / Love this! pill. */
export function RateButton({ item, size = 'sm' }: { item: RatableTitle; size?: RatingButtonSize }) {
  const t = useT();
  const r = useTitleRating(item);
  const current = r?.rating ?? null;
  const [open, setOpen] = useState(false);
  const [hovered, setHovered] = useState<RatingValue | null>(null);
  const [focusIdx, setFocusIdx] = useState(-1);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const timers = useRef<{ open?: number; close?: number }>({});
  const s = SIZE[size];

  const clear = () => {
    window.clearTimeout(timers.current.open);
    window.clearTimeout(timers.current.close);
    timers.current = {};
  };
  const close = useCallback(() => {
    clear();
    setOpen(false);
    setHovered(null);
    setFocusIdx(-1);
  }, []);

  useEffect(() => () => clear(), []);

  // Click outside closes (touch and keyboard users)
  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close();
    };
    document.addEventListener('pointerdown', onDown, true);
    return () => document.removeEventListener('pointerdown', onDown, true);
  }, [open, close]);

  useEffect(() => {
    if (open && focusIdx >= 0) optionRefs.current[focusIdx]?.focus();
  }, [open, focusIdx]);

  const openNow = (focus: boolean) => {
    clear();
    setOpen(true);
    const idx = OPTIONS.findIndex(o => o.value === current);
    if (focus) setFocusIdx(idx >= 0 ? idx : 1);
  };

  const pick = (value: RatingValue, viaPointer: string) => {
    setTitleRating(item, current === value ? null : value);
    // Mouse users keep the pill until they move away; touch and keyboard close it
    if (viaPointer !== 'mouse') {
      close();
      if (viaPointer === 'keyboard') buttonRef.current?.focus();
    }
  };

  const onPointerEnter = (e: React.PointerEvent) => {
    if (e.pointerType !== 'mouse') return;
    window.clearTimeout(timers.current.close);
    if (open) return;
    timers.current.open = window.setTimeout(() => setOpen(true), OPEN_DELAY_MS);
  };
  const onPointerLeave = (e: React.PointerEvent) => {
    if (e.pointerType !== 'mouse') return;
    window.clearTimeout(timers.current.open);
    timers.current.close = window.setTimeout(close, CLOSE_GRACE_MS);
  };

  const onPillKey = (e: React.KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
      buttonRef.current?.focus();
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault();
      const d = e.key === 'ArrowLeft' ? -1 : 1;
      setFocusIdx(i => (Math.max(0, i) + d + OPTIONS.length) % OPTIONS.length);
    }
  };

  const label = current ? `${t('rating.rated')}: ${t(OPTIONS.find(o => o.value === current)!.label)}` : t('rating.rate');

  return (
    <div
      ref={rootRef}
      className="relative"
      onPointerEnter={onPointerEnter}
      onPointerLeave={onPointerLeave}
      onBlur={e => { if (!rootRef.current?.contains(e.relatedTarget as Node)) close(); }}
    >
      <button
        ref={buttonRef}
        type="button"
        onClick={e => {
          e.stopPropagation();
          if (open) close();
          else openNow(e.detail === 0);
        }}
        onKeyDown={e => {
          e.stopPropagation();
          if (e.key === 'ArrowUp' || e.key === 'ArrowDown') { e.preventDefault(); openNow(true); }
        }}
        aria-label={label}
        aria-haspopup="true"
        aria-expanded={open}
        className={`${circle} ${s.btn}`}
      >
        <RatingIcon value={current} filled={!!current} className={s.icon} />
      </button>

      {open && (
        <div
          role="group"
          aria-label={t('rating.rate')}
          onClick={stop}
          onKeyDown={onPillKey}
          className="nf-rate-pill absolute left-1/2 top-1/2 z-30 flex -translate-x-1/2 -translate-y-1/2 items-center gap-0.5 rounded-full border border-white/10 bg-[#232323] p-1 shadow-[0_4px_18px_rgba(0,0,0,0.75)]"
        >
          {OPTIONS.map((o, i) => {
            const active = current === o.value;
            return (
              <button
                key={o.value}
                ref={el => { optionRefs.current[i] = el; }}
                type="button"
                aria-pressed={active}
                aria-label={t(o.label)}
                onPointerEnter={() => setHovered(o.value)}
                onPointerLeave={() => setHovered(h => (h === o.value ? null : h))}
                onFocus={() => setHovered(o.value)}
                onClick={e => {
                  e.stopPropagation();
                  pick(o.value, e.detail === 0 ? 'keyboard' : (e.nativeEvent as PointerEvent).pointerType || 'mouse');
                }}
                className={`relative flex ${s.option} items-center justify-center rounded-full text-white transition-colors hover:bg-white/15 focus-visible:bg-white/20 focus-visible:outline-none ${active ? 'bg-white/10' : ''}`}
              >
                <RatingIcon value={o.value} filled={active} className={`${s.icon} transition-transform ${hovered === o.value ? 'scale-110' : ''}`} />
                <Tip text={t(o.label)} show={hovered === o.value} />
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** Favorite + rating controls side by side (cards, billboards, detail page). */
export default function RatingButtons({ item, size = 'sm' }: { item: RatableTitle; size?: RatingButtonSize }) {
  return (
    <>
      <FavoriteButton item={item} size={size} />
      <RateButton item={item} size={size} />
    </>
  );
}

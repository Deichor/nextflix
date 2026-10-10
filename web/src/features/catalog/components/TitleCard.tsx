"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ChevronDown, Play } from 'lucide-react';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { localGenres, matchPercent, NfTitle } from '@/features/catalog/titles';
import RatingButtons from '@/features/favorites/components/RatingButtons';
import type { RatableTitle } from '@/features/favorites/store';
import { useT } from '@/shared/i18n';
import { ResolveParams } from '@/features/watch/api';
import { useSourceQuality } from '@/features/watch/useSourceQuality';
import { useAvailability, useUnavailableLabel } from '@/features/watch/components/WatchButton';
import { tmdbImage } from '@/shared/lib/image';
import TmdbImage from '@/shared/ui/TmdbImage';
import InstantChip from '@/shared/ui/InstantChip';

export type CardVariant = 'poster' | 'backdrop';

// ---- Hover preview (shared by catalog and home cards) ----

const OPEN_DELAY_MS = 300;
const CLOSE_GRACE_MS = 120;
const EXPAND_MS = 250;
const EDGE = 12;
const INFO_H = 112;
const POSTER_SCALE = 1.35;

export interface PreviewState {
  rect: DOMRect;
  open: boolean;
  keep: () => void;
  leave: () => void;
  close: () => void;
  onExited: () => void;
}

export interface PreviewInfo {
  title: string;
  /** Large image in the card's orientation (backdrop w780 / poster w500) */
  image?: string;
  /** Already loaded card image, shown while `image` loads */
  placeholder?: string;
  match?: number | null;
  /** Replaces the match label, e.g. "Coming Oct 12" */
  note?: string;
  year?: number;
  isTv: boolean;
  genres: string[];
  progress?: number;
  /** Shows the Favorite and rating buttons for this title */
  rateItem?: RatableTitle;
  playable: boolean;
  resume?: boolean;
  /** What Play would resolve; used to show the real quality badge once the source is known */
  source?: ResolveParams;
}

interface HoverPreviewProps {
  state: PreviewState;
  info: PreviewInfo;
  /** Orientation of the card the preview grows from */
  variant?: CardVariant;
  onPlay: () => void;
  onOpen: () => void;
}

// Mouse-only hover intent: opens the preview after OPEN_DELAY_MS. Hovering never warms a
// stream source (that only happens once the detail page opens); the preview only shows the
// quality of a source the server already knows.
export function useHoverPreview() {
  const anchorRef = useRef<HTMLDivElement>(null);
  const [rect, setRect] = useState<DOMRect | null>(null);
  const [open, setOpen] = useState(false);
  const openRef = useRef(false);
  const timers = useRef<{ open?: number; close?: number }>({});

  const stop = useCallback((k: 'open' | 'close') => {
    window.clearTimeout(timers.current[k]);
    timers.current[k] = undefined;
  }, []);

  const close = useCallback(() => {
    stop('open');
    stop('close');
    openRef.current = false;
    setOpen(false);
  }, [stop]);

  const keep = useCallback(() => stop('close'), [stop]);

  const leave = useCallback(() => {
    if (!openRef.current) { close(); return; }
    stop('close');
    timers.current.close = window.setTimeout(close, CLOSE_GRACE_MS);
  }, [close, stop]);

  const enter = useCallback(() => {
    keep();
    if (openRef.current || timers.current.open !== undefined) return;
    timers.current.open = window.setTimeout(() => {
      timers.current.open = undefined;
      const el = anchorRef.current;
      if (!el) return;
      openRef.current = true;
      setRect(el.getBoundingClientRect());
      setOpen(true);
    }, OPEN_DELAY_MS);
  }, [keep]);

  const onExited = useCallback(() => { if (!openRef.current) setRect(null); }, []);

  useEffect(() => () => {
    const t = timers.current;
    window.clearTimeout(t.open);
    window.clearTimeout(t.close);
  }, []);

  const anchorProps = {
    onPointerEnter: (e: React.PointerEvent) => { if (e.pointerType === 'mouse') enter(); },
    onPointerLeave: (e: React.PointerEvent) => { if (e.pointerType === 'mouse') leave(); },
  };

  const preview: PreviewState | null = rect ? { rect, open, keep, leave, close, onExited } : null;
  return { anchorRef, anchorProps, preview };
}

function computeLayout(rect: DOMRect, reduced: boolean, variant: CardVariant) {
  const vw = document.documentElement.clientWidth;
  const vh = window.innerHeight;
  const ratio = variant === 'poster' ? 3 / 2 : 9 / 16;
  const preferred = variant === 'poster'
    ? Math.min(Math.max(rect.width * POSTER_SCALE, 220), 340)
    : Math.min(Math.max(rect.width * 1.5, 280), 480);
  // Shrinks on short screens so image + info panel always fit vertically
  const width = Math.round(Math.max(120, Math.min(preferred, vw - EDGE * 2, (vh - EDGE * 2 - INFO_H) / ratio)));
  const imgH = width * ratio;
  const cx = rect.left + rect.width / 2;
  const cy = rect.top + rect.height / 2;

  // Centered on the card; edge cards grow toward the inside
  let left = cx - width / 2;
  if (left < EDGE) left = Math.max(rect.left, EDGE);
  else if (left + width > vw - EDGE) left = Math.min(rect.right, vw - EDGE) - width;
  // Shifts up when the info panel would leave the viewport
  const top = Math.max(EDGE, Math.min(cy - imgH / 2, vh - EDGE - imgH - INFO_H));

  // Collapsed state: the preview image sits exactly on the card
  const s = rect.width / width;
  const dx = rect.left - left;
  const dy = cy - top - (s * imgH) / 2;
  return { left, top, width, from: reduced ? 'none' : `translate(${dx}px, ${dy}px) scale(${s})` };
}

const circleBtn =
  'flex h-8 w-8 shrink-0 items-center justify-center rounded-full border-2 border-white/50 bg-[#2a2a2a]/60 text-white transition-colors hover:border-white hover:bg-white/10';

// Enlarged card floating above the rows (portaled, so row overflow never clips it)
export function HoverPreview({ state, info, variant = 'backdrop', onPlay, onOpen }: HoverPreviewProps) {
  const t = useT();
  const badges = useSourceQuality(info.playable ? info.source : null);
  // Not out yet (or only in theaters): no Play, the date instead of the match
  const src = info.playable ? info.source : undefined;
  const availability = useAvailability(src?.tmdb_id ? { media_type: src.type, tmdb_id: src.tmdb_id, season: src.season, episode: src.episode } : null);
  const unavailable = useUnavailableLabel(availability);
  const { rect, open, keep, leave, close, onExited } = state;
  const [ready, setReady] = useState(false);
  const [hiLoaded, setHiLoaded] = useState(false);
  const [hiFailed, setHiFailed] = useState(false);
  const reduced = useMemo(() => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false, []);
  const layout = useMemo(() => computeLayout(rect, reduced, variant), [rect, reduced, variant]);
  const poster = variant === 'poster';
  const expanded = ready && open;

  useEffect(() => {
    let inner = 0;
    const outer = requestAnimationFrame(() => { inner = requestAnimationFrame(() => setReady(true)); });
    return () => { cancelAnimationFrame(outer); cancelAnimationFrame(inner); };
  }, []);

  useEffect(() => {
    if (open) return;
    const t = window.setTimeout(onExited, reduced ? 130 : EXPAND_MS + 20);
    return () => window.clearTimeout(t);
  }, [open, reduced, onExited]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') close(); };
    window.addEventListener('scroll', close, true);
    window.addEventListener('resize', close);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('scroll', close, true);
      window.removeEventListener('resize', close);
      window.removeEventListener('keydown', onKey);
    };
  }, [open, close]);

  const act = (fn: () => void) => (e: React.MouseEvent) => { e.stopPropagation(); close(); fn(); };
  const showHi = !!info.image && !hiFailed;
  const placeholder = info.placeholder && info.placeholder !== info.image ? info.placeholder : undefined;
  const progress = Math.min(100, info.progress || 0);

  return createPortal(
    <div
      className="nf-preview"
      data-expanded={expanded}
      onPointerEnter={e => { if (e.pointerType === 'mouse') keep(); }}
      onPointerLeave={e => { if (e.pointerType === 'mouse') leave(); }}
      onClick={act(onOpen)}
      style={{
        left: layout.left,
        top: layout.top,
        width: layout.width,
        transform: expanded ? 'none' : layout.from,
        pointerEvents: open ? 'auto' : 'none',
      }}
    >
      <div className={`relative overflow-hidden rounded-t-md bg-[#2f2f2f] ${poster ? 'aspect-[2/3]' : 'aspect-video'}`}>
        {poster && !placeholder && (
          <div className="absolute inset-0 flex items-center justify-center p-3 text-center text-sm font-semibold text-[#b3b3b3]">
            {info.title}
          </div>
        )}
        {!!placeholder && (
          <img src={placeholder} alt="" draggable={false} className="absolute inset-0 h-full w-full object-cover" />
        )}
        {showHi && (
          <img
            src={info.image}
            alt={info.title}
            draggable={false}
            onLoad={() => setHiLoaded(true)}
            onError={() => setHiFailed(true)}
            className={`absolute inset-0 h-full w-full object-cover transition-opacity duration-300 ${hiLoaded || !placeholder ? 'opacity-100' : 'opacity-0'}`}
          />
        )}
        {/* Posters carry their title in the artwork */}
        {!poster && (
          <div className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/85 via-black/35 to-transparent px-3 pb-2.5 pt-10">
            <p className="line-clamp-2 text-base font-extrabold leading-tight text-white drop-shadow-[0_1px_3px_rgba(0,0,0,0.9)] md:text-lg">
              {info.title}
            </p>
          </div>
        )}
        {progress > 0 && (
          <div className="absolute inset-x-0 bottom-0 h-[3px] bg-[#5a5a5a]/80">
            <div className="h-full bg-[#E50914]" style={{ width: `${progress}%` }} />
          </div>
        )}
      </div>

      <div className="nf-preview-info rounded-b-md bg-[#181818] px-3 pb-3 pt-2.5">
        <div className="mb-2.5 flex items-center gap-2">
          {info.playable && !unavailable && (
            <button
              onClick={act(onPlay)}
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-white text-black transition-colors hover:bg-white/80"
              aria-label={`${t(info.resume ? 'player.resume' : 'player.play')}: ${info.title}`}
              title={t(info.resume ? 'player.resume' : 'player.play')}
            >
              <Play className="h-4 w-4 translate-x-px fill-current" />
            </button>
          )}
          {!!info.rateItem && <RatingButtons item={info.rateItem} size="sm" />}
          <button onClick={act(onOpen)} className={`ml-auto ${circleBtn}`} aria-label={t('detail.moreInfo')} title={t('detail.moreInfo')}>
            <ChevronDown className="h-4 w-4" />
          </button>
        </div>

        <div className="flex items-center gap-2 overflow-hidden whitespace-nowrap text-xs font-semibold">
          {unavailable ? (
            <span className="truncate text-[#bcbcbc]">{unavailable}</span>
          ) : info.note ? (
            <span className="text-[#46d369]">{info.note}</span>
          ) : (
            !!info.match && info.match > 0 && <span className="text-[#46d369]">{t('detail.match', { n: info.match })}</span>
          )}
          {!!info.year && <span className="text-[#bcbcbc]">{info.year}</span>}
          {info.isTv && <span className="text-[#bcbcbc]">{t('detail.series')}</span>}
          {badges.map(b => (
            <span key={b} className="rounded-sm border border-white/40 px-1 text-[10px] leading-tight text-white/85">{b}</span>
          ))}
          {!unavailable && (
            <InstantChip variant="label" className="text-[11px]" item={src?.tmdb_id ? { type: src.type, tmdb_id: src.tmdb_id, title: src.title, year: src.year } : null} />
          )}
        </div>
        {info.genres.length > 0 && (
          <p className="mt-1.5 truncate text-xs text-white/85">
            {info.genres.map((g, i) => (
              <React.Fragment key={g}>
                {i > 0 && <span className="px-1 text-white/40">•</span>}
                {g}
              </React.Fragment>
            ))}
          </p>
        )}
      </div>
    </div>,
    document.body,
  );
}

// ---- Catalog card ----

interface TitleCardProps {
  item: NfTitle;
  variant?: CardVariant;
  /** Overrides the default Play action (TMDB titles open the detail page with ?play=1) */
  onPlay?: (item: NfTitle) => void;
  /** 0-100 red progress bar at the bottom of the image */
  progress?: number;
  className?: string;
  /** `sizes` for the image srcset (the card's CSS width per breakpoint) */
  sizes?: string;
}

const DEFAULT_SIZES: Record<CardVariant, string> = {
  poster: '(min-width: 1024px) 15vw, (min-width: 640px) 22vw, 33vw',
  backdrop: '(min-width: 1024px) 22vw, (min-width: 640px) 33vw, 50vw',
};

// Netflix-style card: plain image in the row, a floating preview (Play, Favorite, Rate, More Info) on hover
export default function TitleCard({ item, variant = 'poster', onPlay, progress = 0, className = '', sizes }: TitleCardProps) {
  const navigate = useNavigate();
  const t = useT();
  const [imgFailed, setImgFailed] = useState(false);
  const img = variant === 'poster' ? item.poster || item.backdrop : item.backdrop || item.poster;

  const { anchorRef, anchorProps, preview } = useHoverPreview();

  const open = () => navigate.push(item.href);
  const play = () => {
    if (onPlay) onPlay(item);
    else navigate.push(item.playHref || item.href);
  };

  return (
    <>
      <div
        ref={anchorRef}
        {...anchorProps}
        role="link"
        tabIndex={0}
        aria-label={item.title}
        onClick={open}
        onKeyDown={e => { if (e.key === 'Enter') open(); }}
        className={`nf-card relative cursor-pointer rounded-md bg-[#181818] ${className}`}
      >
        <div className={`relative overflow-hidden rounded-md ${variant === 'poster' ? 'aspect-[2/3]' : 'aspect-video'}`}>
          {img && !imgFailed ? (
            <TmdbImage src={img} kind={variant} sizes={sizes || DEFAULT_SIZES[variant]} alt={item.title} onError={() => setImgFailed(true)} />
          ) : (
            <div className="flex h-full w-full items-center justify-center bg-[#2f2f2f] p-3 text-center text-sm font-semibold text-[#b3b3b3]">
              {item.title}
            </div>
          )}

          {/* Backdrop cards carry their title on the image, like Netflix rows */}
          {variant === 'backdrop' && !!img && !imgFailed && (
            <div className="pointer-events-none absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/80 to-transparent px-2 pb-1.5 pt-6">
              <p className="line-clamp-1 text-xs font-bold text-white drop-shadow md:text-sm">{item.title}</p>
            </div>
          )}

          <InstantChip item={{ type: item.mediaType, tmdb_id: item.id, title: item.title, year: item.year }} />

          {progress > 0 && (
            <div className="absolute inset-x-2 bottom-1.5 h-[3px] bg-[#5a5a5a]/90">
              <div className="h-full bg-[#E50914]" style={{ width: `${Math.min(100, progress)}%` }} />
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
            image: variant === 'poster'
              ? tmdbImage(item.posterPath, 'w500') || item.poster || item.backdrop
              : item.backdrop || item.poster,
            placeholder: !imgFailed ? img : undefined,
            match: matchPercent(item.rating),
            year: item.year,
            isTv: item.mediaType === 'tv',
            genres: localGenres(t, item.genreIds),
            progress,
            rateItem: { id: item.id, mediaType: item.mediaType, title: item.title, posterPath: item.posterPath, backdropPath: item.backdropPath },
            playable: true,
            source: item.mediaType === 'tv'
              ? { type: 'tv', title: item.title, tmdb_id: item.id, season: 1, episode: 1 }
              : { type: 'movie', title: item.title, tmdb_id: item.id, year: item.year },
          }}
          onPlay={play}
          onOpen={open}
        />
      )}
    </>
  );
}

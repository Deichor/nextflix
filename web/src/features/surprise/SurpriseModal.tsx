"use client";

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Info, Play, Shuffle, X } from 'lucide-react';
import { useT } from '@/shared/i18n';
import { tmdbImage } from '@/shared/lib/image';
import TmdbImage from '@/shared/ui/TmdbImage';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { localGenres, matchPercent, tmdbDetailHref } from '@/features/catalog/titles';
import { fetchSurprise, reasonText, type SurprisePick } from '@/features/surprise/api';

interface SurpriseModalProps {
  onClose: () => void;
  /** Plays in place (home page); otherwise Play opens the detail page with ?play=1 */
  onPlay?: (pick: SurprisePick) => void;
}

// "Surprise me": one pick at a time, "Something else" asks for the next one
export default function SurpriseModal({ onClose, onPlay }: SurpriseModalProps) {
  const t = useT();
  const navigate = useNavigate();
  const [pick, setPick] = useState<SurprisePick | null>(null);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [logoFailed, setLogoFailed] = useState(false);
  const ctrlRef = useRef<AbortController | null>(null);
  const playRef = useRef<HTMLButtonElement>(null);

  const next = useCallback(() => {
    ctrlRef.current?.abort();
    const ctrl = new AbortController();
    ctrlRef.current = ctrl;
    setLoading(true);
    setFailed(false);
    fetchSurprise(ctrl.signal)
      .then(p => {
        if (ctrl.signal.aborted) return;
        if (p) {
          setPick(p);
          setLogoFailed(false);
        } else {
          setFailed(true);
        }
      })
      .catch(() => { if (!ctrl.signal.aborted) setFailed(true); })
      .finally(() => { if (!ctrl.signal.aborted) setLoading(false); });
  }, []);

  useEffect(() => {
    next();
    return () => ctrlRef.current?.abort();
  }, [next]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = overflow;
    };
  }, [onClose]);

  useEffect(() => { if (pick) playRef.current?.focus(); }, [pick]);

  const play = () => {
    if (!pick) return;
    onClose();
    if (onPlay) onPlay(pick);
    else navigate.push(tmdbDetailHref(pick.id, pick.mediaType, true));
  };
  const details = () => {
    if (!pick) return;
    onClose();
    navigate.push(tmdbDetailHref(pick.id, pick.mediaType));
  };

  const year = pick?.date ? parseInt(pick.date.slice(0, 4), 10) : undefined;
  const match = pick ? matchPercent(pick.rating) : null;
  const genres = pick ? localGenres(t, pick.genreIds) : [];

  return (
    <div
      className="fixed inset-0 z-[200] flex items-center justify-center bg-black/75 p-4"
      onClick={e => { if (e.target === e.currentTarget) onClose(); }}
      role="dialog"
      aria-modal="true"
      aria-label={t('surprise.heading')}
    >
      <div className="relative w-full max-w-3xl overflow-hidden rounded-md bg-[#181818] text-white shadow-2xl">
        <button
          onClick={onClose}
          className="absolute right-3 top-3 z-20 flex h-9 w-9 items-center justify-center rounded-full bg-[#181818]/80 text-white hover:bg-[#2a2a2a]"
          aria-label={t('common.close')}
        >
          <X className="h-5 w-5" />
        </button>

        <div className="relative aspect-video w-full bg-[#141414]">
          {!!pick?.backdropPath && (
            <TmdbImage key={pick.backdropPath} src={pick.backdropPath} kind="backdrop" sizes="768px" priority="eager" />
          )}
          <div className="absolute inset-x-0 bottom-0 h-2/3 bg-gradient-to-t from-[#181818] via-[#181818]/60 to-transparent" />
          <div className="absolute left-6 top-5 text-xs font-bold uppercase tracking-[0.2em] text-white/80 md:left-10">
            {t('surprise.heading')}
          </div>
          {!!pick && (
            <div className="absolute bottom-4 left-6 right-6 md:left-10">
              {!!pick.logoPath && !logoFailed ? (
                <img
                  src={tmdbImage(pick.logoPath, 'w500')}
                  alt={pick.title}
                  onError={() => setLogoFailed(true)}
                  className="mb-2 max-h-24 max-w-[60%] object-contain object-left drop-shadow-[0_4px_12px_rgba(0,0,0,0.6)] md:max-h-28"
                />
              ) : (
                <h2 className="mb-2 text-3xl font-black leading-tight drop-shadow-[0_2px_8px_rgba(0,0,0,0.7)] md:text-4xl">
                  {pick.title}
                </h2>
              )}
            </div>
          )}
          {loading && (
            <div className="absolute inset-0 flex items-center justify-center bg-black/30">
              <div className="h-12 w-12 animate-spin rounded-full border-4 border-white/20 border-t-[#e50914]" />
            </div>
          )}
        </div>

        <div className="px-6 pb-7 pt-1 md:px-10">
          {!pick && failed && <p className="py-6 text-center text-[#b3b3b3]">{t('surprise.error')}</p>}
          {!pick && !failed && <p className="py-6 text-center text-[#b3b3b3]">{t('surprise.loading')}</p>}
          {!!pick && (
            <>
              <p className="mb-2 flex items-center gap-2 text-sm font-semibold text-[#e5e5e5] md:text-base">
                <Shuffle className="h-4 w-4 shrink-0 text-[#e50914]" />
                {reasonText(t, pick.reason)}
              </p>
              <div className="mb-3 flex flex-wrap items-center gap-x-2 text-sm text-white/80">
                {!!match && <span className="font-semibold text-[#46d369]">{t('detail.match', { n: match })}</span>}
                {!!year && <span>{year}</span>}
                <span>{t(pick.mediaType === 'tv' ? 'detail.series' : 'common.movie')}</span>
                {genres.length > 0 && <span className="text-white/60">· {genres.join(' • ')}</span>}
              </div>
              {!!pick.overview && <p className="mb-5 line-clamp-3 text-sm leading-relaxed text-white/90 md:text-base">{pick.overview}</p>}
              <div className="flex flex-wrap gap-2 md:gap-3">
                <button
                  ref={playRef}
                  onClick={play}
                  className="flex items-center gap-2 rounded bg-white px-6 py-2 text-base font-bold text-black hover:bg-white/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-white focus-visible:ring-offset-2 focus-visible:ring-offset-[#181818]"
                >
                  <Play className="h-5 w-5 fill-black" /> {t('player.play')}
                </button>
                <button
                  onClick={details}
                  className="flex items-center gap-2 rounded bg-[#6d6d6e]/70 px-5 py-2 text-base font-bold text-white hover:bg-[#6d6d6e]/50"
                >
                  <Info className="h-5 w-5" /> {t('surprise.details')}
                </button>
                <button
                  onClick={next}
                  disabled={loading}
                  className="flex items-center gap-2 rounded border border-white/40 px-5 py-2 text-base font-bold text-white hover:border-white disabled:opacity-60"
                >
                  <Shuffle className="h-5 w-5" /> {t('surprise.next')}
                </button>
              </div>
            </>
          )}
          {!pick && failed && (
            <div className="flex justify-center">
              <button onClick={next} className="rounded bg-white px-6 py-2 font-bold text-black hover:bg-white/80">
                {t('common.retry')}
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

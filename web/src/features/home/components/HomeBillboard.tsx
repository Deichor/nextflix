"use client";

import React, { useEffect, useState } from 'react';
import { Info } from 'lucide-react';
import WatchButton, { formatRemaining } from '@/features/watch/components/WatchButton';
import { episodeLabel, progressPercent } from '@/features/watch/api';
import { useTitleActions } from '@/features/home/components/TitleActions';
import { TitleItem, fetchMovieLogo, genreLabels, img, matchPercent, yearOf } from '@/features/home/feeds';

interface BillboardProps {
  item: TitleItem | null;
  loading?: boolean;
}

// Full-bleed featured title at the top of the home page
export default function Billboard({ item, loading = false }: BillboardProps) {
  const { play, openInfo, progressFor } = useTitleActions();
  const [logo, setLogo] = useState('');
  const [logoFailed, setLogoFailed] = useState(false);
  const [bgLoaded, setBgLoaded] = useState(false);

  useEffect(() => {
    setLogo('');
    setLogoFailed(false);
    setBgLoaded(false);
    if (!item || item.media_type !== 'movie') return;
    const ctrl = new AbortController();
    fetchMovieLogo(item.id, ctrl.signal).then(path => { if (!ctrl.signal.aborted) setLogo(path); });
    return () => ctrl.abort();
  }, [item]);

  const shell = 'relative w-full h-[75vw] sm:h-[56.25vw] max-h-[92vh] min-h-[420px] overflow-hidden bg-[#141414]';

  if (!item) {
    return (
      <div className={shell}>
        {loading && <div className="absolute inset-0 bg-gradient-to-br from-[#1f1f1f] to-[#141414] animate-pulse" />}
      </div>
    );
  }

  const saved = progressFor(item);
  const pct = saved ? progressPercent(saved) : 0;
  const year = yearOf(item);
  const match = matchPercent(item);
  const genres = genreLabels(item);
  const backdrop = item.backdrop_path || item.poster_path;

  return (
    <section className={shell} aria-label={`Featured: ${item.title}`}>
      {!!backdrop && (
        <img
          src={img('w1280', backdrop)}
          srcSet={`${img('w1280', backdrop)} 1280w, ${img('original', backdrop)} 1920w`}
          sizes="100vw"
          alt=""
          fetchPriority="high"
          onLoad={() => setBgLoaded(true)}
          className={`absolute inset-0 w-full h-full object-cover object-top transition-opacity duration-700 ${bgLoaded ? 'opacity-100' : 'opacity-0'}`}
        />
      )}

      {/* Readability: left fade, top fade under the nav, bottom vignette into the page */}
      <div className="absolute inset-0 bg-gradient-to-r from-black/80 via-black/30 to-transparent" />
      <div className="absolute inset-x-0 top-0 h-32 bg-gradient-to-b from-black/60 to-transparent" />
      <div className="absolute inset-x-0 bottom-0 h-[38%] bg-gradient-to-t from-[#141414] via-[#141414]/60 to-transparent" />

      <div className="absolute left-4 md:left-12 right-4 bottom-[22%] sm:bottom-[30%] max-w-[92%] sm:max-w-[48%] lg:max-w-[38%]">
        {!!logo && !logoFailed ? (
          <img
            src={img('w500', logo)}
            alt={item.title}
            onError={() => setLogoFailed(true)}
            className="max-h-[90px] sm:max-h-[120px] md:max-h-[160px] max-w-full w-auto object-contain object-left mb-3 md:mb-5 drop-shadow-[0_4px_12px_rgba(0,0,0,0.6)]"
          />
        ) : (
          <h1 className="text-white font-black tracking-tight leading-[0.95] text-3xl sm:text-5xl lg:text-6xl mb-3 md:mb-5 drop-shadow-[0_2px_8px_rgba(0,0,0,0.7)] line-clamp-3">
            {item.title}
          </h1>
        )}

        <div className="flex items-center gap-2 text-xs sm:text-sm font-semibold mb-2 md:mb-3">
          {match > 0 && <span className="text-[#46d369]">{match}% Match</span>}
          {!!year && <span className="text-white/90">{year}</span>}
          <span className="text-white/90">{item.media_type === 'tv' ? 'Series' : 'Movie'}</span>
          {genres.length > 0 && <span className="hidden sm:inline text-white/70">· {genres.join(' • ')}</span>}
        </div>

        {!!item.overview && (
          <p className="hidden sm:block text-white text-sm md:text-base lg:text-lg leading-snug line-clamp-3 drop-shadow-[0_2px_4px_rgba(0,0,0,0.8)] mb-4 md:mb-6">
            {item.overview}
          </p>
        )}

        <div className="flex flex-wrap items-start gap-2 md:gap-3">
          <WatchButton
            onClick={() => play(item)}
            resume={!!saved}
            detail={saved && item.media_type === 'tv' ? episodeLabel(saved.season, saved.episode) : undefined}
            progress={pct}
            remainingText={saved && saved.duration > 0 ? formatRemaining(saved.position, saved.duration) : undefined}
          />
          <button
            onClick={() => openInfo(item)}
            className="flex items-center justify-center gap-2 px-5 py-2 md:px-7 md:py-2.5 rounded bg-[#6d6d6e]/70 hover:bg-[#6d6d6e]/50 text-white font-bold text-sm md:text-lg transition-colors"
          >
            <Info className="w-5 h-5 md:w-6 md:h-6" />
            <span>More Info</span>
          </button>
        </div>
      </div>
    </section>
  );
}

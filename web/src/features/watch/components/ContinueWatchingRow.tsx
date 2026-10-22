"use client";

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { ChevronLeft, ChevronRight, Info, Play, X } from 'lucide-react';
import TorrentStreamPlayer from '@/features/watch/components/LazyTorrentStreamPlayer';
import { tmdbDetailHref } from '@/features/catalog/titles';
import { useActiveProfile } from '@/shared/lib/profile';
import { useFormat, useLocale, useT } from '@/shared/i18n';
import InstantChip from '@/shared/ui/InstantChip';
import {
  PlayRequest,
  StreamProgress,
  deleteProgress,
  episodeLabel,
  progressPercent,
  progressToRequest,
} from '@/features/watch/api';
import { loadProgress, removeProgressEntry, useProgressList } from '@/features/watch/progressStore';

interface ContinueWatchingRowProps {
  /** Overrides the default "Continue Watching for <profile>" heading */
  title?: string;
  className?: string;
}

// Netflix-style "Continue Watching" row for the active profile.
// Clicking a card resumes it directly in the torrent player; a card whose next episode is not out
// yet shows its air date and opens the title page instead.
export default function ContinueWatchingRow({ title, className = '' }: ContinueWatchingRowProps) {
  const profile = useActiveProfile();
  const t = useT();
  const locale = useLocale();
  const fmt = useFormat();
  const router = useRouter();
  const scrollRef = useRef<HTMLDivElement>(null);
  const [request, setRequest] = useState<PlayRequest | null>(null);
  const [canLeft, setCanLeft] = useState(false);
  const [canRight, setCanRight] = useState(false);

  const profileId = profile?.id ?? null;
  // Shared with the home cards (one request per page load)
  const items = useProgressList(profileId);

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
  }, [items, updateArrows]);

  const scrollBy = (dir: 1 | -1) => {
    const el = scrollRef.current;
    if (el) el.scrollBy({ left: dir * el.clientWidth * 0.85, behavior: 'smooth' });
  };

  const remove = async (p: StreamProgress) => {
    if (!profileId) return;
    removeProgressEntry(profileId, p.media_type, p.tmdb_id);
    const ok = await deleteProgress(profileId, p.media_type, p.tmdb_id);
    if (!ok) loadProgress(profileId, { force: true });
  };

  const closePlayer = useCallback(() => {
    setRequest(null);
    // Give the final progress save a moment to land
    if (profileId) setTimeout(() => loadProgress(profileId, { force: true }), 600);
  }, [profileId]);

  // The player keeps one place in the tree: a row that appears or empties while it plays
  // must not remount it (a remount closes the player and releases the playing title)
  const player = request ? <TorrentStreamPlayer {...request} onClose={closePlayer} /> : null;
  if (!profileId || items.length === 0) return <>{null}{player}</>;

  return (
    <>
      <section className={`relative group/row ${className}`}>
        <h2 className="text-white font-bold text-lg md:text-[1.3rem] mb-2 px-4 md:px-12">
          {title || t('home.continueWatching', { name: profile?.name ?? '' })}
        </h2>

        <div className="relative">
          {canLeft && (
            <button
              onClick={() => scrollBy(-1)}
              className="absolute left-0 top-0 bottom-8 z-20 w-10 md:w-12 flex items-center justify-center bg-black/50 hover:bg-black/70 opacity-0 group-hover/row:opacity-100 transition-opacity"
              aria-label={t('common.scrollLeft')}
            >
              <ChevronLeft className="w-8 h-8 text-white" />
            </button>
          )}
          {canRight && (
            <button
              onClick={() => scrollBy(1)}
              className="absolute right-0 top-0 bottom-8 z-20 w-10 md:w-12 flex items-center justify-center bg-black/50 hover:bg-black/70 opacity-0 group-hover/row:opacity-100 transition-opacity"
              aria-label={t('common.scrollRight')}
            >
              <ChevronRight className="w-8 h-8 text-white" />
            </button>
          )}

          <div
            ref={scrollRef}
            onScroll={updateArrows}
            className="flex gap-2 overflow-x-auto overflow-y-visible px-4 md:px-12 py-3 scroll-smooth [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
          >
            {items.map(p => {
              const pct = progressPercent(p);
              const ep = p.media_type === 'tv' ? episodeLabel(p.season, p.episode, locale) : '';
              const img = p.backdrop_path || p.poster_path;
              const upcoming = p.next_air_date
                ? t('home.newEpisodeOn', { when: airDay(p.next_air_date, fmt.date) })
                : '';
              return (
                <div
                  key={`${p.media_type}-${p.tmdb_id}`}
                  className="group/card relative shrink-0 w-[220px] sm:w-[260px] md:w-[300px] transition-transform duration-300 hover:scale-[1.08] hover:z-10"
                >
                  <button
                    onClick={() => (upcoming ? router.push(tmdbDetailHref(p.tmdb_id, 'tv')) : setRequest(progressToRequest(p)))}
                    className="block w-full text-left"
                    title={upcoming ? `${p.title}: ${upcoming}` : `${t('player.resume')}: ${p.title}${ep ? ` ${ep}` : ''}`}
                  >
                    <div className="relative aspect-video rounded overflow-hidden bg-[#2f2f2f]">
                      {img ? (
                        <RetryImage src={`/tmdb-img/t/p/w780${img}`} alt={p.title} className="w-full h-full object-cover" />
                      ) : (
                        <div className="w-full h-full flex items-center justify-center text-[#b3b3b3] text-sm px-3 text-center">{p.title}</div>
                      )}
                      <div className="absolute inset-0 flex items-center justify-center bg-black/0 group-hover/card:bg-black/40 transition-colors">
                        <span className="w-12 h-12 rounded-full border-2 border-white bg-black/50 flex items-center justify-center opacity-0 group-hover/card:opacity-100 transition-opacity">
                          {upcoming ? <Info className="w-5 h-5 text-white" /> : <Play className="w-5 h-5 text-white fill-current translate-x-0.5" />}
                        </span>
                      </div>
                      {upcoming ? (
                        <span className="absolute bottom-0 inset-x-0 bg-gradient-to-t from-black/90 to-transparent px-2.5 pt-6 pb-1.5 text-white text-xs md:text-sm font-semibold">
                          {upcoming}
                        </span>
                      ) : (
                        <>
                          <InstantChip
                            corner="left"
                            item={{ type: p.media_type, tmdb_id: p.tmdb_id, title: p.title, season: p.season || undefined, episode: p.episode || undefined }}
                          />
                          <div className="absolute bottom-0 inset-x-0 h-[3px] bg-[#5a5a5a]/80">
                            <div className="h-full bg-[#E50914]" style={{ width: `${pct}%` }} />
                          </div>
                        </>
                      )}
                    </div>
                    <div className="pt-2 pr-6">
                      <p className="text-white text-sm font-semibold truncate">{p.title}</p>
                      {!!ep && (
                        <p className="text-[#b3b3b3] text-xs truncate">
                          {ep}{p.episode_title ? ` · ${p.episode_title}` : ''}
                        </p>
                      )}
                    </div>
                  </button>
                  <button
                    onClick={() => remove(p)}
                    className="absolute top-2 right-2 w-7 h-7 rounded-full bg-black/70 border border-white/40 hover:border-white flex items-center justify-center opacity-0 group-hover/card:opacity-100 focus:opacity-100 transition-opacity"
                    title={t('home.removeFromRow')}
                    aria-label={`${t('home.removeFromRow')}: ${p.title}`}
                  >
                    <X className="w-4 h-4 text-white" />
                  </button>
                </div>
              );
            })}
          </div>
        </div>
      </section>
      {player}
    </>
  );
}

// A card image that tries again a few times (e.g. the server was restarting when the page loaded)
function RetryImage({ src, alt, className }: { src: string; alt: string; className: string }) {
  const [attempt, setAttempt] = useState(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  if (attempt > 3) return null;
  return (
    <img
      key={attempt}
      src={attempt ? `${src}?retry=${attempt}` : src}
      alt={alt}
      loading="lazy"
      className={className}
      onError={() => { timer.current = setTimeout(() => setAttempt(a => a + 1), 2000 * (attempt + 1)); }}
    />
  );
}

// "Wednesday" within the coming week, else "October 29" (YYYY-MM-DD read as a local day)
function airDay(day: string, format: (d: Date, opts: Intl.DateTimeFormatOptions) => string): string {
  const [y, m, d] = day.split('-').map(Number);
  const date = new Date(y, m - 1, d);
  const now = new Date();
  const days = Math.round((date.getTime() - new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()) / 86_400_000);
  return format(date, days > 0 && days < 7 ? { weekday: 'long' } : { month: 'long', day: 'numeric' });
}

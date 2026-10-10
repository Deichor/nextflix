"use client";

import React, { useEffect, useState } from 'react';
import { CalendarClock, ChevronDown, Play } from 'lucide-react';
import { useFormat, useT } from '@/shared/i18n';
import { getActiveProfileId } from '@/shared/lib/profile';
import {
  EpisodeInfo,
  SeasonInfo,
  StreamProgress,
  getSeasonEpisodes,
  getTitleProgress,
  progressPercent,
  isFutureDate,
  isPlaceholderEpisodeName,
} from '@/features/watch/api';

interface SeasonEpisodesProps {
  tmdbId: number;
  seasons: SeasonInfo[];
  /** Season shown first (e.g. the one being watched) */
  initialSeason?: number;
  /** Image used when an episode has no still */
  fallbackImage?: string;
  /** Change it to reload per-episode progress (e.g. after the player closes) */
  refreshKey?: number;
  onPlay: (season: number, episode: EpisodeInfo, progress: StreamProgress | null) => void;
}

const NO_EPISODES: EpisodeInfo[] = [];
const NO_PROGRESS: Record<number, StreamProgress> = {};

// Netflix-style season selector + episode list with per-episode progress
export default function SeasonEpisodes({ tmdbId, seasons, initialSeason, fallbackImage, refreshKey = 0, onPlay }: SeasonEpisodesProps) {
  const t = useT();
  const f = useFormat();
  const [season, setSeason] = useState<number>(initialSeason || seasons[0]?.season_number || 1);
  // A new initialSeason (e.g. the one being watched changed) selects it
  const [prevInitial, setPrevInitial] = useState(initialSeason);
  if (initialSeason !== prevInitial) {
    setPrevInitial(initialSeason);
    if (initialSeason) setSeason(initialSeason);
  }

  const episodeCount = seasons.find(s => s.season_number === season)?.episode_count ?? 0;
  const listKey = `${tmdbId}:${season}:${episodeCount}`;
  // Loaded lists are tagged with what they were loaded for; anything else is still loading
  const [loaded, setLoaded] = useState<{ key: string; episodes: EpisodeInfo[] } | null>(null);
  const loading = !loaded || loaded.key !== listKey;
  const episodes = loading ? NO_EPISODES : loaded.episodes;
  const [progressFor, setProgressFor] = useState<{ episodes: EpisodeInfo[]; map: Record<number, StreamProgress> } | null>(null);
  const progress = progressFor && progressFor.episodes === episodes ? progressFor.map : NO_PROGRESS;

  useEffect(() => {
    if (episodeCount <= 0) return;
    let cancelled = false;
    getSeasonEpisodes(tmdbId, { season_number: season, episode_count: episodeCount }).then(eps => {
      if (!cancelled) setLoaded({ key: `${tmdbId}:${season}:${episodeCount}`, episodes: eps });
    });
    return () => { cancelled = true; };
  }, [tmdbId, season, episodeCount]);

  // One request for the whole title, then keep this season's episodes
  useEffect(() => {
    const pid = getActiveProfileId();
    if (!pid || episodes.length === 0) return;
    let cancelled = false;
    getTitleProgress(pid, 'tv', tmdbId).then(list => {
      if (cancelled) return;
      const map: Record<number, StreamProgress> = {};
      list.forEach(p => { if (p.season === season && !map[p.episode]) map[p.episode] = p; });
      setProgressFor({ episodes, map });
    });
    return () => { cancelled = true; };
  }, [tmdbId, season, episodes, refreshKey]);

  if (seasons.length === 0) return null;

  return (
    <section className="mt-2">
      <div className="flex items-center justify-between gap-4 mb-4">
        <h2 className="text-white text-xl md:text-2xl font-bold">{t('detail.episodes')}</h2>
        {seasons.length > 1 ? (
          <div className="relative">
            <select
              value={season}
              onChange={e => setSeason(Number(e.target.value))}
              className="appearance-none bg-[#242424] text-white font-semibold text-sm md:text-base border border-[#4d4d4d] rounded pl-4 pr-10 py-2 cursor-pointer focus:outline-none focus:border-white"
            >
              {seasons.map(s => (
                <option key={s.season_number} value={s.season_number} className="bg-[#242424]">
                  {s.name && !/^Season \d+$/i.test(s.name) ? s.name : t('detail.season', { n: s.season_number })} ({t('detail.episodeCount', { count: s.episode_count })})
                </option>
              ))}
            </select>
            <ChevronDown className="w-4 h-4 text-white absolute right-3 top-1/2 -translate-y-1/2 pointer-events-none" />
          </div>
        ) : (
          <span className="text-[#b3b3b3] text-sm md:text-base">{t('detail.season', { n: season })}</span>
        )}
      </div>

      <div className="divide-y divide-[#404040] border-y border-[#404040]">
        {loading && episodes.length === 0 && (
          <div className="py-10 text-center text-[#b3b3b3] text-sm">{t('detail.loadingEpisodes')}</div>
        )}
        {episodes.map(ep => {
          const p = progress[ep.episode_number] || null;
          const pct = p ? progressPercent(p) : 0;
          const img = ep.still_path ? `/tmdb-img/t/p/w300${ep.still_path}` : fallbackImage;
          // Not aired yet: dimmed, with its air date, and not playable
          const upcoming = isFutureDate(ep.air_date);
          const name = isPlaceholderEpisodeName(ep) ? t('detail.episode', { n: ep.episode_number }) : ep.name;
          return (
            <button
              key={`${ep.season_number}-${ep.episode_number}`}
              onClick={() => { if (!upcoming) onPlay(season, ep, p); }}
              disabled={upcoming}
              aria-disabled={upcoming}
              className={`group w-full flex items-center gap-3 md:gap-5 px-2 md:px-4 py-4 text-left rounded transition-colors focus:outline-none ${upcoming ? 'cursor-default opacity-50' : 'hover:bg-[#333] focus:bg-[#333]'}`}
            >
              <span className="hidden sm:block w-6 shrink-0 text-center text-[#d2d2d2] text-xl md:text-2xl">{ep.episode_number}</span>
              <div className="relative shrink-0 w-32 md:w-40 aspect-video rounded overflow-hidden bg-[#2f2f2f]">
                {img ? (
                  <img src={img} alt={name} loading="lazy" className="w-full h-full object-cover" />
                ) : (
                  <div className="w-full h-full flex items-center justify-center text-[#b3b3b3] text-xs">{ep.episode_number}</div>
                )}
                {!upcoming && (
                  <div className="absolute inset-0 flex items-center justify-center bg-black/0 group-hover:bg-black/40 transition-colors">
                    <span className="w-9 h-9 rounded-full border-2 border-white bg-black/50 flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity">
                      <Play className="w-4 h-4 text-white fill-current translate-x-px" />
                    </span>
                  </div>
                )}
                {pct > 0 && (
                  <div className="absolute bottom-0 inset-x-0 h-[3px] bg-[#5a5a5a]/80">
                    <div className="h-full bg-[#E50914]" style={{ width: `${pct}%` }} />
                  </div>
                )}
              </div>
              <div className="flex-1 min-w-0">
                <div className="flex items-baseline justify-between gap-3">
                  <p className="text-white font-semibold text-sm md:text-base truncate">
                    <span className="sm:hidden">{ep.episode_number}. </span>{name}
                  </p>
                  {upcoming ? (
                    <span className="flex items-center gap-1 text-[#d2d2d2] text-xs md:text-sm shrink-0">
                      <CalendarClock className="w-4 h-4" />
                      {t('availability.airsOn', { date: f.date(ep.air_date) })}
                    </span>
                  ) : (
                    ep.runtime > 0 && <span className="text-[#d2d2d2] text-sm shrink-0">{f.duration(ep.runtime)}</span>
                  )}
                </div>
                {!!ep.overview && (
                  <p className="text-[#b3b3b3] text-xs md:text-sm mt-1 line-clamp-2 md:line-clamp-3">{ep.overview}</p>
                )}
              </div>
            </button>
          );
        })}
      </div>
    </section>
  );
}

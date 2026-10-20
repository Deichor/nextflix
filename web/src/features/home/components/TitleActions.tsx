"use client";

import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import TorrentStreamPlayer from '@/features/watch/components/LazyTorrentStreamPlayer';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { useActiveProfile } from '@/shared/lib/profile';
import {
  PlayRequest,
  getTitleProgress,
  getTvSeasons,
  isFinished,
  nextEpisodeOf,
  progressToRequest,
} from '@/features/watch/api';
import { getProgressFor, loadProgress } from '@/features/watch/progressStore';
import { TitleItem, detailHref, yearOf } from '@/features/home/feeds';

interface TitleActions {
  play: (t: TitleItem) => void;
  openInfo: (t: TitleItem) => void;
}

const Ctx = createContext<TitleActions | null>(null);

// Shared Play / More Info actions for the billboard and every row (ratings live in features/favorites,
// saved progress in features/watch/progressStore). Owns the one torrent player instance of the page.
export function TitleActionsProvider({ children }: { children: React.ReactNode }) {
  const { push } = useNavigate();
  const profile = useActiveProfile();
  const [request, setRequest] = useState<PlayRequest | null>(null);
  const clickRef = useRef(0);

  const profileId = profile?.id ?? null;

  useEffect(() => { if (profileId) loadProgress(profileId); }, [profileId]);

  const play = useCallback(async (t: TitleItem) => {
    // A newer click or closing the player makes this one stale
    const click = ++clickRef.current;
    const saved = profileId ? getProgressFor(t.media_type, t.id, profileId) : undefined;
    if (saved) {
      const req = progressToRequest(saved);
      setRequest({ ...req, meta: { ...req.meta, original_language: t.original_language } });
      return;
    }
    // TV whose newest episode is finished continues with the next one, otherwise S1E1
    let season = 1;
    let episode = 1;
    if (t.media_type === 'tv' && profileId) {
      const latest = (await getTitleProgress(profileId, 'tv', t.id))[0];
      if (click !== clickRef.current) return;
      if (latest && isFinished(latest)) {
        const seasons = await getTvSeasons(t.id);
        if (click !== clickRef.current) return;
        const next = nextEpisodeOf(seasons, latest.season, latest.episode);
        if (next) ({ season, episode } = next);
      }
    }
    // No unfinished progress: the player reuses a finished entry or resolves the best source
    setRequest({
      meta: {
        tmdb_id: t.id,
        media_type: t.media_type,
        title: t.title,
        poster_path: t.poster_path || undefined,
        backdrop_path: t.backdrop_path || undefined,
        year: t.media_type === 'movie' ? yearOf(t) : undefined,
        season: t.media_type === 'tv' ? season : undefined,
        episode: t.media_type === 'tv' ? episode : undefined,
        original_language: t.original_language,
      },
    });
  }, [profileId]);

  const openInfo = useCallback((t: TitleItem) => push(detailHref(t)), [push]);

  const closePlayer = useCallback(() => {
    clickRef.current++;
    setRequest(null);
    // Let the player's final progress save land first
    if (profileId) setTimeout(() => loadProgress(profileId, { force: true }), 600);
  }, [profileId]);

  const value = useMemo(() => ({ play, openInfo }), [play, openInfo]);

  return (
    <Ctx.Provider value={value}>
      {children}
      {!!request && <TorrentStreamPlayer {...request} onClose={closePlayer} />}
    </Ctx.Provider>
  );
}

export function useTitleActions(): TitleActions {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error('useTitleActions must be used inside TitleActionsProvider');
  return ctx;
}

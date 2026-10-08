"use client";

import React, { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import TorrentStreamPlayer from '@/features/watch/components/TorrentStreamPlayer';
import { useNavigate } from '@/shared/navigation/useNavigate';
import { useMyList } from '@/features/my-list/store';
import { useActiveProfile } from '@/shared/lib/profile';
import {
  PlayRequest,
  StreamProgress,
  getProgressList,
  getTitleProgress,
  getTvSeasons,
  isFinished,
  nextEpisodeOf,
  progressToRequest,
} from '@/features/watch/api';
import { TitleItem, detailHref, yearOf } from '@/features/home/feeds';

interface TitleActions {
  play: (t: TitleItem) => void;
  openInfo: (t: TitleItem) => void;
  progressFor: (t: TitleItem) => StreamProgress | undefined;
  inMyList: (t: TitleItem) => boolean;
  toggleMyList: (t: TitleItem) => void;
}

const Ctx = createContext<TitleActions | null>(null);

// Shared Play / More Info / My List actions for the billboard and every row.
// Owns the one torrent player instance of the page.
export function TitleActionsProvider({ children }: { children: React.ReactNode }) {
  const navigate = useNavigate();
  const profile = useActiveProfile();
  const myList = useMyList();
  const [progress, setProgress] = useState<StreamProgress[]>([]);
  const [request, setRequest] = useState<PlayRequest | null>(null);

  const profileId = profile?.id ?? null;

  const loadProgress = useCallback(async () => {
    setProgress(profileId ? await getProgressList(profileId) : []);
  }, [profileId]);

  useEffect(() => { loadProgress(); }, [loadProgress]);

  const progressFor = useCallback(
    (t: TitleItem) => progress.find(p => p.media_type === t.media_type && p.tmdb_id === t.id),
    [progress],
  );

  const play = useCallback(async (t: TitleItem) => {
    const saved = progressFor(t);
    if (saved) {
      setRequest(progressToRequest(saved));
      return;
    }
    // TV whose newest episode is finished continues with the next one, otherwise S1E1
    let season = 1;
    let episode = 1;
    if (t.media_type === 'tv' && profileId) {
      const latest = (await getTitleProgress(profileId, 'tv', t.id))[0];
      if (latest && isFinished(latest)) {
        const next = nextEpisodeOf(await getTvSeasons(t.id), latest.season, latest.episode);
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
      },
    });
  }, [progressFor, profileId]);

  const openInfo = useCallback((t: TitleItem) => navigate.push(detailHref(t)), [navigate]);

  // entries changes identity on every add/remove, so cards re-render with the new state
  const { has, toggle, entries } = myList;
  const inMyList = useCallback((t: TitleItem) => has(t), [has, entries]); // eslint-disable-line react-hooks/exhaustive-deps
  const toggleMyList = useCallback((t: TitleItem) => { toggle({ id: t.id, mediaType: t.media_type }); }, [toggle]);

  const closePlayer = useCallback(() => {
    setRequest(null);
    // Let the player's final progress save land first
    setTimeout(loadProgress, 600);
  }, [loadProgress]);

  const value = useMemo(
    () => ({ play, openInfo, progressFor, inMyList, toggleMyList }),
    [play, openInfo, progressFor, inMyList, toggleMyList],
  );

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

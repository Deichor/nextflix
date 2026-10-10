import { useEffect, useEffectEvent, useState } from 'react';
import { getApiUrl } from '@/shared/lib/api';
import type { StreamMeta } from '@/features/watch/api';

// Intro and end credits of an episode (GET /api/skip-segments): chapter markers of the file,
// else community timestamps. Without them the next episode only comes when the video ends.
export interface SkipSegment {
  start: number;
  end: number;
  source: string;
}

export interface SkipSegments {
  intro: SkipSegment | null;
  credits: SkipSegment | null;
}

export type SkipPhase = 'intro' | 'credits' | null;

/** Which skip button the position calls for. */
export function skipPhase(segs: SkipSegments | null, position: number, duration: number): SkipPhase {
  if (!segs || duration <= 0) return null;
  const i = segs.intro;
  if (i && position >= i.start && position < i.end - 1) return 'intro';
  const c = segs.credits;
  if (c && position >= c.start && position < Math.min(c.end, duration) - 1) return 'credits';
  return null;
}

/** Fetches the segments of the playing file, once per episode and file (series only). */
export function useSkipSegments(meta: StreamMeta | undefined, hash: string, index: number | undefined, duration: number): SkipSegments | null {
  const [segs, setSegs] = useState<{ key: string; value: SkipSegments } | null>(null);
  // Duration at request time; a later change does not refetch
  const durationNow = useEffectEvent(() => duration);
  const tv = meta?.media_type === 'tv' && !!meta.season && !!meta.episode;
  const key = tv && hash && index !== undefined && duration > 0 ? `${meta.tmdb_id}:${meta.season}:${meta.episode}:${hash}:${index}` : '';

  useEffect(() => {
    if (!key || !meta) return;
    const ctl = new AbortController();
    const q = new URLSearchParams({
      tmdb_id: String(meta.tmdb_id), season: String(meta.season), episode: String(meta.episode),
      hash, index: String(index), duration: durationNow().toFixed(1),
    });
    fetch(`${getApiUrl()}/api/skip-segments?${q}`, { signal: ctl.signal })
      .then(r => (r.ok ? r.json() : null))
      .then(v => { if (v) setSegs({ key, value: { intro: v.intro ?? null, credits: v.credits ?? null } }); })
      .catch(() => {});
    return () => ctl.abort();
    // key holds everything the request depends on
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return segs && segs.key === key ? segs.value : null;
}

import { useEffect, useRef, useState } from 'react';
import { getJson } from '../lib/http';
import type { StreamMeta } from '../lib/stream';

// Intro and end credits of an episode (GET /api/skip-segments), as in the web player
// (web/src/features/watch/skipSegments.ts): chapter markers of the file, else community
// timestamps. Without them the next episode only comes when the video ends.
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
export function useSkipSegments(meta: StreamMeta | undefined, file: { hash: string; index: number } | null, duration: number): SkipSegments | null {
  const [segs, setSegs] = useState<{ key: string; value: SkipSegments } | null>(null);
  const durationRef = useRef(duration);
  durationRef.current = duration;
  const tv = !!meta && meta.media_type === 'tv' && !!meta.season && !!meta.episode;
  const key = tv && file && duration > 0 ? `${meta!.tmdb_id}:${meta!.season}:${meta!.episode}:${file.hash}:${file.index}` : '';

  useEffect(() => {
    if (!key || !meta || !file) return;
    let cancelled = false;
    const q = `tmdb_id=${meta.tmdb_id}&season=${meta.season}&episode=${meta.episode}` +
      `&hash=${encodeURIComponent(file.hash)}&index=${file.index}&duration=${durationRef.current.toFixed(1)}`;
    getJson<{ intro?: SkipSegment | null; credits?: SkipSegment | null }>(`/api/skip-segments?${q}`)
      .then(v => { if (!cancelled && v) setSegs({ key, value: { intro: v.intro ?? null, credits: v.credits ?? null } }); })
      .catch(() => {});
    return () => { cancelled = true; };
    // key holds everything the request depends on
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return segs && segs.key === key ? segs.value : null;
}

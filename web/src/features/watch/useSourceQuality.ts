"use client";

import { useEffect, useState } from 'react';
import { getCachedSource, qualityBadges, ResolveParams } from './api';

// Re-check while a prefetch may still be finding the source
const CHECK_AT_MS = [0, 3000, 8000, 15000, 30000];
const NONE: string[] = [];

// Quality badges ("4K", "HDR", "HD") of the source Play would use, once the server has found it.
export function useSourceQuality(params: ResolveParams | null | undefined): string[] {
  const key = params ? JSON.stringify(params) : '';
  // Tagged with the params they belong to: other params show none until theirs arrive
  const [found, setFound] = useState<{ key: string; badges: string[] } | null>(null);

  useEffect(() => {
    if (!params) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const check = async (n: number) => {
      const src = await getCachedSource(params);
      if (cancelled) return;
      if (src) {
        setFound({ key, badges: qualityBadges(src) });
      } else if (n + 1 < CHECK_AT_MS.length) {
        timer = setTimeout(() => check(n + 1), CHECK_AT_MS[n + 1] - CHECK_AT_MS[n]);
      }
    };
    check(0);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return found && found.key === key ? found.badges : NONE;
}

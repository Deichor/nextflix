import { useEffect, useState } from 'react';
import { cached, peek } from './http';

const TEN_MIN = 10 * 60 * 1000;

export interface Query<T> {
  data: T | undefined;
  failed: boolean;
}

export interface QueryOpts<T> {
  /** Shown while loading when the memory cache has nothing (a persisted snapshot) */
  initial?: () => T | undefined;
  /** false: the answer is shown but not kept in the memory cache */
  keep?: (data: T) => boolean;
}

/**
 * Cached loader keyed by `key` (null skips). A cached answer is returned on the first render,
 * so a remounted screen (Back from a detail page) renders its rows and restores focus at once.
 * Changing `reload` loads again (after the cache entry was invalidated) and keeps the old data meanwhile.
 */
export function useQuery<T>(key: string | null, load: () => Promise<T>, ttlMs = TEN_MIN, reload = 0, opts: QueryOpts<T> = {}): Query<T> {
  const first = (k: string | null) => {
    if (!k) return undefined;
    const hit = peek<T>(k, ttlMs);
    return hit !== undefined ? hit : opts.initial?.();
  };
  const [state, setState] = useState<{ key: string | null } & Query<T>>(() => ({ key, data: first(key), failed: false }));

  useEffect(() => {
    if (!key) return;
    const hit = peek<T>(key, ttlMs);
    if (hit !== undefined) {
      setState(s => (s.key === key && s.data === hit ? s : { key, data: hit, failed: false }));
      return;
    }
    let cancelled = false;
    setState(s => (s.key === key ? s : { key, data: opts.initial?.(), failed: false }));
    cached(key, ttlMs, load, opts.keep)
      .then(data => { if (!cancelled) setState({ key, data, failed: false }); })
      .catch(() => { if (!cancelled) setState(s => (s.key === key && s.data !== undefined ? s : { key, data: undefined, failed: true })); });
    return () => { cancelled = true; };
    // `load`, `initial` and `keep` are identified by `key`
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, ttlMs, reload]);

  return state.key === key ? state : { data: first(key), failed: false };
}

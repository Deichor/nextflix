import { useEffect, useState } from 'react';
import { cached, peek } from './http';

const TEN_MIN = 10 * 60 * 1000;

export interface Query<T> {
  data: T | undefined;
  failed: boolean;
}

/**
 * Cached loader keyed by `key` (null skips). A cached answer is returned on the first render,
 * so a remounted screen (Back from a detail page) renders its rows and restores focus at once.
 * Changing `reload` loads again (after the cache entry was invalidated) and keeps the old data meanwhile.
 */
export function useQuery<T>(key: string | null, load: () => Promise<T>, ttlMs = TEN_MIN, reload = 0): Query<T> {
  const [state, setState] = useState<{ key: string | null } & Query<T>>(() => ({
    key,
    data: key ? peek<T>(key, ttlMs) : undefined,
    failed: false,
  }));

  useEffect(() => {
    if (!key) return;
    const hit = peek<T>(key, ttlMs);
    if (hit !== undefined) {
      setState(s => (s.key === key && s.data === hit ? s : { key, data: hit, failed: false }));
      return;
    }
    let cancelled = false;
    setState(s => (s.key === key ? s : { key, data: undefined, failed: false }));
    cached(key, ttlMs, load)
      .then(data => { if (!cancelled) setState({ key, data, failed: false }); })
      .catch(() => { if (!cancelled) setState(s => (s.key === key && s.data !== undefined ? s : { key, data: undefined, failed: true })); });
    return () => { cancelled = true; };
    // `load` is identified by `key`
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, ttlMs, reload]);

  return state.key === key ? state : { data: undefined, failed: false };
}

// The in-memory answer cache of http.ts, least recently used first. Pure, tested in answerCache.test.mjs.

/** `ttl` is the one the entry was stored with (pruned once past it) */
export interface CacheEntry {
  at: number;
  ttl: number;
  data: unknown;
}

/** The data when younger than ttlMs; a hit becomes the most recently used */
export function peekEntry(cache: Map<string, CacheEntry>, key: string, ttlMs: number, now: number): unknown {
  const hit = cache.get(key);
  if (!hit) return undefined;
  const age = now - hit.at;
  if (age >= hit.ttl) {
    cache.delete(key);
    return undefined;
  }
  if (age >= ttlMs) return undefined;
  cache.delete(key);
  cache.set(key, hit);
  return hit.data;
}

/** Stores the entry as the most recently used, drops expired ones and the least recently used past max */
export function storeEntry(cache: Map<string, CacheEntry>, key: string, ttl: number, data: unknown, max: number, now: number) {
  const expired: string[] = [];
  cache.forEach((v, k) => { if (now - v.at >= v.ttl) expired.push(k); });
  expired.forEach(k => cache.delete(k));
  cache.delete(key);
  cache.set(key, { at: now, ttl, data });
  while (cache.size > max) cache.delete(cache.keys().next().value as string);
}

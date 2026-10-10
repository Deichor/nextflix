import { getServer } from './config';
import { getProfileId, profileLocale } from './profile';
import { peekEntry, storeEntry, type CacheEntry } from './answerCache';
import { tmdbImgUrl } from './tmdbImg';

export class HttpError extends Error {
  status: number;
  body: any;
  constructor(status: number, message: string, body?: any) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

export const base = () => getServer() || '';

/** Headers every /api call sends, like the web app: profile (X-User-ID) and content language (X-Lang). */
export function apiHeaders(): Record<string, string> {
  const h: Record<string, string> = { 'X-Lang': profileLocale() };
  const pid = getProfileId();
  if (pid) h['X-User-ID'] = String(pid);
  return h;
}

interface RequestOpts {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** keepalive requests survive the app closing; they skip custom headers (no CORS preflight) */
  keepalive?: boolean;
}

export async function request(path: string, opts: RequestOpts = {}): Promise<Response> {
  const headers: Record<string, string> = opts.keepalive ? {} : apiHeaders();
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  return fetch(`${base()}${path}`, {
    method: opts.method || 'GET',
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    signal: opts.signal,
    keepalive: opts.keepalive,
    cache: 'no-store',
  });
}

export async function getJson<T = any>(path: string, opts: RequestOpts = {}): Promise<T> {
  const res = await request(path, opts);
  let data: any = null;
  try {
    data = res.status === 204 ? null : await res.json();
  } catch {
    /* empty or non-JSON body */
  }
  if (!res.ok) throw new HttpError(res.status, (data && data.error) || `${path} -> ${res.status}`, data);
  return data as T;
}

/** Fire-and-forget POST that also works while the app is closing. */
export function beacon(path: string): void {
  const url = `${base()}${path}`;
  try {
    if (navigator.sendBeacon && navigator.sendBeacon(url)) return;
  } catch {
    /* fall back to fetch */
  }
  fetch(url, { method: 'POST', keepalive: true }).catch(() => {});
}

/** TMDB image through the server's disk-cached proxy (the TV cannot rely on image.tmdb.org). */
export function img(path: string | null | undefined, size: string): string {
  return tmdbImgUrl(base(), path, size);
}

// ---- In-memory answer cache: screens that remount (Back from detail) paint at once ----

const cache = new Map<string, CacheEntry>();
const inflight = new Map<string, Promise<unknown>>();
const CACHE_MAX = 150;

/** Cache key that includes the profile and language (answers depend on both). */
export const scoped = (key: string) => `${getProfileId() ?? 0}|${profileLocale()}|${key}`;

export function peek<T>(key: string, ttlMs: number): T | undefined {
  return peekEntry(cache, key, ttlMs, Date.now()) as T | undefined;
}

function store(key: string, ttl: number, data: unknown) {
  storeEntry(cache, key, ttl, data, CACHE_MAX, Date.now());
}

/** `keep` false: the answer is returned but not cached (the next call loads again). */
export function cached<T>(key: string, ttlMs: number, load: () => Promise<T>, keep?: (data: T) => boolean): Promise<T> {
  const hit = peek<T>(key, ttlMs);
  if (hit !== undefined) return Promise.resolve(hit);
  const pending = inflight.get(key);
  if (pending) return pending as Promise<T>;
  const p = load()
    .then(data => {
      if (!keep || keep(data)) store(key, ttlMs, data);
      return data;
    })
    .finally(() => inflight.delete(key));
  inflight.set(key, p);
  return p;
}

export function invalidate(match: string): void {
  Array.from(cache.keys()).forEach(k => { if (k.includes(match)) cache.delete(k); });
}

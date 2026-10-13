import { getServer } from './config';
import { getProfileId, profileLocale } from './profile';
import { peekEntry, storeEntry, type CacheEntry } from './answerCache';
import { tmdbImgUrl } from './tmdbImg';
import { withQueryParams } from './qp';
import { readJson, writeJson } from './storage';
import type { Locale } from './i18n';

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
export function apiHeaders(locale: Locale = profileLocale(), pid: number | null = getProfileId()): Record<string, string> {
  const h: Record<string, string> = { 'X-Lang': locale };
  if (pid) h['X-User-ID'] = String(pid);
  return h;
}

// ---- Query-param mode: once the server answered /api/home, GETs carry x_lang/profile_id
// in the query instead of headers, so they need no CORS preflight ----

const qpKey = () => `nfx-tv:qp:${base()}`;
const qpState = new Map<string, boolean>();

export function qpEnabled(): boolean {
  const server = base();
  let on = qpState.get(server);
  if (on === undefined) {
    on = readJson<boolean>(qpKey()) === true;
    qpState.set(server, on);
  }
  return on;
}

export function setQp(on: boolean): void {
  if (qpState.get(base()) === on) return;
  qpState.set(base(), on);
  writeJson(qpKey(), on ? true : null);
}

interface RequestOpts {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** keepalive requests survive the app closing; they skip custom headers (no CORS preflight) */
  keepalive?: boolean;
  /** Another profile than the active one (prefetch on the profile picker) */
  profileId?: number | null;
  locale?: Locale;
}

export async function request(path: string, opts: RequestOpts = {}): Promise<Response> {
  const method = opts.method || 'GET';
  const locale = opts.locale || profileLocale();
  const pid = opts.profileId !== undefined ? opts.profileId : getProfileId();
  const simple = method === 'GET' && opts.body === undefined && !opts.keepalive && qpEnabled();
  const headers: Record<string, string> = opts.keepalive || simple ? {} : apiHeaders(locale, pid);
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  return fetch(`${base()}${simple ? withQueryParams(path, locale, pid) : path}`, {
    method,
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
export const scoped = (key: string) => scopedFor(getProfileId() ?? 0, profileLocale(), key);

/** scoped() for a given profile (prefetch before it is active). */
export const scopedFor = (pid: number, locale: Locale, key: string) => `${pid}|${locale}|${key}`;

/** Cache key of profile-independent answers (home, feeds, top 10, discover...). */
export const langScoped = (key: string, locale: Locale = profileLocale()) => `${base()}|${locale}|${key}`;

export function peek<T>(key: string, ttlMs: number): T | undefined {
  return peekEntry(cache, key, ttlMs, Date.now()) as T | undefined;
}

function store(key: string, ttl: number, data: unknown) {
  storeEntry(cache, key, ttl, data, CACHE_MAX, Date.now());
}

/** Stores an answer that came with another one (the feeds inside /api/home). */
export function prime(key: string, ttlMs: number, data: unknown): void {
  store(key, ttlMs, data);
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

const invalidateListeners: ((match: string) => void)[] = [];

/** Called on every invalidate() (persisted copies of the answers drop theirs too). */
export function onInvalidate(fn: (match: string) => void): void {
  invalidateListeners.push(fn);
}

export function invalidate(match: string): void {
  Array.from(cache.keys()).forEach(k => { if (k.includes(match)) cache.delete(k); });
  invalidateListeners.forEach(fn => fn(match));
}

// API configuration for network access
export const getApiUrl = () => {
  // Check if we're in Docker environment
  const dockerApiUrl = process.env.NEXT_PUBLIC_API_URL;

  if (typeof window === 'undefined') {
    // Server-side: Docker internal URL or localhost
    return dockerApiUrl || 'http://localhost:8252';
  }

  const hostname = window.location.hostname;

  // Local network access: connect directly to backend port
  const isLocal = hostname === 'localhost' || hostname === '127.0.0.1' ||
    /^192\.168\./.test(hostname) || /^10\./.test(hostname) ||
    /^172\.(1[6-9]|2\d|3[01])\./.test(hostname);

  if (isLocal) {
    return `http://${hostname}:8252`;
  }

  // External access (ngrok, cloudflare, etc.): use the frontend origin so
  // all /api/* requests go through Next.js rewrites → localhost:8252
  return window.location.origin;
};

export const getApiHost = () => {
  if (typeof window === 'undefined') {
    return 'localhost';
  }
  return window.location.hostname;
};

export const API_ENDPOINTS = {
  profiles: '/api/profiles',
  myList: '/api/mylist',
  streamProgress: '/api/stream-progress',
  notifications: '/api/notifications',
  providers: '/api/providers',
  indexerSettings: '/api/settings/indexer',
};

// Persistent stale-while-revalidate cache.
// - Fresh (< FRESH_TTL): served from cache, no network.
// - Stale (< STALE_TTL): served from cache INSTANTLY, refreshed in background.
// - Persisted to localStorage, so reloads and next-day visits paint from
//   cache immediately instead of waiting on the network.
const FRESH_TTL = 60 * 1000; // 1 minute: no revalidation at all
const STALE_TTL = 24 * 60 * 60 * 1000; // 24h: serve + revalidate in background
const MAX_PERSIST_BYTES = 500 * 1024; // don't persist giant payloads
const LS_PREFIX = 'hfx-api:';

type CacheEntry = { data: any; timestamp: number };
const apiCache = new Map<string, CacheEntry>();
const inflightRevalidations = new Set<string>();

const persistEntry = (key: string, entry: CacheEntry) => {
  if (typeof window === 'undefined') return;
  try {
    const serialized = JSON.stringify(entry);
    if (serialized.length > MAX_PERSIST_BYTES) return;
    window.localStorage.setItem(LS_PREFIX + key, serialized);
  } catch {
    // Quota exceeded: drop the oldest persisted entries and retry once
    try {
      const keys: { k: string; t: number }[] = [];
      for (let i = 0; i < window.localStorage.length; i++) {
        const k = window.localStorage.key(i);
        if (k && k.startsWith(LS_PREFIX)) {
          try {
            keys.push({ k, t: JSON.parse(window.localStorage.getItem(k) || '{}').timestamp || 0 });
          } catch {
            window.localStorage.removeItem(k);
          }
        }
      }
      keys.sort((a, b) => a.t - b.t);
      keys.slice(0, Math.max(1, Math.floor(keys.length / 4))).forEach(({ k }) => window.localStorage.removeItem(k));
      window.localStorage.setItem(LS_PREFIX + key, JSON.stringify(entry));
    } catch {
      /* give up quietly - cache is best-effort */
    }
  }
};

const readEntry = (key: string): CacheEntry | null => {
  const mem = apiCache.get(key);
  if (mem) return mem;
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.localStorage.getItem(LS_PREFIX + key);
    if (!raw) return null;
    const entry = JSON.parse(raw) as CacheEntry;
    apiCache.set(key, entry); // promote to memory
    return entry;
  } catch {
    return null;
  }
};

const storeEntry = (key: string, data: any) => {
  const entry = { data, timestamp: Date.now() };
  apiCache.set(key, entry);
  persistEntry(key, entry);
};

// Invalidate cached GET responses; optionally only those whose endpoint
// contains `match`. Call after mutations that must be visible immediately.
export const invalidateApiCache = (match?: string) => {
  for (const key of Array.from(apiCache.keys())) {
    if (!match || key.includes(match)) apiCache.delete(key);
  }
  if (typeof window === 'undefined') return;
  try {
    for (let i = window.localStorage.length - 1; i >= 0; i--) {
      const k = window.localStorage.key(i);
      if (k && k.startsWith(LS_PREFIX) && (!match || k.includes(match))) {
        window.localStorage.removeItem(k);
      }
    }
  } catch {
    /* best-effort */
  }
};

const fetchAndStore = async (url: string, cacheKey: string, options?: RequestInit, cacheable = true) => {
  const response = await fetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
  });
  if (!response.ok) {
    throw new Error(`API call failed: ${response.status} ${response.statusText}`);
  }
  const data = await response.json();
  if (cacheable) storeEntry(cacheKey, data);
  return data;
};

// Helper function to make API calls with caching
export const apiCall = async (endpoint: string, options?: RequestInit, useCache = true) => {
  const baseUrl = getApiUrl();
  const url = `${baseUrl}${endpoint}`;
  const isGet = !options || !options.method || options.method === 'GET';
  const cacheKey = `${url}-${JSON.stringify(options)}`;

  if (useCache && isGet) {
    const cached = readEntry(cacheKey);
    if (cached) {
      const age = Date.now() - cached.timestamp;
      if (age < FRESH_TTL) {
        return cached.data;
      }
      if (age < STALE_TTL) {
        // Serve stale instantly, refresh in the background
        if (!inflightRevalidations.has(cacheKey)) {
          inflightRevalidations.add(cacheKey);
          fetchAndStore(url, cacheKey, options)
            .catch(() => { /* keep stale data on failure */ })
            .finally(() => inflightRevalidations.delete(cacheKey));
        }
        return cached.data;
      }
    }
  }

  return fetchAndStore(url, cacheKey, options, useCache && isGet);
};

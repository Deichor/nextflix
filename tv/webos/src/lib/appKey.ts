// The packaged app runs from file://, so its requests carry "Origin: null", which a sandboxed
// iframe on any site in the TV's browser sends too. The server only trusts that origin with the
// per-install TV_APP_KEY (deploy/config/server.env, embedded by scripts/build.sh). It goes in the
// query so CORS preflights, keepalive fetches and sendBeacon carry it as well.

/** Adds app_key to a Nextflix server URL (/api/ and /tmdb-img/); other URLs stay as they are. */
export function withAppKey(url: string, key: string): string {
  if (!key || !/^https?:\/\/[^/]+\/(api|tmdb-img)\//i.test(url) || /[?&]app_key=/.test(url)) return url;
  return `${url}${url.indexOf('?') >= 0 ? '&' : '?'}app_key=${encodeURIComponent(key)}`;
}

/** Packaged app only: every fetch and sendBeacon to the server carries the key. */
export function installAppKey(key: string): void {
  if (!key || window.location.protocol !== 'file:') return;
  const fetch0 = window.fetch;
  window.fetch = ((input: RequestInfo | URL, init?: RequestInit) =>
    fetch0.call(window, typeof input === 'string' ? withAppKey(input, key) : input, init)) as typeof window.fetch;
  const beacon0 = navigator.sendBeacon;
  if (beacon0) {
    navigator.sendBeacon = (url: string | URL, data?: BodyInit | null) =>
      beacon0.call(navigator, typeof url === 'string' ? withAppKey(url, key) : url, data);
  }
}

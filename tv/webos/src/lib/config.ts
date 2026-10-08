import { readJson, writeJson } from './storage';

export const DEFAULT_SERVER = 'http://nextflix.local:8252';
// Tried on first run, in this order: the mDNS name (not every TV resolves .local), the PC's LAN IP, this machine
export const SERVER_CANDIDATES = [DEFAULT_SERVER, 'http://192.168.1.221:8252', 'http://127.0.0.1:8252'];
const SERVER_KEY = 'nfx-tv:server';

let server: string | null = null;

/** The Nextflix server base URL ("http://host:8252"), or null before the first setup. */
export function getServer(): string | null {
  if (server) return server;
  // Dev convenience: ?server=http://host:8252 overrides the stored address
  try {
    const fromUrl = new URLSearchParams(window.location.search).get('server');
    if (fromUrl) return (server = normalizeServer(fromUrl));
  } catch {
    /* no URL parameters */
  }
  server = readJson<string>(SERVER_KEY);
  // Opened in the TV's browser from the Nextflix PC (http://tv.nextflix.local or http://<pc>:8252/tv/):
  // the API is on the same host, port 8252
  if (!server && /^https?:$/.test(window.location.protocol) && window.location.port !== '5173') {
    server = `${window.location.protocol}//${window.location.hostname}:8252`;
  }
  return server;
}

export function setServer(url: string): void {
  server = normalizeServer(url);
  writeJson(SERVER_KEY, server);
}

/** "192.168.1.5" -> "http://192.168.1.5:8252"; keeps an explicit scheme, port and path. */
export function normalizeServer(input: string): string {
  let s = input.trim().replace(/\/+$/, '');
  if (!/^https?:\/\//i.test(s)) s = `http://${s}`;
  const m = s.match(/^(https?:\/\/)([^/:]+)(:\d+)?(\/.*)?$/i);
  if (m && !m[3] && m[1].toLowerCase() === 'http://') s = `${m[1]}${m[2]}:8252${m[4] || ''}`;
  return s;
}

/** Running as a packaged app (file://): the server must allow the "null" origin. */
export const isPackaged = () => window.location.protocol === 'file:';

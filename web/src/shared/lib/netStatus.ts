import { getApiUrl } from '@/shared/lib/api';

/** online; offline: the server has no internet; unreachable: the server does not answer */
export type NetState = 'online' | 'offline' | 'unreachable';

const CHECK_TIMEOUT_MS = 6000;

/** Asks the server's /api/health (services.internet). */
export async function checkNet(): Promise<NetState> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), CHECK_TIMEOUT_MS);
  try {
    const res = await fetch(`${getApiUrl()}/api/health`, { cache: 'no-store', signal: ctrl.signal });
    const data = await res.json().catch(() => null);
    return data?.services?.internet === 'offline' ? 'offline' : 'online';
  } catch {
    return 'unreachable';
  } finally {
    clearTimeout(timer);
  }
}

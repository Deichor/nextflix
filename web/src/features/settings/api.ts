import { getApiUrl } from '@/shared/lib/api';

// The server never returns the API key, only whether one is set
export interface IndexerSettings {
  jackett_url: string;
  api_key_set: boolean;
  bitsearch_enabled: boolean;
}

// Omitted fields and an empty api key keep the stored value; a null api key clears it.
export interface IndexerSettingsInput {
  jackett_url?: string;
  jackett_api_key?: string | null;
  bitsearch_enabled?: boolean;
}

export interface IndexerTestResult {
  success: boolean;
  message: string;
}

async function readError(res: Response): Promise<string> {
  try {
    const body = await res.json();
    if (body?.error) return String(body.error);
    if (body?.message) return String(body.message);
  } catch {
    /* non-JSON error body */
  }
  return `Request failed (${res.status})`;
}

function toIndexerSettings(data: Partial<IndexerSettings> | null): IndexerSettings {
  return {
    jackett_url: data?.jackett_url ?? '',
    api_key_set: !!data?.api_key_set,
    bitsearch_enabled: data?.bitsearch_enabled !== false,
  };
}

export async function getIndexerSettings(signal?: AbortSignal): Promise<IndexerSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/indexer`, { signal, cache: 'no-store' });
  if (!res.ok) throw new Error(await readError(res));
  return toIndexerSettings(await res.json());
}

export async function saveIndexerSettings(input: IndexerSettingsInput): Promise<IndexerSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/indexer`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw new Error(await readError(res));
  return toIndexerSettings(await res.json());
}

// Tests the given values (the backend falls back to the stored settings for empty fields)
export async function testIndexer(settings: { jackett_url: string; jackett_api_key: string }): Promise<IndexerTestResult> {
  const res = await fetch(`${getApiUrl()}/api/settings/indexer/test`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  });
  let body: { success?: boolean; message?: string; error?: string } = {};
  try {
    body = await res.json();
  } catch {
    /* empty body */
  }
  const success = res.ok && body.success !== false;
  return {
    success,
    message: success ? body.message || '' : body.error || body.message || `Test failed (${res.status})`,
  };
}

export interface SubtitleSettings {
  opensubtitles: { api_key_set: boolean; username: string; password_set: boolean };
  subdl: { api_key_set: boolean };
  subsource: { api_key_set: boolean };
  turkcealtyazi: { enabled: boolean };
  subf2m: { enabled: boolean };
  yifysubtitles: { enabled: boolean };
  default_langs: string[];
}

// Empty strings keep the stored value, null clears it.
export interface SubtitleSettingsInput {
  opensubtitles?: { api_key?: string | null; username?: string | null; password?: string | null };
  subdl?: { api_key?: string | null };
  subsource?: { api_key?: string | null };
  turkcealtyazi?: { enabled?: boolean };
  subf2m?: { enabled?: boolean };
  yifysubtitles?: { enabled?: boolean };
  default_langs?: string[];
}

export async function getSubtitleSettings(signal?: AbortSignal): Promise<SubtitleSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/subtitles`, { signal, cache: 'no-store' });
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

export async function saveSubtitleSettings(input: SubtitleSettingsInput): Promise<SubtitleSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/subtitles`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

// TorBox debrid. The key is write-only: only api_key_set comes back.
export interface DebridSettings {
  torbox: { api_key_set: boolean; enabled: boolean; prefer: boolean; add_uncached: boolean };
}

// Empty or omitted api_key keeps the stored key, null clears it.
export interface DebridSettingsInput {
  torbox?: { api_key?: string | null; enabled?: boolean; prefer?: boolean; add_uncached?: boolean };
}

export interface DebridTestResult {
  success: boolean;
  message: string;
  planName?: string;
  expiresAt?: string | null;
  active?: boolean;
}

function toDebridSettings(data: Partial<DebridSettings> | null): DebridSettings {
  const tb = data?.torbox;
  return {
    torbox: {
      api_key_set: !!tb?.api_key_set,
      enabled: tb?.enabled !== false,
      prefer: tb?.prefer !== false,
      add_uncached: !!tb?.add_uncached,
    },
  };
}

export async function getDebridSettings(signal?: AbortSignal): Promise<DebridSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/debrid`, { signal, cache: 'no-store' });
  if (!res.ok) throw new Error(await readError(res));
  return toDebridSettings(await res.json());
}

export async function saveDebridSettings(input: DebridSettingsInput): Promise<DebridSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/debrid`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw new Error(await readError(res));
  return toDebridSettings(await res.json());
}

// Checks a key (empty: the stored one) with TorBox's user/me: plan and expiry.
export async function testDebrid(apiKey: string): Promise<DebridTestResult> {
  const res = await fetch(`${getApiUrl()}/api/settings/debrid/test`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ torbox: { api_key: apiKey } }),
  });
  let body: {
    success?: boolean;
    error?: string;
    plan_name?: string;
    premium_expires_at?: string | null;
    active?: boolean;
  } = {};
  try {
    body = await res.json();
  } catch {
    /* empty body */
  }
  const success = res.ok && body.success !== false;
  return {
    success,
    message: success ? '' : body.error || `Test failed (${res.status})`,
    planName: body.plan_name,
    expiresAt: body.premium_expires_at ?? null,
    active: body.active,
  };
}

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
  turkcealtyazi: { enabled: boolean };
  default_langs: string[];
}

// Empty strings keep the stored value, null clears it.
export interface SubtitleSettingsInput {
  opensubtitles?: { api_key?: string | null; username?: string | null; password?: string | null };
  subdl?: { api_key?: string | null };
  turkcealtyazi?: { enabled?: boolean };
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

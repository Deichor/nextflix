import { getApiUrl } from '@/shared/lib/api';

export interface IndexerSettings {
  id?: number;
  jackett_url: string;
  jackett_api_key: string;
  min_seeders?: number;
  preferred_quality?: string;
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

export async function getIndexerSettings(signal?: AbortSignal): Promise<IndexerSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/indexer`, { signal, cache: 'no-store' });
  if (!res.ok) throw new Error(await readError(res));
  const data = await res.json();
  return {
    ...data,
    jackett_url: data?.jackett_url ?? '',
    jackett_api_key: data?.jackett_api_key ?? '',
  };
}

export async function saveIndexerSettings(settings: IndexerSettings): Promise<IndexerSettings> {
  const res = await fetch(`${getApiUrl()}/api/settings/indexer`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  });
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

// Tests the given values (the backend falls back to the stored settings for empty fields)
export async function testIndexer(settings: Pick<IndexerSettings, 'jackett_url' | 'jackett_api_key'>): Promise<IndexerTestResult> {
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
    message: success ? body.message || 'Connected to Jackett' : body.error || body.message || `Test failed (${res.status})`,
  };
}

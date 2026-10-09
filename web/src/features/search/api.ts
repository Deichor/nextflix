import { getApiUrl } from '@/shared/lib/api';
import { profileHeaders } from '@/shared/lib/profile';

export interface SearchAnswer {
  results: any[];
  /** Corrected query whose results were merged in (typo), '' otherwise */
  didYouMean: string;
}

async function fetchJson(path: string, signal?: AbortSignal): Promise<any | null> {
  try {
    // The profile header lets the server rank by the profile's taste
    const res = await fetch(`${getApiUrl()}${path}`, { headers: profileHeaders(), signal });
    return res.ok ? await res.json() : null;
  } catch {
    return null;
  }
}

/** GET /api/tmdb/search: ranked results of `q` (one page) and an optional correction. */
export async function searchTitles(q: string, signal?: AbortSignal): Promise<SearchAnswer | null> {
  const data = await fetchJson(`/api/tmdb/search?q=${encodeURIComponent(q)}`, signal);
  if (!data) return null;
  return { results: Array.isArray(data.results) ? data.results : [], didYouMean: String(data.did_you_mean || '') };
}

export interface Completion {
  title: string;
  mediaType: 'movie' | 'tv';
  id: number;
}

/** GET /api/catalog/suggest: title completions from the server's typo index (fast, no TMDB call). */
export async function suggestTitles(q: string, signal?: AbortSignal): Promise<Completion[]> {
  const data = await fetchJson(`/api/catalog/suggest?q=${encodeURIComponent(q)}&limit=6`, signal);
  const list: any[] = Array.isArray(data?.results) ? data.results : [];
  return list
    .filter(r => r && r.title)
    .map(r => ({ title: String(r.title), mediaType: r.media_type === 'tv' ? 'tv' : 'movie', id: Number(r.id) || 0 }));
}

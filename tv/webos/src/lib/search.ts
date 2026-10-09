import { toTitles, type Title } from './catalog';
import type { SearchFilters } from './discover';
import { getJson } from './http';

export interface SearchAnswer {
  items: Title[];
  /** Corrected query whose results were merged in (typo), '' otherwise */
  didYouMean: string;
}

function answer(data: any): SearchAnswer {
  return { items: toTitles(data?.results, 'movie'), didYouMean: String(data?.did_you_mean || '') };
}

/** GET /api/tmdb/search: ranked results of `q` (one page) and an optional correction. */
export async function searchRanked(q: string, signal?: AbortSignal): Promise<SearchAnswer> {
  return answer(await getJson(`/api/tmdb/search?q=${encodeURIComponent(q)}`, { signal }));
}

/** GET /api/catalog/discover with a query: the ranked results of `q` that pass the filters. */
export async function searchFiltered(f: SearchFilters, q: string, signal?: AbortSignal): Promise<SearchAnswer> {
  const params: string[] = [`q=${encodeURIComponent(q)}`];
  if (f.type !== 'all') params.push(`type=${f.type}`);
  if (f.genres.length > 0) params.push(`genres=${f.genres.join(',')}`);
  if (f.anime) params.push('anime=1');
  return answer(await getJson(`/api/catalog/discover?${params.join('&')}`, { signal }));
}

/** GET /api/catalog/suggest: title completions from the server's typo index. */
export async function suggestTitles(q: string, signal?: AbortSignal): Promise<string[]> {
  const data = await getJson(`/api/catalog/suggest?q=${encodeURIComponent(q)}&limit=5`, { signal });
  const list: any[] = Array.isArray(data?.results) ? data.results : [];
  const out: string[] = [];
  list.forEach(r => {
    const title = String(r?.title || '');
    if (title && out.indexOf(title) < 0) out.push(title);
  });
  return out;
}

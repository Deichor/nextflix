import { getJson, type NfMediaType } from '@/features/catalog/titles';

export type TypeFilter = 'all' | NfMediaType;

export interface SearchFilters {
  type: TypeFilter;
  /** TMDB genre ids, combined with AND */
  genres: number[];
  anime: boolean;
}

export const NO_FILTERS: SearchFilters = { type: 'all', genres: [], anime: false };

// Filter chips: TMDB movie genre ids plus TV-only ones. The server maps them per media type
// (Sci-Fi -> Sci-Fi & Fantasy for TV ...); `types` hides chips that cannot match the type.
export const GENRE_CHIPS: { id: number; types: NfMediaType[] }[] = [
  { id: 28, types: ['movie', 'tv'] },
  { id: 12, types: ['movie', 'tv'] },
  { id: 16, types: ['movie', 'tv'] },
  { id: 35, types: ['movie', 'tv'] },
  { id: 80, types: ['movie', 'tv'] },
  { id: 99, types: ['movie', 'tv'] },
  { id: 18, types: ['movie', 'tv'] },
  { id: 10751, types: ['movie', 'tv'] },
  { id: 14, types: ['movie', 'tv'] },
  { id: 36, types: ['movie'] },
  { id: 27, types: ['movie'] },
  { id: 10762, types: ['tv'] },
  { id: 10402, types: ['movie'] },
  { id: 9648, types: ['movie', 'tv'] },
  { id: 10764, types: ['tv'] },
  { id: 10749, types: ['movie'] },
  { id: 878, types: ['movie', 'tv'] },
  { id: 53, types: ['movie', 'tv'] },
  { id: 10752, types: ['movie', 'tv'] },
  { id: 37, types: ['movie', 'tv'] },
];

export const chipFits = (types: NfMediaType[], type: TypeFilter) => type === 'all' || types.includes(type);

export const hasFilters = (f: SearchFilters) => f.type !== 'all' || f.genres.length > 0 || f.anime;

export function filtersQuery(f: SearchFilters): string {
  const q = new URLSearchParams();
  if (f.type !== 'all') q.set('type', f.type);
  if (f.genres.length > 0) q.set('genres', f.genres.join(','));
  if (f.anime) q.set('anime', '1');
  return q.toString();
}

export function parseFilters(params: URLSearchParams | null): SearchFilters {
  const type = params?.get('type');
  const genres = (params?.get('genres') || '')
    .split(',')
    .map(s => parseInt(s, 10))
    .filter(n => GENRE_CHIPS.some(g => g.id === n));
  return {
    type: type === 'movie' || type === 'tv' ? type : 'all',
    genres: Array.from(new Set(genres)),
    anime: params?.get('anime') === '1',
  };
}

export interface DiscoverPage {
  results: any[];
  page: number;
  totalPages: number;
}

/** GET /api/catalog/discover: browse by filters (no query) or filter the search results of `q`. */
export async function discover(f: SearchFilters, q: string, page: number): Promise<DiscoverPage | null> {
  const params = new URLSearchParams(filtersQuery(f));
  if (q) params.set('q', q);
  params.set('page', String(page));
  // Browse pages are stable (cached like other lists); searches are not
  const data = await getJson<any>(`/api/catalog/discover?${params.toString()}`, { cache: !q });
  if (!data) return null;
  return {
    results: Array.isArray(data.results) ? data.results : [],
    page: Number(data.page) || page,
    totalPages: Number(data.total_pages) || 1,
  };
}

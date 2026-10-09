import { getJson } from './http';
import { getProfileId } from './profile';
import { toTitle, toTitles, type MediaType, type Title } from './catalog';
import type { Locale, MessageKey, TFunction } from './i18n';

// ---- Search filters (same chips and rules as web/src/features/search/filters.ts) ----

export type TypeFilter = 'all' | MediaType;

export interface SearchFilters {
  type: TypeFilter;
  /** TMDB genre ids, combined with AND */
  genres: number[];
  anime: boolean;
}

export const NO_FILTERS: SearchFilters = { type: 'all', genres: [], anime: false };

export const GENRE_CHIPS: { id: number; types: MediaType[] }[] = [
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

export const chipFits = (types: MediaType[], type: TypeFilter) => type === 'all' || types.indexOf(type) >= 0;

export const hasFilters = (f: SearchFilters) => f.type !== 'all' || f.genres.length > 0 || f.anime;

// Ids with a genre.<id> string (the TV-only combined genres name surprise reasons)
const NAMED_GENRES = GENRE_CHIPS.map(g => g.id).concat([10759, 10765, 10768]);

/** Genre name in the UI language, or '' for an unknown id. */
export function genreName(t: TFunction, id: number): string {
  return NAMED_GENRES.indexOf(id) >= 0 ? t(`genre.${id}` as MessageKey) : '';
}

export interface DiscoverPage {
  items: Title[];
  page: number;
  totalPages: number;
}

/** GET /api/catalog/discover: browse by filters (no query) or filter the search results of `q`. */
export async function discover(f: SearchFilters, q: string, page: number, signal?: AbortSignal): Promise<DiscoverPage> {
  const params: string[] = [`page=${page}`];
  if (f.type !== 'all') params.push(`type=${f.type}`);
  if (f.genres.length > 0) params.push(`genres=${f.genres.join(',')}`);
  if (f.anime) params.push('anime=1');
  if (q) params.push(`q=${encodeURIComponent(q)}`);
  const data = await getJson(`/api/catalog/discover?${params.join('&')}`, { signal });
  return {
    items: toTitles(data?.results, 'movie'),
    page: Number(data?.page) || page,
    totalPages: Number(data?.total_pages) || 1,
  };
}

// ---- Surprise me ----

export type SurpriseReasonKind =
  | 'because_loved' | 'because_liked' | 'because_watched' | 'favorite_like' | 'genre' | 'adjacent' | 'wild_card' | 'trending_pick';

export interface SurprisePick {
  title: Title;
  logo: string;
  reason: {
    kind: SurpriseReasonKind;
    title?: string;
    mediaType?: MediaType;
    genreId?: number;
    genreIds?: number[];
    language?: string;
  };
}

// The last picks of this session, sent as exclude= (the server also remembers 30 per profile)
const recentByProfile: Record<number, string[]> = {};
const MEMORY = 30;

export async function surprise(signal?: AbortSignal): Promise<SurprisePick | null> {
  const pid = getProfileId();
  if (!pid) return null;
  const recent = recentByProfile[pid] || (recentByProfile[pid] = []);
  const exclude = recent.length > 0 ? `&exclude=${recent.join(',')}` : '';
  const data = await getJson(`/api/recommendations/surprise?profile_id=${pid}${exclude}`, { signal });
  const title = toTitle(data?.title, 'movie');
  if (!title) return null;
  recent.push(title.key);
  if (recent.length > MEMORY) recent.shift();
  const r = data.reason || {};
  return {
    title,
    logo: data.title.logo_path || '',
    reason: {
      kind: r.kind || 'trending_pick',
      title: r.title || undefined,
      mediaType: r.media_type === 'tv' ? 'tv' : r.media_type === 'movie' ? 'movie' : undefined,
      genreId: typeof r.genre_id === 'number' ? r.genre_id : undefined,
      genreIds: Array.isArray(r.genre_ids) ? r.genre_ids.filter((x: unknown) => typeof x === 'number') : undefined,
      language: typeof r.language === 'string' ? r.language : undefined,
    },
  };
}

const SEED_KEYS: Partial<Record<SurpriseReasonKind, [MessageKey, MessageKey]>> = {
  because_loved: ['surprise.reason.lovedMovie', 'surprise.reason.lovedTv'],
  because_liked: ['surprise.reason.likedMovie', 'surprise.reason.likedTv'],
  because_watched: ['surprise.reason.watchedMovie', 'surprise.reason.watchedTv'],
  favorite_like: ['surprise.reason.favoriteMovie', 'surprise.reason.favoriteTv'],
};

const LANGUAGES = ['ko', 'ja', 'es', 'fr', 'it', 'tr', 'hi', 'de', 'zh', 'da', 'sv', 'pt', 'ru', 'fa'];

/** Turkish locative suffix with vowel harmony: "Bilim kurgu" -> "Bilim kurguda", "Suç" -> "Suçta". */
export function trLocative(word: string): string {
  const lower = word.toLocaleLowerCase('tr-TR');
  let back = true;
  for (let i = lower.length - 1; i >= 0; i--) {
    if ('aıouâû'.indexOf(lower[i]) >= 0) break;
    if ('eiöüî'.indexOf(lower[i]) >= 0) { back = false; break; }
  }
  const hard = 'çfhkpsşt'.indexOf(lower[lower.length - 1] || '-') >= 0;
  return word + (hard ? 't' : 'd') + (back ? 'a' : 'e');
}

/** "Because you loved Dune" / "Bayıldığın Dune filmine benzediği için", "Something completely different – ...". */
export function reasonText(t: TFunction, locale: Locale, pick: SurprisePick): string {
  const { reason } = pick;
  const lower = (s: string) => s.toLocaleLowerCase(locale === 'tr' ? 'tr-TR' : 'en-US');
  // English names genres in lower case; Turkish starts the sentence with the first one
  const lead = (s: string) => (locale === 'tr' ? s.charAt(0).toLocaleUpperCase('tr-TR') + lower(s.slice(1)) : lower(s));
  const seed = SEED_KEYS[reason.kind];
  if (seed && reason.title) return t(seed[reason.mediaType === 'tv' ? 1 : 0], { title: reason.title });
  if (reason.kind === 'genre') {
    const ids = reason.genreIds && reason.genreIds.length > 0 ? reason.genreIds : reason.genreId ? [reason.genreId] : [];
    const names = ids.map(id => genreName(t, id)).filter(Boolean);
    if (names.length >= 2) return t('surprise.reason.genres', { first: lead(names[0]), second: lower(names[1]) });
    if (names.length === 1) return t('surprise.reason.genre', { genre: lead(names[0]) });
  }
  if (reason.kind === 'adjacent' && reason.genreId) {
    const genre = genreName(t, reason.genreId);
    if (genre) return t('surprise.reason.adjacent', { genre: lower(genre), genreIn: trLocative(lead(genre)) });
  }
  if (reason.kind === 'wild_card') {
    if (reason.language === 'ja' && pick.title.genreIds.indexOf(16) >= 0) return t('surprise.reason.wildAnime');
    if (reason.language && LANGUAGES.indexOf(reason.language) >= 0) {
      const language = t(`surprise.lang.${reason.language}` as MessageKey);
      return t(pick.title.type === 'tv' ? 'surprise.reason.wildTv' : 'surprise.reason.wildMovie', { language });
    }
    return t('surprise.reason.wild');
  }
  return t('surprise.reason.trending');
}

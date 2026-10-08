// TMDB images are always routed through /tmdb-img: Next.js rewrites it to the Go server,
// which keeps a disk cache in front of image.tmdb.org (server/internal/modules/images).
export function tmdbImage(path: string | null | undefined, size = 'w500'): string | undefined {
  if (!path) return undefined;
  if (path.startsWith('/tmdb-img/') || path.startsWith('data:')) return path;
  const tmdb = path.match(/^https?:\/\/image\.tmdb\.org(\/t\/p\/.*)$/);
  if (tmdb) return `/tmdb-img${tmdb[1]}`;
  return `/tmdb-img/t/p/${size}${path.startsWith('/') ? path : `/${path}`}`;
}

/** Bare TMDB file path ("/abc.jpg") from a path, a /tmdb-img URL or an image.tmdb.org URL. */
export function tmdbPath(src: string | null | undefined): string | undefined {
  if (!src) return undefined;
  const m = src.match(/\/t\/p\/[^/]+(\/[^/?#]+)$/);
  if (m) return m[1];
  return /^\/[^/]+$/.test(src) ? src : undefined;
}

export type ImageKind = 'poster' | 'backdrop' | 'billboard';

// srcset candidates per kind; the browser picks one from `sizes` and the device pixel ratio.
// "original" is declared wide so normal screens stay on w1280 (it is only picked on HiDPI).
const CANDIDATES: Record<ImageKind, [string, number][]> = {
  poster: [['w185', 185], ['w342', 342], ['w500', 500]],
  backdrop: [['w300', 300], ['w500', 500], ['w780', 780]],
  billboard: [['w780', 780], ['w1280', 1280], ['original', 2560]],
};

// Tiny image shown blurred until the real one has loaded
const PLACEHOLDER: Record<ImageKind, string> = { poster: 'w92', backdrop: 'w92', billboard: 'w300' };

export function tmdbSrcSet(path: string, kind: ImageKind): string {
  return CANDIDATES[kind].map(([size, w]) => `${tmdbImage(path, size)} ${w}w`).join(', ');
}

/** Fallback src for browsers without srcset: the middle candidate. */
export function tmdbDefaultSrc(path: string, kind: ImageKind): string {
  return tmdbImage(path, CANDIDATES[kind][1][0]) as string;
}

export function tmdbPlaceholder(path: string, kind: ImageKind): string {
  return tmdbImage(path, PLACEHOLDER[kind]) as string;
}

export const BILLBOARD_SIZES = '(min-width: 1280px) 1280px, 100vw';

type TmdbLogo = { iso_639_1?: string | null; file_path?: string };

/** Title logo in `lang`, else English, else a language-less one (images/warmer.go picks the same). */
export function pickLogo<T extends TmdbLogo>(logos: T[], lang: string): T | undefined {
  return logos.find(l => l.iso_639_1 === lang) || logos.find(l => l.iso_639_1 === 'en') || logos.find(l => !l.iso_639_1);
}

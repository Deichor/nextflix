// TMDB image URLs through the server's disk-cached proxy. Pure, tested in tmdbImg.test.mjs.

/**
 * `path` may be a bare TMDB path ("/abc.jpg"), an image.tmdb.org URL or the server's own relative
 * "/tmdb-img/t/p/<size>/abc.jpg"; all become `<server>/tmdb-img/t/p/<size>/abc.jpg` in the asked size.
 */
export function tmdbImgUrl(server: string, path: string | null | undefined, size: string): string {
  if (!path) return '';
  const m = path.match(/\/t\/p\/[^/]+(\/[^/?#]+)$/);
  const file = m ? m[1] : path.startsWith('/') ? path : `/${path}`;
  return `${server}/tmdb-img/t/p/${size}${file}`;
}

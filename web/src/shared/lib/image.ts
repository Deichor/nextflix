// TMDB images are always routed through the /tmdb-img proxy (see next.config.js rewrites).
export function tmdbImage(path: string | null | undefined, size = 'w500'): string | undefined {
  if (!path) return undefined;
  if (path.startsWith('/tmdb-img/') || path.startsWith('data:')) return path;
  const tmdb = path.match(/^https?:\/\/image\.tmdb\.org(\/t\/p\/.*)$/);
  if (tmdb) return `/tmdb-img${tmdb[1]}`;
  return `/tmdb-img/t/p/${size}${path.startsWith('/') ? path : `/${path}`}`;
}

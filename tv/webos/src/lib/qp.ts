// Header-less GETs (no CORS preflight): the language and profile go in the query. Pure, tested in qp.test.mjs.

/** Adds x_lang and, when the path has none, profile_id (skipped without a profile). */
export function withQueryParams(path: string, locale: string, pid: number | null | undefined): string {
  const add: string[] = [];
  if (!/[?&]x_lang=/.test(path)) add.push(`x_lang=${encodeURIComponent(locale)}`);
  if (pid && !/[?&]profile_id=/.test(path)) add.push(`profile_id=${pid}`);
  if (add.length === 0) return path;
  return `${path}${path.indexOf('?') >= 0 ? '&' : '?'}${add.join('&')}`;
}

/**
 * Year of a TMDB "YYYY-MM-DD" date. Read from the text, since new Date() parses it as UTC
 * midnight and getFullYear() then gives the previous year west of UTC on January 1st.
 */
export function releaseYear(date?: string | null): number | undefined {
  if (!date) return undefined;
  const y = parseInt(date.slice(0, 4), 10);
  return Number.isFinite(y) && y > 1800 ? y : undefined;
}

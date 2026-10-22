// When an upcoming episode airs: the weekday within the next 6 days, else day + month.

const DAY_MS = 24 * 60 * 60 * 1000;

/** YYYY-MM-DD as a local date (`new Date('YYYY-MM-DD')` is UTC midnight, a day early west of UTC). */
export function parseLocalDay(ymd: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(ymd);
  if (!m) return null;
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return Number.isNaN(d.getTime()) ? null : d;
}

/** `intl` is a BCP 47 tag ('en-US', 'tr-TR'); '' when the date does not parse. */
export function formatAirDay(intl: string, ymd: string, now = new Date()): string {
  const d = parseLocalDay(ymd);
  if (!d) return '';
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  // Rounded: a DST change makes a day 23 or 25 hours long
  const days = Math.round((d.getTime() - today.getTime()) / DAY_MS);
  const opts: Intl.DateTimeFormatOptions = days >= 0 && days <= 6 ? { weekday: 'long' } : { day: 'numeric', month: 'long' };
  try {
    return d.toLocaleDateString(intl, opts);
  } catch {
    return ymd;
  }
}

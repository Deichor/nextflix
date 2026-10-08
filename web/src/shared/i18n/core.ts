import { en, type Message, type MessageKey, type Messages } from '@/shared/i18n/en';
import { tr } from '@/shared/i18n/tr';
import { getActiveProfile } from '@/shared/lib/profile';

export type Locale = 'en' | 'tr';
export type Vars = Record<string, string | number>;

export const LOCALES: readonly Locale[] = ['en', 'tr'];
export const DEFAULT_LOCALE: Locale = 'en';
export const LANG_STORAGE_KEY = 'hfx-lang';
export const LANG_EVENT = 'hfx-lang-change';

const DICTS: Record<Locale, Messages> = { en, tr };
const INTL: Record<Locale, string> = { en: 'en-US', tr: 'tr-TR' };

export const isLocale = (v: unknown): v is Locale => v === 'en' || v === 'tr';
export const intlLocale = (locale: Locale) => INTL[locale];

const pluralRules: Partial<Record<Locale, Intl.PluralRules>> = {};
function plural(locale: Locale, n: number): Intl.LDMLPluralRule {
  pluralRules[locale] ??= new Intl.PluralRules(INTL[locale]);
  return pluralRules[locale]!.select(n);
}

function pick(locale: Locale, msg: Message, vars?: Vars): string {
  if (typeof msg === 'string') return msg;
  const count = Number(vars?.count ?? 0);
  return msg[plural(locale, count)] ?? msg.other;
}

const PLACEHOLDER = /\{(\w+)\}/g;

/** Message for `key` in `locale` (falls back to English, then the key) with {placeholders} filled in. */
export function translate(locale: Locale, key: MessageKey, vars?: Vars): string {
  const msg = DICTS[locale][key] ?? en[key];
  if (msg === undefined) return key;
  const text = pick(locale, msg, vars);
  if (!vars) return text;
  return text.replace(PLACEHOLDER, (m, name: string) => {
    const v = vars[name];
    if (v === undefined) return m;
    return typeof v === 'number' ? formatNumber(locale, v) : v;
  });
}

/** Like translate, but placeholders may be any value (e.g. React nodes); returns the pieces in order. */
export function translateParts<T>(locale: Locale, key: MessageKey, vars: Record<string, T>): (string | T)[] {
  const msg = DICTS[locale][key] ?? en[key];
  const count = vars.count;
  const text = pick(locale, msg, typeof count === 'number' ? { count } : undefined);
  const out: (string | T)[] = [];
  let last = 0;
  for (const m of text.matchAll(PLACEHOLDER)) {
    if (!(m[1] in vars)) continue;
    if (m.index! > last) out.push(text.slice(last, m.index));
    out.push(vars[m[1]]);
    last = m.index! + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** Active profile language -> localStorage 'hfx-lang' -> navigator.language -> 'en'. */
export function detectLocale(): Locale {
  if (typeof window === 'undefined') return DEFAULT_LOCALE;
  const fromProfile = getActiveProfile()?.language;
  if (isLocale(fromProfile)) return fromProfile;
  try {
    const stored = window.localStorage.getItem(LANG_STORAGE_KEY);
    if (isLocale(stored)) return stored;
  } catch {
    /* storage unavailable */
  }
  return navigator.language?.toLowerCase().startsWith('tr') ? 'tr' : DEFAULT_LOCALE;
}

/** Device-level language, used when no profile (or no profile language) is active. */
export function setStoredLocale(locale: Locale) {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(LANG_STORAGE_KEY, locale);
  } catch {
    /* storage unavailable */
  }
  window.dispatchEvent(new Event(LANG_EVENT));
}

// Format helpers (Intl based)

type DateInput = Date | string | number;

function toDate(d: DateInput): Date {
  if (d instanceof Date) return d;
  // Bare "YYYY-MM-DD" is read as local midnight, not UTC
  if (typeof d === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(d)) return new Date(`${d}T00:00:00`);
  return new Date(d);
}

export function formatNumber(locale: Locale, n: number, opts?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(INTL[locale], opts).format(n);
}

/** Localized date; returns '' for invalid input. */
export function formatDate(locale: Locale, date: DateInput, opts?: Intl.DateTimeFormatOptions): string {
  const d = toDate(date);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleDateString(INTL[locale], opts ?? { year: 'numeric', month: 'long', day: 'numeric' });
}

/** "Oct 24" / "24 Eki", with the year when it is not the current one. */
export function formatShortDate(locale: Locale, date: DateInput): string {
  const d = toDate(date);
  if (Number.isNaN(d.getTime())) return '';
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return formatDate(locale, d, { month: 'short', day: 'numeric', ...(sameYear ? {} : { year: 'numeric' }) });
}

/** "Just now", "3 minutes ago" / "3 dakika önce", "Yesterday", then a short date after a week. */
export function formatRelativeTime(locale: Locale, date: DateInput, now = Date.now()): string {
  const d = toDate(date);
  if (Number.isNaN(d.getTime())) return '';
  const s = Math.max(0, Math.floor((now - d.getTime()) / 1000));
  if (s < 60) return translate(locale, 'time.justNow');
  const rtf = new Intl.RelativeTimeFormat(INTL[locale], { numeric: 'always' });
  const m = Math.floor(s / 60);
  if (m < 60) return rtf.format(-m, 'minute');
  const h = Math.floor(m / 60);
  if (h < 24) return rtf.format(-h, 'hour');
  const days = Math.floor(h / 24);
  if (days === 1) return translate(locale, 'time.yesterday');
  if (days < 7) return rtf.format(-days, 'day');
  return formatDate(locale, d, { month: 'short', day: 'numeric' });
}

/** Runtime in minutes as "1h 36m" / "1 sa 36 dk". */
export function formatDuration(locale: Locale, minutes: number): string {
  const total = Math.max(0, Math.round(minutes));
  const h = Math.floor(total / 60);
  const m = total % 60;
  if (h > 0 && m > 0) return translate(locale, 'format.durationHM', { h, m });
  if (h > 0) return translate(locale, 'format.durationH', { h });
  return translate(locale, 'format.durationM', { m });
}

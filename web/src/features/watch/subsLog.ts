// Subtitle diagnostics for the playback log: fields of the `subs`, `subs-none` and `subdelay` events.
// Pure, tested in subsLog.test.mjs; same as tv/webos/src/player/subsLog.ts (keep both in sync).
// The server keeps string values to 40 chars of [A-Za-z0-9._:/+-] and events to 16 fields, so values are made safe here.

export type SubsWhy = 'open' | 'poll' | 'user' | 'auto' | 'fallback';
type Val = number | string | boolean;
export type SubsFields = Record<string, Val | undefined>;

interface Cue {
  start: number;
  end: number;
  text: string;
}

interface Item {
  id: string;
  source: string;
  language: string;
  release?: string;
  label?: string;
}

/** One answer of a subtitle file as shown */
export interface SubsShown {
  id: string;
  cues: Cue[];
  partial: boolean;
  sync: string;
}

const MAX_TOKEN = 40;
// Header value when the server sent none (same origin) or the browser hides it (cross-origin, not exposed)
export const SYNC_NONE = '-';
export const SYNC_HIDDEN = 'hidden';

const round1 = (n: number) => Math.round(n * 10) / 10;
const round2 = (n: number) => Math.round(n * 100) / 100;

/** A log-safe token: ASCII (Turkish letters folded), other runs become "_", at most max chars, "-" when empty. */
export function logToken(s: string | null | undefined, max = MAX_TOKEN): string {
  let out = String(s || '').replace(/ı/g, 'i');
  if (typeof out.normalize === 'function') out = out.normalize('NFD');
  out = out
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^A-Za-z0-9._:/+-]+/g, '_')
    .replace(/^_+/, '')
    .slice(0, max)
    .replace(/_+$/, '');
  return out || '-';
}

/**
 * A subtitle id as logged: 8 hex chars of its FNV-1a hash (32 bit, over its UTF-16 code units; the ids are ASCII).
 * Cut ids would not do: every subtitle timed for one file starts with the same chars.
 */
export function idHash(id: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) h = Math.imul(h ^ id.charCodeAt(i), 0x01000193);
  return ('0000000' + (h >>> 0).toString(16)).slice(-8);
}

/** X-Subtitle-Sync as logged: the raw value, or "hidden" when a cross-origin answer cannot show it ("?" would reach the log as "_"). */
export function syncValue(header: string | null, crossOrigin: boolean): string {
  if (header && header.trim()) return logToken(header.trim(), 16);
  return crossOrigin ? SYNC_HIDDEN : SYNC_NONE;
}

/** Start of the first and the last cue (0.1 s) */
export function cueSpan(cues: Cue[]): { first?: number; last?: number } {
  if (cues.length === 0) return {};
  return { first: round1(cues[0].start), last: round1(cues[cues.length - 1].start) };
}

// Moved cues this close to the viewer (s before, after) are logged at once
export const NEAR_BEFORE_S = 5;
export const NEAR_AFTER_S = 120;

/** How an answer moved the cues of another (cueShift) */
export interface CueMoves {
  /** median start change of the cues matched by text (0.1 s) */
  shift?: number;
  /** cues that moved by 0.1 s or more, and cues matched */
  moved: number;
  matched: number;
  /** of the moved cues: first and last start in the new answer (0.1 s), median and largest change (0.01 s) */
  mfrom?: number;
  mto?: number;
  mshift?: number;
  mmax?: number;
  /** moved cues starting (before or after) from NEAR_BEFORE_S before `at` to NEAR_AFTER_S after it */
  near?: number;
  /** start change of the cue showing at `at` (0 when it stayed); none when no cue shows or its line is gone */
  here?: number;
}

/** The first cue showing at t (cues sorted by start) */
function cueAt(cues: Cue[], t: number): Cue | undefined {
  let lo = 0;
  let hi = cues.length - 1;
  let last = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (cues[mid].start <= t) { last = mid; lo = mid + 1; } else hi = mid - 1;
  }
  let found: Cue | undefined;
  for (let i = last; i >= 0 && i > last - 12; i--) if (cues[i].end > t) found = cues[i];
  return found;
}

/**
 * How a new answer moved the cues of the last one, matched by text (each text's first cue): median start change,
 * how many moved by 0.1 s or more and how many matched; with moved cues also where they are and how far they moved.
 * `at`: the file time shown (the subtitle clock), for `near` and `here`.
 */
export function cueShift(prev: Cue[], next: Cue[], at?: number): CueMoves {
  const was = new Map<string, number>();
  for (const c of prev) if (!was.has(c.text)) was.set(c.text, c.start);
  const seen = new Set<string>();
  const d: number[] = [];
  const m: number[] = [];
  let mfrom = 0;
  let mto = 0;
  let mmax = 0;
  let near = 0;
  const isNear = (s: number) => at !== undefined && s >= at - NEAR_BEFORE_S && s <= at + NEAR_AFTER_S;
  for (const c of next) {
    if (seen.has(c.text)) continue;
    seen.add(c.text);
    const s = was.get(c.text);
    if (s === undefined) continue;
    const x = c.start - s;
    d.push(x);
    if (Math.abs(x) < 0.1) continue;
    if (m.length === 0) mfrom = c.start;
    mto = c.start;
    m.push(x);
    if (Math.abs(x) > Math.abs(mmax)) mmax = x;
    if (isNear(s) || isNear(c.start)) near++;
  }
  if (d.length === 0) return { moved: 0, matched: 0 };
  d.sort((a, b) => a - b);
  const out: CueMoves = { shift: round1(d[d.length >> 1]), moved: m.length, matched: d.length };
  if (m.length === 0) return out;
  m.sort((a, b) => a - b);
  out.mfrom = round1(mfrom);
  out.mto = round1(mto);
  out.mshift = round2(m[m.length >> 1]);
  out.mmax = round2(mmax);
  if (at === undefined) return out;
  out.near = near;
  // The line on screen, matched to its nearest start in the new answer (its text may come more than once)
  const shown = cueAt(prev, at);
  let best: number | undefined;
  if (shown) {
    for (const c of next) {
      if (c.text === shown.text && (best === undefined || Math.abs(c.start - shown.start) < Math.abs(best))) best = c.start - shown.start;
    }
  }
  if (best !== undefined) out.here = round2(best);
  return out;
}

// Polls that only moved cues (a live timing step) away from the viewer are logged at most this often
export const SUBS_MOVE_LOG_MS = 60000;

/** Whether an answer is worth a `subs` event, and how it moved the cues (only when some moved) */
export interface SubsChange {
  log: boolean;
  moves?: CueMoves;
}

/**
 * Whether an answer is worth a `subs` event, against the last logged one (`sinceMs` ago): another subtitle,
 * the first cues, partial or sync changed, cues replaced, or moved (timed again; at once near `at`, the file
 * time shown, else at most every SUBS_MOVE_LOG_MS, the moves then add up since the last event). A live track
 * that only grew is not.
 */
export function subsChange(prev: SubsShown | null, next: SubsShown, sinceMs = Infinity, at?: number): SubsChange {
  if (!prev || prev.id !== next.id) return { log: true };
  if (prev.cues === next.cues) return { log: prev.partial !== next.partial || prev.sync !== next.sync };
  const s = cueShift(prev.cues, next.cues, at);
  const moves = s.moved > 0 ? s : undefined;
  // Most old cues gone: the server served another subtitle
  const replaced = prev.cues.length > 0 && s.matched < Math.min(prev.cues.length, next.cues.length) / 2;
  if (prev.partial !== next.partial || prev.sync !== next.sync || replaced) return moves ? { log: true, moves } : { log: true };
  if (moves) return sinceMs >= SUBS_MOVE_LOG_MS || (moves.near || 0) > 0 ? { log: true, moves } : { log: false };
  return { log: prev.cues.length === 0 && next.cues.length > 0 };
}

/** Fields of a `subs` event; the list item's fields only when a subtitle starts (a poll keeps them) */
export function subsFields(item: Item | undefined, shown: SubsShown, why: SubsWhy, delay: number, moves?: CueMoves): SubsFields {
  const it = why === 'poll' ? undefined : item;
  return {
    id: idHash(shown.id),
    cues: shown.cues.length,
    partial: shown.partial,
    lang: it ? logToken(it.language, 8) : undefined,
    provider: it ? logToken(it.source, 16) : undefined,
    name: it ? logToken(it.release || it.label) : undefined,
    sync: shown.sync,
    why,
    ...cueSpan(shown.cues),
    subdelay: delay,
    shift: moves?.shift,
    moved: moves?.moved,
    mfrom: moves?.mfrom,
    mto: moves?.mto,
    mshift: moves?.mshift,
    mmax: moves?.mmax,
    near: moves?.near,
    here: moves?.here,
  };
}

/** Fields of a `subs-none` event: why (off, open, auto, failed, missing), the profile language and what the list has */
export function noneFields(why: string, lang: string, items: Item[] | null, extra?: SubsFields): SubsFields {
  const langs: string[] = [];
  for (const s of items || []) {
    const l = logToken(s.language, 8);
    if (l !== '-' && langs.indexOf(l) < 0) langs.push(l);
  }
  return { why, lang: logToken(lang, 8), n: (items || []).length, have: langs.length ? logToken(langs.join('+')) : undefined, ...extra };
}

export const DELAY_SETTLE_MS = 3000;

/** Debounced subtitle delay changes: the settled value once DELAY_SETTLE_MS passed after the last change. */
export class DelayLog {
  private logged = 0;
  private value = 0;
  private changedAt = -1;

  /** Current delay (seconds) */
  get current(): number {
    return this.value;
  }

  /** The viewer changed the delay */
  change(v: number, now: number) {
    this.value = v;
    this.changedAt = now;
  }

  /** The settled change to log, or null (nothing pending, still changing, or back where it was). force: now (exit). */
  take(now: number, force = false): { subdelay: number; from: number } | null {
    if (this.changedAt < 0 || (!force && now - this.changedAt < DELAY_SETTLE_MS)) return null;
    this.changedAt = -1;
    if (this.value === this.logged) return null;
    const out = { subdelay: this.value, from: this.logged };
    this.logged = this.value;
    return out;
  }

  /** The player set the delay itself (another file): a pending change is returned first, v is the new baseline */
  reset(v: number, now: number): { subdelay: number; from: number } | null {
    const pending = this.take(now, true);
    this.logged = this.value = v;
    return pending;
  }
}

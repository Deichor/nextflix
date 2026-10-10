// Subtitle diagnostics for the playback log: fields of the `subs`, `subs-none` and `subdelay` events.
// Pure, tested in subsLog.test.mjs; same as tv/webos/src/player/subsLog.ts (keep both in sync).
// The server keeps string values to 40 chars of [A-Za-z0-9._:/+-], so they are made safe here.

export type SubsWhy = 'open' | 'poll' | 'user' | 'auto' | 'fallback';
type Val = number | string | boolean;
export type SubsFields = Record<string, Val | undefined>;

interface Cue {
  start: number;
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

/**
 * How a new answer moved the cues of the last one, matched by text (each text's first cue):
 * median start change (0.1 s), how many moved by 0.1 s or more, and how many matched.
 */
export function cueShift(prev: Cue[], next: Cue[]): { shift?: number; moved: number; matched: number } {
  const at = new Map<string, number>();
  for (const c of prev) if (!at.has(c.text)) at.set(c.text, c.start);
  const seen = new Set<string>();
  const d: number[] = [];
  let moved = 0;
  for (const c of next) {
    if (seen.has(c.text)) continue;
    seen.add(c.text);
    const s = at.get(c.text);
    if (s === undefined) continue;
    const x = c.start - s;
    d.push(x);
    if (Math.abs(x) >= 0.1) moved++;
  }
  if (d.length === 0) return { moved, matched: 0 };
  d.sort((a, b) => a - b);
  return { shift: round1(d[d.length >> 1]), moved, matched: d.length };
}

// Polls that only moved cues (a live timing step) are logged at most this often
export const SUBS_MOVE_LOG_MS = 60000;

/**
 * Whether an answer is worth a `subs` event, against the last logged one (`sinceMs` ago): another subtitle,
 * the first cues, partial or sync changed, cues replaced, or moved (timed again; at most every SUBS_MOVE_LOG_MS,
 * the shift then adds up since the last event). A live track that only grew is not.
 */
export function subsChange(prev: SubsShown | null, next: SubsShown, sinceMs = Infinity): { log: boolean; shift?: number; moved?: number } {
  if (!prev || prev.id !== next.id) return { log: true };
  if (prev.cues === next.cues) return { log: prev.partial !== next.partial || prev.sync !== next.sync };
  const s = cueShift(prev.cues, next.cues);
  const out = { shift: s.moved > 0 ? s.shift : undefined, moved: s.moved > 0 ? s.moved : undefined };
  // Most old cues gone: the server served another subtitle
  const replaced = prev.cues.length > 0 && s.matched < Math.min(prev.cues.length, next.cues.length) / 2;
  if (prev.partial !== next.partial || prev.sync !== next.sync || replaced) return { log: true, ...out };
  if (s.moved > 0) return sinceMs >= SUBS_MOVE_LOG_MS ? { log: true, ...out } : { log: false };
  return { log: prev.cues.length === 0 && next.cues.length > 0 };
}

/** Fields of a `subs` event */
export function subsFields(
  item: Item | undefined,
  shown: SubsShown,
  why: SubsWhy,
  delay: number,
  change?: { shift?: number; moved?: number },
): SubsFields {
  return {
    id: logToken(shown.id, 24),
    cues: shown.cues.length,
    partial: shown.partial,
    lang: item ? logToken(item.language, 8) : undefined,
    provider: item ? logToken(item.source, 16) : undefined,
    name: item ? logToken(item.release || item.label) : undefined,
    sync: shown.sync,
    why,
    ...cueSpan(shown.cues),
    subdelay: delay,
    shift: change?.shift,
    moved: change?.moved,
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

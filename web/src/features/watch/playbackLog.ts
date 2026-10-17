import { getApiUrl } from '@/shared/lib/api';

// Player diagnostics for the server's playback log (POST /api/playback-log, see docs/API.md): a few
// events sent every 30 s, on close and on pagehide; read with scripts/playback-log.sh. Clients: "web" (subtitles,
// device, restarts and media errors), "web-a4k" (upscaler).

type Val = number | string | boolean;
export type LogNote = (k: string, x?: Record<string, Val | undefined>) => void;

export interface PlaybackLogCtx {
  type: string;
  tmdb: number;
  s?: number;
  e?: number;
  title?: string;
  /** "<hash8>:<index>" of the playing file */
  src?: string;
  dur?: number;
}

// Short device label for the log (values are cut at 40 chars): "iPhone-iOS18.1-Safari", "Android14-Chrome130"
export function deviceToken(ua = typeof navigator !== 'undefined' ? navigator.userAgent : ''): string {
  const ios = ua.match(/(iPhone|iPad|iPod).*? OS (\d+)_(\d+)/);
  const android = ua.match(/Android (\d+(?:\.\d+)?)/);
  const os = ios ? `${ios[1]}-iOS${ios[2]}.${ios[3]}`
    : android ? `Android${android[1]}`
    : /Macintosh/.test(ua) ? (navigator.maxTouchPoints > 1 ? 'iPad-mac' : 'Mac')
    : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : 'other';
  const b = ua.match(/(SamsungBrowser|EdgA?|OPR|Firefox|FxiOS|CriOS|Chrome)\/(\d+)/);
  const safari = ua.match(/Version\/(\d+)[.\d]* .*Safari/);
  return `${os}-${b ? b[1] + b[2] : safari ? 'Safari' + safari[1] : 'other'}`;
}

const FLUSH_MS = 30000;
const MAX_EVENTS = 100;

export class PlaybackNotes {
  private readonly sid = Math.random().toString(36).slice(2, 10).padEnd(8, '0');
  private seq = 0;
  private events: { t: number; k: string; p: number; x?: Record<string, Val> }[] = [];
  private dropped = 0;
  private readonly timer: ReturnType<typeof setInterval>;
  private readonly client: string;
  private readonly ctx: () => PlaybackLogCtx;
  private readonly position: () => number;

  constructor(client: string, ctx: () => PlaybackLogCtx, position: () => number) {
    this.client = client;
    this.ctx = ctx;
    this.position = position;
    this.timer = setInterval(() => this.flush(), FLUSH_MS);
  }

  readonly note: LogNote = (k, x) => {
    if (this.events.length >= MAX_EVENTS) {
      this.dropped++;
      return;
    }
    let out: Record<string, Val> | undefined;
    for (const key in x) {
      const v = x[key];
      if (v !== undefined) (out ??= {})[key] = v;
    }
    this.events.push({ t: Date.now(), k, p: Math.round(this.position() * 100) / 100, x: out });
  };

  close() {
    clearInterval(this.timer);
    this.flush(true);
  }

  /** Sends what is pending now, surviving a page close (pagehide) */
  send() {
    this.flush(true);
  }

  private flush(keepalive = false) {
    if (this.events.length === 0) return;
    const body = JSON.stringify({
      sid: this.sid, seq: this.seq++, client: this.client, ctx: this.ctx(), dropped: this.dropped, events: this.events,
    });
    this.events = [];
    this.dropped = 0;
    // text/plain: no CORS preflight for the body itself
    fetch(`${getApiUrl()}/api/playback-log`, { method: 'POST', headers: { 'Content-Type': 'text/plain' }, body, keepalive })
      .catch(() => {});
  }
}

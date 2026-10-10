import { getApiUrl } from '@/shared/lib/api';

// Upscaler diagnostics (GPU, shader compile time, frame times, step downs) for the server's playback
// log (POST /api/playback-log, client "web-a4k"); read with scripts/playback-log.sh.

type Val = number | string | boolean;
export type UpscaleNote = (k: string, x?: Record<string, Val>) => void;

export interface UpscaleLogCtx {
  type: string;
  tmdb: number;
  s?: number;
  e?: number;
  title?: string;
}

const FLUSH_MS = 30000;
const MAX_EVENTS = 100;

export class UpscaleLog {
  private readonly sid = Math.random().toString(36).slice(2, 10).padEnd(8, '0');
  private seq = 0;
  private events: { t: number; k: string; p: number; x?: Record<string, Val> }[] = [];
  private dropped = 0;
  private readonly timer: ReturnType<typeof setInterval>;

  constructor(private readonly ctx: UpscaleLogCtx, private readonly position: () => number) {
    this.timer = setInterval(() => this.flush(), FLUSH_MS);
  }

  readonly note: UpscaleNote = (k, x) => {
    if (this.events.length >= MAX_EVENTS) {
      this.dropped++;
      return;
    }
    this.events.push({ t: Date.now(), k, p: Math.round(this.position() * 100) / 100, x });
  };

  close() {
    clearInterval(this.timer);
    this.flush(true);
  }

  private flush(keepalive = false) {
    if (this.events.length === 0) return;
    const body = JSON.stringify({
      sid: this.sid, seq: this.seq++, client: 'web-a4k', ctx: this.ctx, dropped: this.dropped, events: this.events,
    });
    this.events = [];
    this.dropped = 0;
    // text/plain: no CORS preflight for the body itself
    fetch(`${getApiUrl()}/api/playback-log`, { method: 'POST', headers: { 'Content-Type': 'text/plain' }, body, keepalive })
      .catch(() => {});
  }
}

import { PlaybackNotes } from '@/features/watch/playbackLog';

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

export class UpscaleLog extends PlaybackNotes {
  constructor(ctx: UpscaleLogCtx, position: () => number) {
    super('web-a4k', () => ctx, position);
  }
}

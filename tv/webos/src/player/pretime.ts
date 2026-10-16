// Next-episode pre-timing (pure, tested in pretime.test.mjs; same steps as the web's
// web/src/features/watch/pretime.ts and the Android TV app): once the next episode's prefetch found its source,
// its subtitle list is asked and the subtitle this player would show there is timed by the server before the
// episode opens (GET /api/subtitles/file?pre=1), so its first answer is timed instead of the subtitle as it is.

// The prefetched source is looked up this often...
export const PRETIME_POLL_MS = 20000;
// ...this many times (3 minutes: a prefetch of a fresh torrent takes up to a minute or two)
export const PRETIME_TRIES = 9;

export interface PretimeSteps<S, I> {
  /** The next episode's source once the server found it (resolve?cached=1), else null */
  cachedSource: () => Promise<S | null>;
  /** Its subtitle list */
  subtitles: (source: S) => Promise<I[]>;
  /** The subtitle the player would show (its default pick), null for none */
  pick: (items: I[]) => string | null;
  /** Asks the server to time it */
  pretime: (id: string) => Promise<unknown>;
  wait: (ms: number) => Promise<void>;
  cancelled: () => boolean;
}

/** Runs the pre-timing; resolves with the subtitle id it asked to time (null: none). */
export async function pretimeNext<S, I>(s: PretimeSteps<S, I>): Promise<string | null> {
  let source: S | null = null;
  for (let i = 0; i < PRETIME_TRIES && !source; i++) {
    if (i > 0) await s.wait(PRETIME_POLL_MS);
    if (s.cancelled()) return null;
    source = await s.cachedSource();
  }
  if (!source || s.cancelled()) return null;
  const items = await s.subtitles(source);
  if (s.cancelled()) return null;
  const id = s.pick(items);
  if (!id) return null;
  await s.pretime(id);
  return id;
}

// When an MSE stream (mseStream.ts) may end. Pure, tested in mseEnd.test.mjs.

export interface LaneFill {
  /** The stream feeding the SourceBuffer is complete */
  done: boolean;
  /** Bytes received but not appended yet */
  queued: number;
  updating: boolean;
}

/** Every stream is complete and appended: nothing more goes into the SourceBuffers */
export const allDataIn = (lanes: LaneFill[]) => lanes.every(l => l.done && l.queued === 0);

/**
 * endOfStream() is due. Read from the MediaSource state, not a flag: a remove() or append after
 * endOfStream() reopens it, and the element fires `ended` only after another endOfStream().
 */
export function endOfStreamDue(readyState: string, lanes: LaneFill[]): boolean {
  return readyState === 'open' && allDataIn(lanes) && lanes.every(l => !l.updating);
}

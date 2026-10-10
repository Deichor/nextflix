// The position a progress save stores, or null to skip it. Once the item was saved finished, later
// saves (unmount, the 10 s timer, pagehide) are skipped: the server keeps the last save, and the
// position where the end credits began can be below the finished threshold.
export function savePosition(saveAt: number, duration: number, finished: boolean, savedFinished: boolean): number | null {
  if (duration <= 0 || (savedFinished && !finished)) return null;
  const pos = finished ? duration : saveAt;
  return pos < 10 ? null : pos;
}

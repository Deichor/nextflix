// What a profile editor sends. Pure, tested in profileEdit.test.mjs.

/**
 * The fields of `to` that differ from `from` (the values the editor opened with). The server
 * updates only the fields sent, so a change made elsewhere meanwhile is not undone.
 */
export function changedFields<T extends object>(from: T, to: T): Partial<T> {
  const out: Partial<T> = {};
  (Object.keys(to) as (keyof T)[]).forEach(k => {
    if (to[k] !== from[k]) out[k] = to[k];
  });
  return out;
}

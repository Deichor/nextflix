// Loading a screen's extra rows together. Pure, tested in rowLoad.test.mjs.

export interface LoadedRows<K extends string, T> {
  rows: Record<K, T[]>;
  /** A row failed (shown empty): the answer must not be cached */
  failed: boolean;
}

/** Waits for every row; a failed one becomes empty instead of failing the others. */
export async function loadRows<K extends string, T>(loads: Record<K, Promise<T[]>>): Promise<LoadedRows<K, T>> {
  const keys = Object.keys(loads) as K[];
  let failed = false;
  const lists = await Promise.all(keys.map(k => loads[k].catch(() => {
    failed = true;
    return [] as T[];
  })));
  const rows = {} as Record<K, T[]>;
  keys.forEach((k, i) => { rows[k] = lists[i]; });
  return { rows, failed };
}

/** useQuery `keep`: only an answer without failed rows is cached */
export const allRowsLoaded = (d: { failed: boolean }) => !d.failed;

const RETRY_DELAYS = [1000, 3000, 8000];

/** Wait before retrying a failed "load more" page; null once it should wait for the user. */
export function loadMoreRetryDelay(fails: number): number | null {
  if (fails < 1) return null;
  return RETRY_DELAYS[fails - 1] ?? null;
}

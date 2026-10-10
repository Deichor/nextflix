/** Reads a /api/ratings answer; an error status throws so the caller keeps what it has. */
export async function parseRatings<T>(res: Response): Promise<T[]> {
  if (!res.ok) throw new Error(String(res.status));
  const data: unknown = await res.json();
  return Array.isArray(data) ? (data as T[]) : [];
}

import type { Notification } from '@/features/notifications/types';

const TV_TYPES = new Set(['tmdb_upcoming_tv', 'tmdb_now_airing_tv', 'new_episodes']);

export function notificationMediaType(n: Pick<Notification, 'type'>): 'movie' | 'tv' {
  return TV_TYPES.has(n.type) ? 'tv' : 'movie';
}

// Detail page for the n-th TMDB title of a notification, or null when it has none
export function notificationHref(n: Notification, index = 0): string | null {
  const id = n.tmdb_ids?.[index] ?? n.media_details?.[index]?.id;
  if (!id || id <= 0) return null;
  return `/tmdb-movie/${id}?type=${notificationMediaType(n)}`;
}

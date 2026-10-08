import type { Notification } from '@/features/notifications/types';
import { tmdbImage } from '@/shared/lib/image';
import { formatRelativeTime, formatShortDate, translate, type Locale, type MessageKey } from '@/shared/i18n';

// Re-sizes a TMDB image (full TMDB URL, proxied URL or bare path) to /tmdb-img/t/p/<size><path>
export function tmdbImageAt(url: string | undefined, size: string): string | undefined {
  if (!url) return undefined;
  const sized = url.match(/\/t\/p\/[^/]+(\/[^/?#]+)$/);
  if (sized) return tmdbImage(sized[1], size);
  if (/^[a-z]+:/i.test(url)) return undefined;
  return tmdbImage(url, size);
}

// 16:9 thumbnail: backdrop first, poster as fallback
export function notificationThumb(n: Notification, size = 'w300'): string | undefined {
  const media = n.media_details?.[0];
  return (
    tmdbImageAt(n.backdrop_url || media?.backdrop_url, size) ||
    tmdbImageAt(n.poster_url || media?.poster_url, 'w185')
  );
}

export function notificationTitle(n: Notification): string {
  return n.tmdb_titles?.[0] || n.media_details?.[0]?.title || n.title;
}

// One short line in Netflix wording ("New arrival", "Coming Oct 24", "Now playing" ...)
export function notificationMessage(n: Notification, locale: Locale): string {
  const extra = Math.max(n.tmdb_ids?.length ?? 0, n.media_details?.length ?? 0) - 1;
  let key: MessageKey;
  let date = '';
  switch (n.type) {
    case 'tmdb_coming_soon':
      date = n.release_date ? formatShortDate(locale, n.release_date) : '';
      key = date ? 'notifications.msg.comingOn' : 'notifications.msg.comingSoon';
      break;
    case 'tmdb_upcoming':
      key = 'notifications.msg.comingToTheaters';
      break;
    case 'tmdb_now_playing':
      key = 'notifications.msg.nowPlaying';
      break;
    case 'tmdb_trending':
      key = 'notifications.msg.trending';
      break;
    case 'tmdb_upcoming_tv':
      key = 'notifications.msg.newEpisodesToday';
      break;
    case 'tmdb_now_airing_tv':
      key = 'notifications.msg.newEpisodes';
      break;
    default:
      key = 'notifications.msg.newArrival';
  }
  const line = translate(locale, key, { date });
  return extra > 0 ? translate(locale, 'notifications.msg.more', { line, count: extra }) : line;
}

export function relativeTime(unix: number, locale: Locale, now = Date.now()): string {
  return formatRelativeTime(locale, unix * 1000, now);
}

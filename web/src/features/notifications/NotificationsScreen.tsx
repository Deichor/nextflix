"use client";

import React, { useEffect, useMemo, useState } from 'react';
import { Check, Star } from 'lucide-react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import { useNavigate } from '@/shared/navigation/useNavigate';
import NetflixPage, { EmptyState } from '@/features/shell/PageShell';
import BrowseHeader, { NfSelect } from '@/features/catalog/components/BrowseHeader';
import type { Notification } from '@/features/notifications/types';
import { notificationHref } from '@/features/notifications/links';
import { notificationMessage, notificationTitle, relativeTime } from '@/features/notifications/display';
import {
  markAllNotificationsRead,
  markNotificationRead,
  markNotificationsSeen,
  useNotifications,
} from '@/features/notifications/store';
import NotificationThumb from '@/features/notifications/components/NotificationThumb';
import { NotificationListSkeleton } from '@/features/notifications/NotificationsLoading';
import { useLocale, useT, type MessageKey } from '@/shared/i18n';

type Filter = 'all' | 'unread';

const DAY = 86400;

function groupLabel(unix: number, now: number): MessageKey {
  const startOfToday = new Date(now);
  startOfToday.setHours(0, 0, 0, 0);
  const today = startOfToday.getTime() / 1000;
  if (unix >= today) return 'notifications.group.today';
  if (unix >= today - DAY) return 'notifications.group.yesterday';
  if (unix >= today - 6 * DAY) return 'notifications.group.thisWeek';
  return 'notifications.group.earlier';
}

function NotificationRow({ n, onOpen }: { n: Notification; onOpen: (n: Notification, index?: number) => void }) {
  const t = useT();
  const locale = useLocale();
  const extras = (n.media_details ?? []).slice(1, 5);
  return (
    <li className="relative">
      {!n.read && <span className="absolute inset-y-0 left-0 w-[3px] bg-[#E50914]" aria-hidden />}
      <div className={`flex gap-4 py-4 pl-4 pr-2 transition-colors hover:bg-white/[0.05] md:gap-5 md:pl-5 ${n.read ? '' : 'bg-white/[0.03]'}`}>
        <button onClick={() => onOpen(n)} className="shrink-0 focus:outline-none" aria-label={notificationTitle(n)} tabIndex={-1}>
          <NotificationThumb n={n} className="w-32 transition-opacity hover:opacity-80 sm:w-44 md:w-56" />
        </button>
        <div className="min-w-0 flex-1">
          <button onClick={() => onOpen(n)} className="block w-full text-left focus:outline-none focus-visible:underline">
            <p className="text-xs font-medium uppercase tracking-wide text-[#b3b3b3]">{notificationMessage(n, locale)}</p>
            <h3 className={`mt-0.5 truncate text-base font-semibold md:text-lg ${n.read ? 'text-[#d2d2d2]' : 'text-white'}`}>
              {notificationTitle(n)}
            </h3>
            {!!n.overview && (
              <p className="mt-1 hidden text-sm leading-snug text-[#b3b3b3] line-clamp-2 sm:block">{n.overview}</p>
            )}
          </button>
          {extras.length > 0 && (
            <p className="mt-1.5 truncate text-xs text-[#808080]">
              {t('notifications.also')}{' '}
              {extras.map((m, i) => (
                <React.Fragment key={`${m.id}-${i}`}>
                  {i > 0 && ', '}
                  <button onClick={() => onOpen(n, i + 1)} className="text-[#b3b3b3] hover:text-white hover:underline">
                    {m.title}
                  </button>
                </React.Fragment>
              ))}
            </p>
          )}
          <div className="mt-1.5 flex items-center gap-2 text-xs text-[#808080]">
            <span>{relativeTime(n.timestamp, locale)}</span>
            {(n.rating ?? 0) > 0 && (
              <>
                <span aria-hidden>·</span>
                <span className="inline-flex items-center gap-1">
                  <Star className="h-3 w-3 fill-current" />
                  {n.rating!.toFixed(1)}
                </span>
              </>
            )}
            {!n.read && <span className="ml-1 h-1.5 w-1.5 rounded-full bg-[#E50914]" aria-label={t('common.unread')} />}
          </div>
        </div>
      </div>
    </li>
  );
}

export default function NotificationsScreen() {
  const t = useT();
  usePageTitle(t('notifications.title'));
  const navigate = useNavigate();
  const { items, loaded, unread } = useNotifications(true);
  const [filter, setFilter] = useState<Filter>('all');

  // Viewing the page clears the bell badge for what is listed
  useEffect(() => {
    if (items.length > 0) void markNotificationsSeen();
  }, [items]);

  const groups = useMemo(() => {
    const now = Date.now();
    const list = filter === 'unread' ? items.filter(n => !n.read) : items;
    const out: { label: MessageKey; items: Notification[] }[] = [];
    for (const n of list) {
      const label = groupLabel(n.timestamp, now);
      const last = out[out.length - 1];
      if (last?.label === label) last.items.push(n);
      else out.push({ label, items: [n] });
    }
    return out;
  }, [items, filter]);

  const open = (n: Notification, index = 0) => {
    void markNotificationRead(n.id);
    const href = notificationHref(n, index);
    if (href) navigate.push(href);
  };

  return (
    <NetflixPage>
      <BrowseHeader
        title={t('notifications.title')}
        right={
          items.length > 0 ? (
            <>
              <NfSelect
                label={t('notifications.show')}
                value={filter}
                onChange={v => setFilter(v as Filter)}
                options={[
                  { value: 'all', label: t('common.all') },
                  { value: 'unread', label: t('common.unread') },
                ]}
              />
              <button
                onClick={() => void markAllNotificationsRead()}
                disabled={unread === 0}
                className="inline-flex items-center gap-1.5 border border-white/40 px-3 py-1 text-sm font-semibold text-white transition-colors hover:border-white hover:bg-white/10 disabled:cursor-default disabled:opacity-40 disabled:hover:border-white/40 disabled:hover:bg-transparent"
              >
                <Check className="h-4 w-4" />
                {t('notifications.markAllRead')}
              </button>
            </>
          ) : undefined
        }
      />
      {unread > 0 && <p className="px-4 text-sm text-[#b3b3b3] md:px-12">{t('notifications.unreadCount', { count: unread })}</p>}

      <div className="max-w-5xl px-4 pt-6 md:px-12">
        {!loaded && items.length === 0 ? (
          <NotificationListSkeleton />
        ) : items.length === 0 ? (
          <EmptyState
            title={t('notifications.empty')}
            text={t('notifications.emptyText')}
            action={
              <button
                onClick={() => navigate.push('/')}
                className="rounded bg-white px-6 py-2 text-sm font-bold text-black transition-colors hover:bg-white/80"
              >
                {t('notifications.browseTitles')}
              </button>
            }
          />
        ) : groups.length === 0 ? (
          <EmptyState title={t('notifications.allCaughtUp')} text={t('notifications.noUnread')} />
        ) : (
          groups.map(g => (
            <section key={g.label} className="mb-8">
              <h2 className="mb-2 text-sm font-semibold text-[#e5e5e5] md:text-base">{t(g.label)}</h2>
              <ul className="divide-y divide-white/[0.06] border-y border-white/[0.06]">
                {g.items.map(n => (
                  <NotificationRow key={n.id} n={n} onOpen={open} />
                ))}
              </ul>
            </section>
          ))
        )}
      </div>
    </NetflixPage>
  );
}

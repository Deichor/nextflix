"use client";

import React from 'react';
import NetflixPage from '@/features/shell/PageShell';
import { useT } from '@/shared/i18n';

export function NotificationListSkeleton({ rows = 6 }: { rows?: number }) {
  const t = useT();
  return (
    <ul
      className="divide-y divide-white/[0.06] border-y border-white/[0.06]"
      aria-busy="true"
      aria-label={t('notifications.loading')}
    >
      {Array.from({ length: rows }).map((_, i) => (
        <li key={i} className="flex gap-4 py-4 pl-4 pr-2 md:gap-5 md:pl-5">
          <div className="aspect-video w-32 shrink-0 animate-pulse rounded-sm bg-[#2f2f2f] sm:w-44 md:w-56" />
          <div className="flex-1 space-y-2 pt-1">
            <div className="h-3 w-24 animate-pulse rounded bg-[#2f2f2f]" />
            <div className="h-4 w-2/3 animate-pulse rounded bg-[#2f2f2f]" />
            <div className="hidden h-3 w-full animate-pulse rounded bg-[#262626] sm:block" />
          </div>
        </li>
      ))}
    </ul>
  );
}

export default function NotificationsLoading() {
  return (
    <NetflixPage>
      <div className="px-4 pt-20 md:px-12 md:pt-24">
        <div className="mb-8 h-8 w-56 animate-pulse rounded bg-[#2f2f2f] md:h-10" />
        <div className="max-w-5xl">
          <NotificationListSkeleton />
        </div>
      </div>
    </NetflixPage>
  );
}

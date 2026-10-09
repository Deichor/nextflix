"use client";

import React, { useEffect } from "react";
import { Bell } from "lucide-react";
import type { Notification } from "@/features/notifications/types";
import { useNavigate } from "@/shared/navigation/useNavigate";
import { notificationHref } from "@/features/notifications/links";
import { notificationMessage, notificationTitle, relativeTime } from "@/features/notifications/display";
import {
  markNotificationRead,
  markNotificationsSeen,
  refreshNotifications,
  useNotifications,
} from "@/features/notifications/store";
import NotificationThumb from "@/features/notifications/components/NotificationThumb";
import { useLocale, useT } from "@/shared/i18n";

const MAX_ROWS = 6;

interface NotificationDropdownProps {
  isOpen: boolean;
  onClose: () => void;
}

// Netflix-style panel under the bell: caret, compact rows, "See all" footer
const NotificationDropdown: React.FC<NotificationDropdownProps> = ({ isOpen, onClose }) => {
  const t = useT();
  const locale = useLocale();
  const navigate = useNavigate();
  const { items, loaded } = useNotifications();

  // Opening the panel loads the list (if stale) and clears the badge for what is shown
  useEffect(() => {
    if (!isOpen) return;
    let cancelled = false;
    refreshNotifications().then(() => {
      if (!cancelled) void markNotificationsSeen();
    });
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    document.addEventListener("keydown", onKey);
    return () => {
      cancelled = true;
      document.removeEventListener("keydown", onKey);
    };
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  const open = (n: Notification) => {
    void markNotificationRead(n.id);
    navigate.push(notificationHref(n) ?? "/notifications");
    onClose();
  };

  const seeAll = () => {
    navigate.push("/notifications");
    onClose();
  };

  const rows = items.slice(0, MAX_ROWS);

  return (
    <div
      className="fixed left-2 right-2 top-[calc(3.75rem+env(safe-area-inset-top))] z-[60] sm:absolute sm:left-auto sm:right-0 sm:top-full sm:mt-4 sm:w-[400px]"
      role="menu"
      aria-label={t("notifications.title")}
    >
      <span className="absolute -top-[7px] right-[2px] hidden h-0 w-0 border-x-[7px] border-b-[7px] border-x-transparent border-b-[#e5e5e5] sm:block md:right-[5px]" />
      <div className="overflow-hidden border-t-2 border-t-[#e5e5e5] bg-[#141414]/[0.97] shadow-[0_8px_24px_rgba(0,0,0,0.6)] ring-1 ring-white/10">
        {!loaded && rows.length === 0 ? (
          <div className="divide-y divide-white/[0.06]">
            {Array.from({ length: 3 }).map((_, i) => (
              <div key={i} className="flex gap-3 px-3 py-3">
                <div className="aspect-video w-28 shrink-0 animate-pulse rounded-sm bg-[#2f2f2f]" />
                <div className="flex-1 space-y-2 pt-1">
                  <div className="h-3 w-3/4 animate-pulse rounded bg-[#2f2f2f]" />
                  <div className="h-3 w-1/2 animate-pulse rounded bg-[#2f2f2f]" />
                </div>
              </div>
            ))}
          </div>
        ) : rows.length === 0 ? (
          <div className="flex flex-col items-center gap-2 px-6 py-10 text-center">
            <Bell className="h-6 w-6 text-[#6d6d6d]" />
            <p className="text-sm text-[#b3b3b3]">{t("notifications.empty")}</p>
          </div>
        ) : (
          <ul className="max-h-[min(70vh,480px)] divide-y divide-white/[0.06] overflow-y-auto">
            {rows.map(n => (
              <li key={n.id}>
                <button
                  onClick={() => open(n)}
                  role="menuitem"
                  className={`relative flex w-full gap-3 px-3 py-3 text-left transition-colors hover:bg-white/[0.08] focus:bg-white/[0.08] focus:outline-none ${n.read ? "" : "bg-white/[0.03]"}`}
                >
                  {!n.read && <span className="absolute inset-y-0 left-0 w-[3px] bg-[#E50914]" aria-hidden />}
                  <NotificationThumb n={n} className="w-28" />
                  <div className="min-w-0 flex-1 py-0.5">
                    <p className={`truncate text-sm ${n.read ? "text-[#b3b3b3]" : "font-semibold text-white"}`}>
                      {notificationTitle(n)}
                    </p>
                    <p className="truncate text-[13px] text-[#b3b3b3]">{notificationMessage(n, locale)}</p>
                    <p className="mt-1 text-xs text-[#808080]">{relativeTime(n.timestamp, locale)}</p>
                  </div>
                  {!n.read && <span className="sr-only">{t("common.unread")}</span>}
                </button>
              </li>
            ))}
          </ul>
        )}
        <button
          onClick={seeAll}
          className="block w-full border-t border-white/10 px-4 py-3 text-center text-sm font-medium text-white transition-colors hover:bg-white/[0.08]"
        >
          {t("notifications.seeAll")}
        </button>
      </div>
    </div>
  );
};

export default NotificationDropdown;

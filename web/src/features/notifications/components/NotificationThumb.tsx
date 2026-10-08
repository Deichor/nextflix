"use client";

import React, { useState } from "react";
import { Film } from "lucide-react";
import type { Notification } from "@/features/notifications/types";
import { notificationThumb } from "@/features/notifications/display";

// 16:9 backdrop thumbnail with a neutral fallback
export default function NotificationThumb({ n, className = "", size = "w300" }: { n: Notification; className?: string; size?: string }) {
  const [failed, setFailed] = useState(false);
  const src = failed ? undefined : notificationThumb(n, size);
  return (
    <div className={`relative aspect-video shrink-0 overflow-hidden rounded-sm bg-[#2f2f2f] ${className}`}>
      {src ? (
        <img src={src} alt="" loading="lazy" onError={() => setFailed(true)} className="h-full w-full object-cover" />
      ) : (
        <div className="flex h-full w-full items-center justify-center">
          <Film className="h-5 w-5 text-[#6d6d6d]" />
        </div>
      )}
    </div>
  );
}

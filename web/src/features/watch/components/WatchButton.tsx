"use client";

import React, { useEffect, useState } from 'react';
import { CalendarClock, Play } from 'lucide-react';
import { formatDuration, translate, useFormat, useT, type Locale } from '@/shared/i18n';
import { Availability, AvailabilityTarget, getAvailability } from '@/features/watch/api';

interface WatchButtonProps {
  onClick: () => void;
  /** Shows "Resume" instead of "Play" */
  resume?: boolean;
  /** Extra text after the label, e.g. "S1:E2" */
  detail?: string;
  /** 0-100; renders a thin progress bar under the button when > 0 */
  progress?: number;
  /** e.g. "34m left", shown next to the progress bar */
  remainingText?: string;
  disabled?: boolean;
  /** Not released / in theaters: replaces Play with a disabled "Coming ..." button */
  availability?: Availability | null;
  className?: string;
}

/** Text for a title that cannot be streamed yet, or null when it can. */
export function useUnavailableLabel(a: Availability | null | undefined): string | null {
  const t = useT();
  const f = useFormat();
  if (!a) return null;
  if (a.status === 'not_released') {
    const date = a.release_date || a.digital_date;
    return date ? t('availability.comingOn', { date: f.date(date) }) : t('home.comingSoon');
  }
  if (a.status === 'in_theaters') {
    return a.digital_date ? t('availability.inTheatersUntil', { date: f.date(a.digital_date) }) : t('availability.inTheaters');
  }
  return null;
}

/** Availability of what Play would start; only asked for one target at a time (detail, billboard, preview). */
export function useAvailability(target: AvailabilityTarget | null): Availability | null {
  const key = target ? `${target.media_type}-${target.tmdb_id}-${target.season ?? 0}-${target.episode ?? 0}` : '';
  const [state, setState] = useState<{ key: string; value: Availability | null }>({ key: '', value: null });
  useEffect(() => {
    if (!target) return;
    let cancelled = false;
    getAvailability(target).then(a => { if (!cancelled) setState({ key, value: a }); });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return state.key === key ? state.value : null;
}

// Netflix-style white Play / Resume button
export default function WatchButton({ onClick, resume, detail, progress = 0, remainingText, disabled, availability, className = '' }: WatchButtonProps) {
  const t = useT();
  const unavailable = useUnavailableLabel(availability);

  if (unavailable) {
    return (
      <div className={`inline-flex flex-col gap-2 ${className}`}>
        <button
          disabled
          aria-disabled
          className="flex cursor-default items-center justify-center gap-2 rounded bg-white/25 px-5 py-2 text-sm font-bold text-white/85 md:px-7 md:py-2.5 md:text-lg"
        >
          <CalendarClock className="h-5 w-5 shrink-0 md:h-6 md:w-6" />
          <span className="text-left leading-tight">{unavailable}</span>
        </button>
      </div>
    );
  }

  return (
    <div className={`inline-flex flex-col gap-2 ${className}`}>
      <button
        onClick={onClick}
        disabled={disabled}
        className="flex items-center justify-center gap-2 px-6 py-2 md:px-8 md:py-2.5 bg-white text-black font-bold rounded hover:bg-white/75 transition-colors text-sm md:text-lg disabled:opacity-60"
      >
        <Play className="w-5 h-5 md:w-6 md:h-6 fill-current" />
        <span>{t(resume ? 'player.resume' : 'player.play')}</span>
        {!!detail && <span className="font-semibold">{detail}</span>}
      </button>
      {progress > 0 && (
        <div className="flex items-center gap-2">
          <div className="h-[3px] w-24 md:w-32 bg-[#5a5a5a] rounded-sm overflow-hidden">
            <div className="h-full bg-[#E50914]" style={{ width: `${Math.min(100, progress)}%` }} />
          </div>
          {!!remainingText && <span className="text-xs text-[#b3b3b3] whitespace-nowrap">{remainingText}</span>}
        </div>
      )}
    </div>
  );
}

export function formatRemaining(position: number, duration: number, locale: Locale = 'en'): string {
  const minutes = Math.max(1, Math.round(Math.max(0, duration - position) / 60));
  return translate(locale, 'player.timeLeft', { time: formatDuration(locale, minutes) });
}

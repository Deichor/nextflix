"use client";

import React, { useState } from 'react';
import { Clapperboard } from 'lucide-react';
import { useT } from '@/shared/i18n';

interface CinemaRecordingButtonProps {
  /** Plays the film with cinema=1 once the viewer confirmed */
  onPlay: () => void;
  className?: string;
}

// "Watch the cinema recording anyway" under the in-theaters banner: a first click shows a
// one-line quality warning, the second one plays.
export default function CinemaRecordingButton({ onPlay, className = '' }: CinemaRecordingButtonProps) {
  const t = useT();
  const [asking, setAsking] = useState(false);

  if (!asking) {
    return (
      <button
        onClick={() => setAsking(true)}
        className={`inline-flex items-center gap-2 rounded bg-[#6d6d6eb3] px-4 py-2 text-sm font-semibold text-white transition-colors hover:bg-[#6d6d6e66] md:text-base ${className}`}
      >
        <Clapperboard aria-hidden className="h-4 w-4 shrink-0 md:h-5 md:w-5" />
        <span>{t('cinema.watchAnyway')}</span>
      </button>
    );
  }

  return (
    <div className={`flex flex-wrap items-center gap-x-3 gap-y-2 ${className}`} role="group">
      <span className="text-sm text-white/85 md:text-base">{t('cinema.confirm')}</span>
      <button
        autoFocus
        onClick={onPlay}
        className="rounded bg-white px-4 py-1.5 text-sm font-bold text-black transition-colors hover:bg-white/75 md:text-base"
      >
        {t('cinema.play')}
      </button>
      <button
        onClick={() => setAsking(false)}
        className="rounded bg-[#6d6d6eb3] px-4 py-1.5 text-sm font-semibold text-white transition-colors hover:bg-[#6d6d6e66] md:text-base"
      >
        {t('common.cancel')}
      </button>
    </div>
  );
}

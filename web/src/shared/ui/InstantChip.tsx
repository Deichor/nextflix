"use client";

import React, { useState } from 'react';
import { useT } from '@/shared/i18n';
import { InstantItem, useInstant } from '@/shared/lib/instant';
import { useRowNear } from '@/shared/ui/TmdbImage';

interface InstantChipProps {
  item: InstantItem | null;
  /** card: a small ⚡ in the image corner (needs a positioned parent); label: inline "⚡ Instant" */
  variant?: 'card' | 'label';
  className?: string;
  /** Corner of the card chip (left where the card has its own top-right button) */
  corner?: 'left' | 'right';
}

// Shows ⚡ when the title has a release TorBox has cached (it plays at once). Asks only once
// the card's row is near the viewport; renders nothing while unknown or not cached.
export default function InstantChip({ item, variant = 'card', className = '', corner = 'right' }: InstantChipProps) {
  const t = useT();
  const [el, setEl] = useState<HTMLSpanElement | null>(null);
  const near = useRowNear(el);
  const instant = useInstant(item, variant === 'label' || near) === true;
  const label = t('instant.label');

  if (variant === 'label') {
    if (!instant) return null;
    return (
      <span
        title={label}
        className={`inline-flex items-center gap-1 whitespace-nowrap text-[#ffd338] ${className}`}
      >
        <span aria-hidden>⚡</span>
        {t('instant.short')}
      </span>
    );
  }

  return (
    <span ref={setEl} className={`pointer-events-none absolute top-1.5 z-10 ${corner === 'left' ? 'left-1.5' : 'right-1.5'} ${className}`}>
      {instant && (
        <span
          role="img"
          aria-label={label}
          title={label}
          className="pointer-events-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-black/70 px-1 text-[11px] leading-none text-[#ffd338] shadow-[0_1px_3px_rgba(0,0,0,0.6)] ring-1 ring-white/15"
        >
          ⚡
        </span>
      )}
    </span>
  );
}

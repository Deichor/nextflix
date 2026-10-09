"use client";

import React, { useCallback, useState } from 'react';
import { Shuffle } from 'lucide-react';
import { useT } from '@/shared/i18n';
import SurpriseModal from '@/features/surprise/SurpriseModal';
import type { SurprisePick } from '@/features/surprise/api';

interface SurpriseButtonProps {
  className?: string;
  onPlay?: (pick: SurprisePick) => void;
}

export default function SurpriseButton({ className = '', onPlay }: SurpriseButtonProps) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const close = useCallback(() => setOpen(false), []);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={`flex items-center gap-2 whitespace-nowrap font-semibold text-white transition-colors ${className}`}
      >
        <Shuffle className="h-5 w-5" />
        <span>{t('surprise.button')}</span>
      </button>
      {open && <SurpriseModal onClose={close} onPlay={onPlay} />}
    </>
  );
}

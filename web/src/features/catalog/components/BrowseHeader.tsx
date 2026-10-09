"use client";

import React, { useEffect, useRef, useState } from 'react';
import { ChevronDown } from 'lucide-react';
import { useT } from '@/shared/i18n';

export interface HeaderOption {
  value: string;
  label: string;
}

interface BrowseHeaderProps {
  title: string;
  /** Shown as "Title > Genre" when a genre is picked */
  selectedLabel?: string;
  options?: HeaderOption[];
  value?: string;
  onChange?: (value: string) => void;
  /** Overlay on top of a billboard (absolute) or a normal block */
  overlay?: boolean;
  /** Extra controls on the right (e.g. a sort dropdown) */
  right?: React.ReactNode;
  dropdownLabel?: string;
}

// Netflix sub-header: big page title + "Genres" dropdown
export default function BrowseHeader({
  title, selectedLabel, options, value, onChange, overlay, right, dropdownLabel,
}: BrowseHeaderProps) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div
      className={`${overlay ? `absolute inset-x-0 top-[calc(3.75rem+env(safe-area-inset-top))] md:top-[70px] ${open ? 'z-[60]' : 'z-30'}` : 'relative pt-20 md:pt-24'} flex flex-wrap items-center gap-x-6 gap-y-3 px-4 pb-2 md:px-12`}
    >
      <h1 className="flex items-center gap-3 text-2xl font-bold text-white drop-shadow md:text-[2.4rem]">
        {selectedLabel ? (
          <>
            <button onClick={() => onChange?.('')} className="text-[#b3b3b3] transition-colors hover:text-white">{title}</button>
            <span className="text-[#b3b3b3]">›</span>
            <span>{selectedLabel}</span>
          </>
        ) : (
          title
        )}
      </h1>

      {!!options && options.length > 0 && (
        <div ref={ref} className="relative">
          <button
            onClick={() => setOpen(o => !o)}
            aria-expanded={open}
            className="flex items-center gap-6 border border-white/80 bg-black px-2.5 py-1 text-sm font-semibold text-white transition-colors hover:bg-white/10 pointer-coarse:min-h-11"
          >
            {dropdownLabel || t('catalog.genres')}
            <ChevronDown className={`h-4 w-4 transition-transform ${open ? 'rotate-180' : ''}`} />
          </button>
          {open && (
            // Phones: bottom sheet over a dimmed page
            <button aria-hidden tabIndex={-1} onClick={() => setOpen(false)} className="fixed inset-0 z-50 cursor-default bg-black/60 sm:hidden" />
          )}
          {open && (
            <div className="fixed inset-x-0 bottom-0 z-50 grid max-h-[70vh] grid-cols-2 gap-x-4 overflow-y-auto rounded-t-xl border-t border-white/15 bg-[#181818] px-5 pb-[calc(1rem+env(safe-area-inset-bottom))] pt-4 sm:absolute sm:inset-x-auto sm:bottom-auto sm:left-0 sm:top-full sm:mt-0.5 sm:max-h-[60vh] sm:w-[min(92vw,520px)] sm:grid-cols-3 sm:rounded-none sm:border sm:bg-black/95 sm:px-4 sm:py-3">
              {value && (
                <button
                  onClick={() => { onChange?.(''); setOpen(false); }}
                  className="col-span-full mb-1 text-left text-sm font-semibold text-white hover:underline pointer-coarse:min-h-11"
                >
                  {t('catalog.allOf', { title })}
                </button>
              )}
              {options.map(o => (
                <button
                  key={o.value}
                  onClick={() => { onChange?.(o.value); setOpen(false); }}
                  className={`py-1 text-left text-sm hover:underline pointer-coarse:min-h-11 ${o.value === value ? 'font-bold text-white' : 'text-[#e5e5e5]'}`}
                >
                  {o.label}
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {!!right && <div className="ml-auto flex items-center gap-3">{right}</div>}
    </div>
  );
}

interface SelectProps {
  value: string;
  onChange: (v: string) => void;
  options: HeaderOption[];
  label?: string;
}

// Small Netflix-styled native select (sort orders etc.)
export function NfSelect({ value, onChange, options, label }: SelectProps) {
  return (
    <label className="relative inline-flex items-center">
      {!!label && <span className="sr-only">{label}</span>}
      <select
        value={value}
        onChange={e => onChange(e.target.value)}
        className="cursor-pointer appearance-none border border-white/80 bg-black py-1 pl-2.5 pr-8 text-sm font-semibold text-white focus:outline-none focus-visible:ring-2 focus-visible:ring-white pointer-coarse:min-h-11"
      >
        {options.map(o => (
          <option key={o.value} value={o.value} className="bg-black">{o.label}</option>
        ))}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2 h-4 w-4 text-white" />
    </label>
  );
}

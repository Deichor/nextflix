"use client";

import React from 'react';
import TitleCard, { CardVariant } from '@/features/catalog/components/TitleCard';
import type { NfTitle } from '@/features/catalog/titles';

interface TitleGridProps {
  items: NfTitle[];
  variant?: CardVariant;
  loading?: boolean;
  onPlay?: (item: NfTitle) => void;
  /** Optional 0-100 progress per item key */
  progress?: Record<string, number>;
  className?: string;
}

// Poster (or 16:9) grid used by My List, Search and filtered genre views
export default function TitleGrid({ items, variant = 'poster', loading, onPlay, progress, className = '' }: TitleGridProps) {
  const cols = variant === 'poster'
    ? 'grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7'
    : 'grid-cols-2 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-5';

  return (
    <div className={`grid gap-x-1.5 gap-y-6 md:gap-x-2 md:gap-y-10 ${cols} ${className}`}>
      {loading && items.length === 0
        ? Array.from({ length: 14 }).map((_, i) => (
            <div key={i} className={`animate-pulse rounded-md bg-[#2a2a2a] ${variant === 'poster' ? 'aspect-[2/3]' : 'aspect-video'}`} />
          ))
        : items.map(item => (
            <TitleCard key={item.key} item={item} variant={variant} onPlay={onPlay} progress={progress?.[item.key]} />
          ))}
    </div>
  );
}

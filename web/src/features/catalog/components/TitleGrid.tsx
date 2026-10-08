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
  // Column width as srcset `sizes`
  const sizes = variant === 'poster'
    ? '(min-width: 1280px) 14vw, (min-width: 1024px) 16vw, (min-width: 768px) 20vw, (min-width: 640px) 25vw, 33vw'
    : '(min-width: 1280px) 20vw, (min-width: 768px) 25vw, (min-width: 640px) 33vw, 50vw';

  return (
    <div className={`nf-card-grid grid ${cols} ${className}`}>
      {loading && items.length === 0
        ? Array.from({ length: 14 }).map((_, i) => (
            <div key={i} className={`relative overflow-hidden rounded-md ${variant === 'poster' ? 'aspect-[2/3]' : 'aspect-video'}`}>
              <div className="nf-img" />
            </div>
          ))
        : items.map(item => (
            <TitleCard key={item.key} item={item} variant={variant} onPlay={onPlay} progress={progress?.[item.key]} sizes={sizes} />
          ))}
    </div>
  );
}

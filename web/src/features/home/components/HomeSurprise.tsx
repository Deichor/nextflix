"use client";

import React from 'react';
import SurpriseButton from '@/features/surprise/SurpriseButton';
import { useTitleActions } from '@/features/home/components/TitleActions';

// "Surprise me" on the billboard; Play starts the pick in this page's player
export default function HomeSurprise({ className = '' }: { className?: string }) {
  const { play } = useTitleActions();
  return (
    <SurpriseButton
      className={className}
      onPlay={p => play({
        id: p.id,
        media_type: p.mediaType,
        title: p.title,
        overview: p.overview,
        poster_path: p.posterPath,
        backdrop_path: p.backdropPath,
        date: p.date,
        vote_average: p.rating,
        genre_ids: p.genreIds,
        original_language: p.originalLanguage,
      })}
    />
  );
}

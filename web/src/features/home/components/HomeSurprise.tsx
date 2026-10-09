"use client";

import React from 'react';
import SurpriseButton from '@/features/surprise/SurpriseButton';
import { useTitleActions } from '@/features/home/components/TitleActions';

// "Surprise me" centered under the last row; Play starts the pick in this page's player
export default function HomeSurprise() {
  const { play } = useTitleActions();
  return (
    <div className="flex justify-center px-4 pt-8 md:pt-12">
      <SurpriseButton
        className="rounded-full border border-white/40 bg-white/10 px-7 py-3 text-base md:text-lg hover:border-white hover:bg-white/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
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
    </div>
  );
}

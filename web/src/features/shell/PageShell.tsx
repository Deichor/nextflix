"use client";

import React from 'react';
import Navbar from '@/features/shell/Navbar';

interface NetflixPageProps {
  children: React.ReactNode;
  className?: string;
}

// Page shell for browse/detail pages: #141414 background, top nav, footer space
export default function NetflixPage({ children, className = '' }: NetflixPageProps) {
  return (
    <div className={`min-h-screen overflow-x-hidden bg-[#141414] pb-20 text-white ${className}`}>
      <Navbar />
      {children}
    </div>
  );
}

export function EmptyState({ title, text, action }: { title: string; text?: string; action?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-24 text-center">
      <h2 className="mb-2 text-xl font-semibold text-white md:text-2xl">{title}</h2>
      {!!text && <p className="mb-6 max-w-md text-sm text-[#b3b3b3] md:text-base">{text}</p>}
      {action}
    </div>
  );
}

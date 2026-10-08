"use client";

import { useEffect } from 'react';

// The full-screen player sits over a scrollable page: hide its scrollbar and keep it still
export function useScrollLock(): void {
  useEffect(() => {
    const html = document.documentElement;
    const body = document.body;
    const prev = [html.style.overflow, body.style.overflow];
    html.style.overflow = 'hidden';
    body.style.overflow = 'hidden';
    return () => {
      html.style.overflow = prev[0];
      body.style.overflow = prev[1];
    };
  }, []);
}

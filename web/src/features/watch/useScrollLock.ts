"use client";

import { useEffect } from 'react';

// The full-screen player sits over a scrollable page: hide its scrollbar and keep it still
// (overscroll-behavior also stops pull-to-refresh and the rubber band on phones)
export function useScrollLock(): void {
  useEffect(() => {
    const html = document.documentElement;
    const body = document.body;
    const prev = [html.style.overflow, body.style.overflow, html.style.overscrollBehavior, body.style.overscrollBehavior];
    html.style.overflow = 'hidden';
    body.style.overflow = 'hidden';
    html.style.overscrollBehavior = 'none';
    body.style.overscrollBehavior = 'none';
    return () => {
      html.style.overflow = prev[0];
      body.style.overflow = prev[1];
      html.style.overscrollBehavior = prev[2];
      body.style.overscrollBehavior = prev[3];
    };
  }, []);
}

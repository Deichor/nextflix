"use client";

import { useId } from 'react';

// Lucide's thumbs-up drawn twice: the back thumb is masked where the front one sits, so no lines cross
const THUMB = 'M7 10v12M15 5.88 14 10h5.83a2 2 0 0 1 1.92 2.56l-2.33 8A2 2 0 0 1 17.5 22H4a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h2.76a2 2 0 0 0 1.79-1.11L12 2a3.13 3.13 0 0 1 3 3.88Z';
const OUTLINE = 'M15 5.88 14 10h5.83a2 2 0 0 1 1.92 2.56l-2.33 8A2 2 0 0 1 17.5 22H4a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h2.76a2 2 0 0 0 1.79-1.11L12 2a3.13 3.13 0 0 1 3 3.88Z';
const BACK = 'translate(0 0) scale(0.8)';
const FRONT = 'translate(4.8 4.8) scale(0.8)';

export default function DoubleThumbsUp({ className = '', filled = false }: { className?: string; filled?: boolean }) {
  const mask = `dt${useId().replace(/:/g, '')}`;
  const fill = filled ? 'currentColor' : 'none';
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <defs>
        <mask id={mask} maskUnits="userSpaceOnUse" x="0" y="0" width="24" height="24">
          <rect width="24" height="24" fill="#fff" />
          <path d={OUTLINE} transform={FRONT} fill="#000" stroke="#000" strokeWidth={5.5} />
        </mask>
      </defs>
      <g mask={`url(#${mask})`}>
        <path d={THUMB} transform={BACK} fill={fill} />
      </g>
      <path d={THUMB} transform={FRONT} fill={fill} />
    </svg>
  );
}

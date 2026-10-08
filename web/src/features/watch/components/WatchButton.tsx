"use client";

import React from 'react';
import { Play } from 'lucide-react';

interface WatchButtonProps {
  onClick: () => void;
  /** Shows "Resume" instead of "Play" */
  resume?: boolean;
  /** Extra text after the label, e.g. "S1:E2" */
  detail?: string;
  /** 0-100; renders a thin progress bar under the button when > 0 */
  progress?: number;
  /** e.g. "34m left", shown next to the progress bar */
  remainingText?: string;
  disabled?: boolean;
  className?: string;
}

// Netflix-style white Play / Resume button
export default function WatchButton({ onClick, resume, detail, progress = 0, remainingText, disabled, className = '' }: WatchButtonProps) {
  return (
    <div className={`inline-flex flex-col gap-2 ${className}`}>
      <button
        onClick={onClick}
        disabled={disabled}
        className="flex items-center justify-center gap-2 px-6 py-2 md:px-8 md:py-2.5 bg-white text-black font-bold rounded hover:bg-white/75 transition-colors text-sm md:text-lg disabled:opacity-60"
      >
        <Play className="w-5 h-5 md:w-6 md:h-6 fill-current" />
        <span>{resume ? 'Resume' : 'Play'}</span>
        {!!detail && <span className="font-semibold">{detail}</span>}
      </button>
      {progress > 0 && (
        <div className="flex items-center gap-2">
          <div className="h-[3px] w-24 md:w-32 bg-[#5a5a5a] rounded-sm overflow-hidden">
            <div className="h-full bg-[#E50914]" style={{ width: `${Math.min(100, progress)}%` }} />
          </div>
          {!!remainingText && <span className="text-xs text-[#b3b3b3] whitespace-nowrap">{remainingText}</span>}
        </div>
      )}
    </div>
  );
}

export function formatRemaining(position: number, duration: number): string {
  const left = Math.max(0, duration - position);
  const h = Math.floor(left / 3600);
  const m = Math.max(1, Math.round((left % 3600) / 60));
  return h > 0 ? `${h}h ${m}m left` : `${m}m left`;
}

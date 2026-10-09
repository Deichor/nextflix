"use client";

import React, { useRef, useState } from 'react';

interface Props {
  duration: number;
  /** Shown time: the dragged time while dragging, else the position */
  value: number;
  label: string;
  /** Time under the finger while dragging (null: drag cancelled) */
  onPreview: (t: number | null) => void;
  onCommit: (t: number) => void;
  /** Any touch on the bar (keeps the controls up) */
  onActivity?: () => void;
}

// Seek bar for touch: a 44 px tall target around a thin track. A drag starts only once the finger
// moved sideways, so a mostly vertical swipe never seeks; a still tap seeks to that point.
export default function TouchSeekBar({ duration, value, label, onPreview, onCommit, onActivity }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const gesture = useRef<{ id: number; x: number; y: number; dragging: boolean; t: number } | null>(null);
  const [active, setActive] = useState(false);
  const timeAt = (clientX: number) => {
    const r = ref.current?.getBoundingClientRect();
    if (!r || r.width <= 0) return 0;
    return Math.max(0, Math.min(1, (clientX - r.left) / r.width)) * duration;
  };
  const pct = duration > 0 ? Math.min(100, Math.max(0, (value / duration) * 100)) : 0;
  const end = () => {
    gesture.current = null;
    setActive(false);
  };

  return (
    <div
      ref={ref}
      role="slider"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={Math.round(duration)}
      aria-valuenow={Math.round(value)}
      className="relative h-11 touch-none"
      onPointerDown={e => {
        if (duration <= 0) return;
        e.currentTarget.setPointerCapture(e.pointerId);
        gesture.current = { id: e.pointerId, x: e.clientX, y: e.clientY, dragging: false, t: timeAt(e.clientX) };
        onActivity?.();
      }}
      onPointerMove={e => {
        const g = gesture.current;
        if (!g || g.id !== e.pointerId) return;
        const dx = e.clientX - g.x;
        const dy = e.clientY - g.y;
        if (!g.dragging) {
          // Vertical swipe: not a seek
          if (Math.abs(dy) > 12 && Math.abs(dy) > Math.abs(dx)) {
            end();
            return;
          }
          if (Math.abs(dx) < 6) return;
          g.dragging = true;
          setActive(true);
        }
        g.t = timeAt(e.clientX);
        onPreview(g.t);
        onActivity?.();
      }}
      onPointerUp={e => {
        const g = gesture.current;
        end();
        if (!g || g.id !== e.pointerId) return;
        onCommit(g.dragging ? g.t : timeAt(e.clientX));
      }}
      onPointerCancel={() => {
        const dragging = gesture.current?.dragging;
        end();
        if (dragging) onPreview(null);
      }}
    >
      <div className={`absolute inset-x-0 top-1/2 -translate-y-1/2 overflow-hidden rounded-full bg-white/30 transition-[height] duration-150 ${active ? 'h-1.5' : 'h-1'}`}>
        <div className="h-full bg-[#E50914]" style={{ width: `${pct}%` }} />
      </div>
      <div
        className={`absolute top-1/2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-[#E50914] shadow-[0_0_0_2px_rgba(255,255,255,0.9)] transition-[width,height] duration-150 ${active ? 'h-6 w-6' : 'h-4 w-4'}`}
        style={{ left: `${pct}%` }}
      />
    </div>
  );
}

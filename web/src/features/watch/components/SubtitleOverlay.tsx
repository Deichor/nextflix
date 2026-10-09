"use client";

import React, { useEffect, useState } from 'react';
import { Cue, activeCues } from '@/features/watch/subtitles';

interface SubtitleOverlayProps {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  /** Where the current stream starts in the file (the stream restarts with offsets) */
  offsetRef: React.MutableRefObject<number>;
  cues: Cue[];
  /** Seconds; positive shows subtitles later */
  delay: number;
  /** Controls are visible: sit above them */
  raised: boolean;
  /** Font size multiplier (Settings: SUBTITLE_SIZE_SCALE) */
  scale?: number;
  /** Touch layout: place and size from the picture box (px), instead of the viewport formula */
  layout?: { bottom: number; fontPx: number };
}

const ENTITIES: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', lrm: '', rlm: '' };
const decode = (s: string) => s.replace(/&(#\d+|[a-z]+);/gi, (m, e: string) => {
  if (e[0] === '#') return String.fromCharCode(Number(e.slice(1)));
  return ENTITIES[e.toLowerCase()] ?? m;
});

// Cue text -> styled runs. Only <i>, <b> and <u> are kept; other tags (voices, classes,
// timestamps) are dropped. Nothing is ever injected as HTML.
function renderCue(text: string, key: number): React.ReactNode {
  const out: React.ReactNode[] = [];
  const style = { i: 0, b: 0, u: 0 };
  let n = 0;
  for (const part of text.split(/(<[^>]*>|\n)/)) {
    if (!part) continue;
    if (part === '\n') { out.push(<br key={n++} />); continue; }
    const tag = part.match(/^<(\/?)([ibu])(?:[\s.][^>]*)?>$/i);
    if (tag) {
      const k = tag[2].toLowerCase() as 'i' | 'b' | 'u';
      style[k] = Math.max(0, style[k] + (tag[1] ? -1 : 1));
      continue;
    }
    if (part.startsWith('<')) continue;
    out.push(
      <span
        key={n++}
        style={{
          fontStyle: style.i ? 'italic' : undefined,
          fontWeight: style.b ? 800 : undefined,
          textDecoration: style.u ? 'underline' : undefined,
        }}
      >
        {decode(part)}
      </span>,
    );
  }
  return <span key={key} className="block">{out}</span>;
}

// Netflix-style subtitles drawn over the video. The cue for (offset + video time - delay) is
// looked up every frame, so timing stays right across stream restarts.
export default function SubtitleOverlay({ videoRef, offsetRef, cues, delay, raised, scale = 1, layout }: SubtitleOverlayProps) {
  const [shown, setShown] = useState<Cue[]>([]);

  useEffect(() => {
    if (cues.length === 0) { setShown([]); return; }
    let raf = 0;
    let lastKey = '';
    const tick = () => {
      const v = videoRef.current;
      if (v) {
        const now = activeCues(cues, offsetRef.current + v.currentTime - delay);
        const key = now.map(c => c.start).join('|');
        if (key !== lastKey) {
          lastKey = key;
          setShown(now);
        }
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [cues, delay, videoRef, offsetRef]);

  if (shown.length === 0) return null;

  return (
    <div
      aria-live="off"
      className="pointer-events-none absolute inset-x-0 z-[5] flex justify-center px-[6%] text-center transition-[bottom] duration-300"
      style={{ bottom: layout ? layout.bottom : raised ? 'calc(7.5rem + 2vh)' : '7vh' }}
    >
      <p
        className="max-w-[90%] font-semibold leading-[1.25] text-white"
        style={{
          fontSize: layout ? layout.fontPx : `clamp(${16 * scale}px, calc((1.6vw + 1.2vh) * ${scale}), ${54 * scale}px)`,
          textShadow: '0 0 4px rgba(0,0,0,0.95), 0 0 2px rgba(0,0,0,0.95), 0 2px 4px rgba(0,0,0,0.8)',
        }}
      >
        {shown.map((c, i) => renderCue(c.text, i))}
      </p>
    </div>
  );
}

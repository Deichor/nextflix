import React, { useEffect, useState } from 'react';
import { activeCues, type Cue } from '../lib/subtitles';

const ENTITIES: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', lrm: '', rlm: '' };
const decode = (s: string) => s.replace(/&(#\d+|[a-z]+);/gi, (m, e: string) => {
  if (e[0] === '#') return String.fromCharCode(Number(e.slice(1)));
  return ENTITIES[e.toLowerCase()] ?? m;
});

// Cue text -> styled runs; only <i>, <b>, <u> are kept and nothing is injected as HTML
function renderCue(text: string, key: number): React.ReactNode {
  const out: React.ReactNode[] = [];
  const style = { i: 0, b: 0, u: 0 };
  let n = 0;
  text.split(/(<[^>]*>|\n)/).forEach(part => {
    if (!part) return;
    if (part === '\n') { out.push(<br key={n++} />); return; }
    const tag = part.match(/^<(\/?)([ibu])(?:[\s.][^>]*)?>$/i);
    if (tag) {
      const k = tag[2].toLowerCase() as 'i' | 'b' | 'u';
      style[k] = Math.max(0, style[k] + (tag[1] ? -1 : 1));
      return;
    }
    if (part.charAt(0) === '<') return;
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
  });
  return <span key={key} className="cue">{out}</span>;
}

// Same timing rule as the web player: the cue for (stream offset + video time - delay),
// looked up every frame so it stays right across stream restarts
export default function SubtitleOverlay({ cues, time, raised }: { cues: Cue[]; time: () => number; raised: boolean }) {
  const [shown, setShown] = useState<Cue[]>([]);

  useEffect(() => {
    if (cues.length === 0) { setShown([]); return; }
    let raf = 0;
    let lastKey = '';
    const tick = () => {
      const now = activeCues(cues, time());
      const key = now.map(c => c.start).join('|');
      if (key !== lastKey) {
        lastKey = key;
        setShown(now);
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [cues, time]);

  if (shown.length === 0) return null;
  return (
    <div className={`subtitles ${raised ? 'is-raised' : ''}`}>
      <p className="subtitles-text">{shown.map((c, i) => renderCue(c.text, i))}</p>
    </div>
  );
}

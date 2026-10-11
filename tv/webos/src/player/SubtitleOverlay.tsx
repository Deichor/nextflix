import React, { useEffect, useState } from 'react';
import { activeCues, type Cue } from '../lib/subtitles';
import { subStyleClass, useSubtitleStyle } from '../lib/subtitleStyle';
import { cueCheckDelay } from './cueTiming';

const ENTITIES: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', lrm: '', rlm: '' };
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
  // .cue-box carries the background box (one per line, see styles.css)
  return <span key={key} className="cue"><span className="cue-box">{out}</span></span>;
}

const SCRIM_HOLD_MS = 1500;

/** Static subtitle look for previews (Settings): the sample text with the profile's style. */
export function SubtitleSample({ text }: { text: string }) {
  const style = useSubtitleStyle();
  return (
    <>
      {style.window !== 'none' && <div className={`subtitle-scrim scrim-${style.window} is-on`} />}
      <div className={`subtitles ${subStyleClass(style)}`}>
        <p className="subtitles-text">{renderCue(text, 0)}</p>
      </div>
    </>
  );
}

// Same timing rule as the web player: the cue for (stream offset + video time - delay), looked
// up right after each cue boundary (cueTiming.ts) and at least every 100 ms (seeks, restarts).
// A fixed 100 ms poll showed cues 50 ms late on average, up to 100 ms; far cheaper than every frame.
// `sample` shows while no cue does (live preview of the appearance settings).
export default function SubtitleOverlay({ cues, time, raised, sample, previewing }: {
  cues: Cue[];
  time: () => number;
  raised: boolean;
  sample?: string;
  previewing?: boolean;
}) {
  const style = useSubtitleStyle();
  const [shown, setShown] = useState<Cue[]>([]);
  const [scrim, setScrim] = useState(false);
  const on = shown.length > 0 || !!sample;

  useEffect(() => {
    if (cues.length === 0) { setShown([]); return; }
    let lastKey = '';
    let id: ReturnType<typeof setTimeout>;
    const tick = () => {
      const t = time();
      const now = activeCues(cues, t);
      const key = now.map(c => c.start).join('|');
      if (key !== lastKey) {
        lastKey = key;
        setShown(now);
      }
      id = setTimeout(tick, cueCheckDelay(cues, t));
    };
    tick();
    return () => clearTimeout(id);
  }, [cues, time]);

  // The dimmed band stays through short gaps between cues instead of blinking
  useEffect(() => {
    if (on) { setScrim(true); return; }
    const id = setTimeout(() => setScrim(false), SCRIM_HOLD_MS);
    return () => clearTimeout(id);
  }, [on]);

  return (
    <>
      {style.window !== 'none' && <div className={`subtitle-scrim scrim-${style.window} ${scrim ? 'is-on' : ''}`} />}
      {on && (
        <div className={`subtitles ${subStyleClass(style)} ${raised ? 'is-raised' : ''} ${previewing ? 'is-previewing' : ''}`}>
          <p className="subtitles-text">{shown.length > 0 ? shown.map((c, i) => renderCue(c.text, i)) : renderCue(sample || '', 0)}</p>
        </div>
      )}
    </>
  );
}

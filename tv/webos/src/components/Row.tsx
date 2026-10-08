import { memo, useCallback, useEffect, useRef, useState } from 'react';
import { img } from '../lib/http';
import { matchPercent, yearOf, type Title } from '../lib/catalog';
import { episodeLabel, progressPercent, type StreamProgress } from '../lib/stream';
import { useI18n, type I18n } from '../lib/i18n';
import { setHero, type HeroItem } from './heroStore';

export type RowDef =
  | { id: string; title: string; variant: 'backdrop' | 'rank'; items: Title[] | undefined }
  | { id: string; title: string; variant: 'continue'; items: StreamProgress[] | undefined };

export function heroOfTitle(t: Title, i18n: I18n, kicker?: string): HeroItem {
  const facts: string[] = [];
  const m = matchPercent(t.vote);
  if (m) facts.push(i18n.t('detail.match', { n: m }));
  const y = yearOf(t.date);
  if (y) facts.push(String(y));
  facts.push(i18n.t(t.type === 'tv' ? 'common.tvShows' : 'common.movies'));
  return { key: t.key, title: t.title, overview: t.overview, backdrop: t.backdrop, facts, kicker };
}

export function heroOfProgress(p: StreamProgress, i18n: I18n): HeroItem {
  const facts = [episodeLabel(i18n.locale, p.season, p.episode), p.episode_title].filter(Boolean);
  const left = Math.max(1, Math.round(Math.max(0, p.duration - p.position) / 60));
  facts.push(i18n.t('player.timeLeft', { time: i18n.duration(left) }));
  return { key: `cw-${p.media_type}-${p.tmdb_id}`, title: p.title, overview: '', backdrop: p.backdrop_path, facts };
}

interface RowProps {
  row: RowDef;
  onSelectTitle: (t: Title) => void;
  onSelectProgress?: (p: StreamProgress) => void;
}

const SKELETONS = [0, 1, 2, 3, 4, 5];
// Cards past the last focused one that already load their image
const AHEAD = 7;

function Row({ row, onSelectTitle, onSelectProgress }: RowProps) {
  const i18n = useI18n();
  const ref = useRef<HTMLElement>(null);
  const [near, setNear] = useState(false);
  const [reach, setReach] = useState(0);
  // Cards get callbacks that never change, so a focus move re-renders no card
  const latest = useRef({ row, i18n, onSelectTitle, onSelectProgress });
  latest.current = { row, i18n, onSelectTitle, onSelectProgress };

  // Images load once the row is close to the visible part of the screen
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (typeof IntersectionObserver === 'undefined') { setNear(true); return; }
    const root = el.closest('[data-scroll-y]');
    const io = new IntersectionObserver(entries => {
      if (entries.some(e => e.isIntersecting)) { setNear(true); io.disconnect(); }
    }, { root, rootMargin: '60% 0px 60% 0px' });
    io.observe(el);
    return () => io.disconnect();
  }, []);

  const focusAt = useCallback((i: number) => {
    setReach(r => Math.max(r, i));
    const { row: r, i18n: l } = latest.current;
    const item = r.items && r.items[i];
    if (!item) return;
    setHero(r.variant === 'continue' ? heroOfProgress(item as StreamProgress, l) : heroOfTitle(item as Title, l));
  }, []);

  const selectAt = useCallback((i: number) => {
    const { row: r, onSelectTitle: st, onSelectProgress: sp } = latest.current;
    const item = r.items && r.items[i];
    if (!item) return;
    if (r.variant === 'continue') sp?.(item as StreamProgress);
    else st(item as Title);
  }, []);

  const items = row.items;
  if (items && items.length === 0) return null;

  return (
    <section className={`row row-${row.variant}`} ref={ref} data-scroll-anchor>
      <h2 className="row-title">{row.title}</h2>
      <div className="row-scroller" data-scroll-x="start" data-scroll-pad="8">
        <div className="row-track" data-scroll-content data-nav-group={`row-${row.id}`}>
          {!items && SKELETONS.map(i => <div key={i} className="card card-skeleton" />)}
          {row.variant === 'continue' && items && (items as StreamProgress[]).map((p, i) => (
            <ContinueCard
              key={`${p.media_type}-${p.tmdb_id}`}
              p={p}
              index={i}
              navId={`${row.id}:${p.media_type}-${p.tmdb_id}`}
              show={near && i <= reach + AHEAD}
              onFocusAt={focusAt}
              onSelectAt={selectAt}
              label={episodeLabel(i18n.locale, p.season, p.episode)}
            />
          ))}
          {row.variant !== 'continue' && items && (items as Title[]).map((t, i) => (
            <TitleCard
              key={t.key}
              t={t}
              index={i}
              rank={row.variant === 'rank' ? t.rank ?? i + 1 : undefined}
              navId={`${row.id}:${t.key}`}
              show={near && i <= reach + AHEAD}
              onFocusAt={focusAt}
              onSelectAt={selectAt}
            />
          ))}
        </div>
      </div>
    </section>
  );
}

export default memo(Row);

// At most a few card images download and decode at once; the rest wait their turn
const MAX_LOADS = 4;
let activeLoads = 0;
const waiting: (() => void)[] = [];

function pump() {
  while (activeLoads < MAX_LOADS && waiting.length > 0) (waiting.shift() as () => void)();
}

/** Calls `start` when a slot is free; the returned function frees the slot (or leaves the queue). */
function loadSlot(start: () => void): () => void {
  let state: 'waiting' | 'active' | 'done' = 'waiting';
  // A stalled request must not hold its slot forever
  let timer: ReturnType<typeof setTimeout> | undefined;
  const release = () => {
    clearTimeout(timer);
    if (state === 'active') {
      activeLoads--;
      pump();
    } else if (state === 'waiting') {
      const i = waiting.indexOf(run);
      if (i >= 0) waiting.splice(i, 1);
    }
    state = 'done';
  };
  const run = () => {
    state = 'active';
    activeLoads++;
    timer = setTimeout(release, 8000);
    start();
  };
  if (activeLoads < MAX_LOADS) run();
  else waiting.push(run);
  return release;
}

/** Fades in once loaded; a failed load (the server fetching from TMDB timed out) is retried twice. */
export function CardImage({ src, show = true }: { src: string; show?: boolean }) {
  const [loaded, setLoaded] = useState(false);
  const [slot, setSlot] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const release = useRef<(() => void) | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => {
    if (!show || !src) return;
    const r = loadSlot(() => setSlot(true));
    release.current = r;
    return () => {
      r();
      release.current = undefined;
    };
  }, [show, src]);
  const done = () => {
    if (release.current) release.current();
    release.current = undefined;
  };
  if (!show || !src || !slot || attempt > 2) return null;
  return (
    <img
      key={attempt}
      className={`card-img ${loaded ? 'is-loaded' : ''}`}
      src={attempt ? `${src}?retry=${attempt}` : src}
      alt=""
      decoding="async"
      onLoad={() => { done(); setLoaded(true); }}
      onError={() => { done(); timer.current = setTimeout(() => setAttempt(a => a + 1), 1500 * (attempt + 1)); }}
    />
  );
}

interface CardProps {
  navId: string;
  index: number;
  show: boolean;
  onFocusAt: (i: number) => void;
  onSelectAt: (i: number) => void;
}

const TitleCard = memo(function TitleCard({ t, rank, navId, index, show, onFocusAt, onSelectAt }: CardProps & { t: Title; rank?: number }) {
  const focus = () => onFocusAt(index);
  const select = () => onSelectAt(index);
  if (rank !== undefined) {
    return (
      <button type="button" className="card-rank" data-nav data-nav-id={navId} onFocus={focus} onClick={select}>
        <span className="rank-number">{rank}</span>
        <span className="card card-poster">
          <CardImage src={img(t.poster || t.backdrop, 'w342')} show={show} />
        </span>
      </button>
    );
  }
  return (
    <button type="button" className="card card-backdrop" data-nav data-nav-id={navId} onFocus={focus} onClick={select}>
      <CardImage src={img(t.backdrop || t.poster, 'w500')} show={show} />
      <span className="card-caption">{t.title}</span>
    </button>
  );
});

const ContinueCard = memo(function ContinueCard({ p, label, navId, index, show, onFocusAt, onSelectAt }: CardProps & { p: StreamProgress; label: string }) {
  return (
    <button
      type="button"
      className="card card-backdrop card-continue"
      data-nav
      data-nav-id={navId}
      onFocus={() => onFocusAt(index)}
      onClick={() => onSelectAt(index)}
    >
      <CardImage src={img(p.backdrop_path || p.poster_path, 'w500')} show={show} />
      <span className="card-caption">
        {p.title}
        {!!label && <span className="card-caption-sub">{label}</span>}
      </span>
      <span className="card-progress"><span style={{ width: `${progressPercent(p)}%` }} /></span>
    </button>
  );
});

import { memo, useCallback, useEffect, useRef, useState, type FocusEvent } from 'react';
import { Info, Play, Shuffle } from 'lucide-react';
import { img } from '../lib/http';
import { getProfileId } from '../lib/profile';
import { useI18n } from '../lib/i18n';
import type { Title } from '../lib/catalog';
import type { StreamProgress } from '../lib/stream';
import { useEntry, useFocusRestore, useRouter, type Tab } from '../nav/router';
import { usePlayer } from '../nav/player';
import { isUpcoming, progressToRequest } from '../lib/stream';
import SideNav from './SideNav';
import Row, { RowPlaceholder, heroOfTitle, type RowDef } from './Row';
import { setHero, useHero, type HeroItem } from './heroStore';

// Rows mounted above and below the focused one
const ROWS_BEFORE = 2;
const ROWS_AFTER = 4;

interface RowsScreenProps {
  tab: Tab;
  /** Billboard title (Play / More Info); null while loading or when there is none */
  featured: Title | null;
  kicker?: string;
  rows: RowDef[];
  /** First data are in: focus can be restored */
  ready: boolean;
  /** Shows "Surprise me" under the last row (home) */
  surprise?: boolean;
  /** Called after a Continue Watching item was played */
  onPlayed?: () => void;
}

let layerSeq = 0;

// The backdrop is downloaded and decoded off-screen first, then faded in over the previous one
// (at most two layers; the covered one is dropped after the fade).
function HeroImage({ path }: { path: string }) {
  const src = img(path, 'w780');
  const [layers, setLayers] = useState<{ src: string; id: number }[]>([]);

  useEffect(() => {
    if (!src) {
      setLayers([]);
      return;
    }
    let cancelled = false;
    const im = new Image();
    const show = () => {
      if (cancelled) return;
      setLayers(l => [...l.filter(x => x.src !== src).slice(-1), { src, id: ++layerSeq }]);
    };
    if (typeof im.decode === 'function') {
      im.src = src;
      im.decode().then(show, () => { if (im.complete && im.naturalWidth > 0) show(); });
    } else {
      im.onload = show;
      im.src = src;
    }
    return () => {
      cancelled = true;
      im.onload = null;
    };
  }, [src]);

  useEffect(() => {
    if (layers.length < 2) return;
    const timer = setTimeout(() => setLayers(l => l.slice(-1)), 600);
    return () => clearTimeout(timer);
  }, [layers]);

  return <>{layers.map(l => <img key={l.id} className="hero-img" src={l.src} alt="" />)}</>;
}

interface HeroProps {
  featured: Title | null;
  kicker?: string;
  inRows: boolean;
  onOpen: (t: Title, autoplay?: boolean) => void;
  onButtonsFocus: () => void;
}

// Only this part follows the focused card (heroStore), so a focus move does not re-render the rows
const Hero = memo(function Hero({ featured, kicker, inRows, onOpen, onButtonsFocus }: HeroProps) {
  const i18n = useI18n();
  const { t } = i18n;
  const focusedHero = useHero();
  const featuredHero: HeroItem | null = featured ? heroOfTitle(featured, i18n, kicker) : null;
  const hero = inRows ? focusedHero || featuredHero : featuredHero;

  return (
    <div className="hero">
      {!!hero?.backdrop && <HeroImage path={hero.backdrop} />}
      <div className="hero-shade" />
      <div className={`hero-stack ${inRows ? 'is-compact' : ''}`}>
        {!!hero && (
          <div className="hero-info">
            {!!hero.kicker && !inRows && <div className="hero-kicker">{hero.kicker}</div>}
            <h1 className="hero-title">{hero.title}</h1>
            <div className="hero-facts">{hero.facts.map((f, i) => <span key={i}>{f}</span>)}</div>
            {!!hero.overview && <p className="hero-overview">{hero.overview}</p>}
          </div>
        )}
        {!!featured && (
          <div
            className={`hero-buttons ${inRows ? 'is-hidden' : ''}`}
            data-nav-group="hero"
            onFocus={onButtonsFocus}
          >
            <button type="button" className="btn btn-primary" data-nav data-nav-id="hero-play" onClick={() => onOpen(featured, true)}>
              <Play className="btn-icon btn-icon-fill" /> {t('player.play')}
            </button>
            <button type="button" className="btn btn-secondary" data-nav data-nav-id="hero-info" onClick={() => onOpen(featured)}>
              <Info className="btn-icon" /> {t('detail.moreInfo')}
            </button>
          </div>
        )}
      </div>
    </div>
  );
});

// Home, Movies and TV Shows: a hero with the focused title on top, rows of cards below
export default function RowsScreen({ tab, featured, kicker, rows, ready, surprise, onPlayed }: RowsScreenProps) {
  const router = useRouter();
  const player = usePlayer();
  const [inRows, setInRows] = useState(false);
  // Callbacks stay the same across renders, so the memoized rows do not re-render
  const latest = useRef({ router, player, onPlayed });
  latest.current = { router, player, onPlayed };

  useEffect(() => () => setHero(null, true), []);

  const firstRow = rows.find(r => r.items && r.items.length > 0);
  const firstCard = firstRow && firstRow.items
    ? `${firstRow.id}:${firstRow.variant === 'continue'
      ? `${(firstRow.items[0] as StreamProgress).media_type}-${(firstRow.items[0] as StreamProgress).tmdb_id}`
      : (firstRow.items[0] as Title).key}`
    : undefined;
  useFocusRestore(ready, featured ? 'hero-play' : firstCard);

  const openTitle = useCallback((item: Title, autoplay = false) => {
    latest.current.router.push({ name: 'detail', id: item.id, type: item.type, autoplay });
  }, []);
  const selectProgress = useCallback((p: StreamProgress) => {
    const { router: r, player: pl, onPlayed: done } = latest.current;
    // Not out yet: the show's page instead of playback
    if (isUpcoming(p)) r.push({ name: 'detail', id: p.tmdb_id, type: p.media_type });
    else pl.open(progressToRequest(p), done);
  }, []);
  const toHero = useCallback(() => setInRows(false), []);

  // Only the rows near the focused one are mounted; the others are placeholders of the same height
  const entry = useEntry();
  const [focusedRow, setFocusedRow] = useState<string | null>(null);
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  const toRows = useCallback((e: FocusEvent) => {
    setInRows(true);
    const section = (e.target as Element).closest('[data-row-index]');
    const r = section && rowsRef.current[Number(section.getAttribute('data-row-index'))];
    if (r) setFocusedRow(r.id);
  }, []);
  // Before the first focus: the row of the restored card (Back), else the top
  const restoreId = entry.focusId && entry.focusId.lastIndexOf(':') > 0 ? entry.focusId.slice(0, entry.focusId.lastIndexOf(':')) : null;
  const centerId = focusedRow || restoreId;
  const found = centerId ? rows.findIndex(r => r.id === centerId) : 0;
  // A focused row that left the list: the window stays where it was
  const lastCenter = useRef(0);
  const center = found >= 0 ? found : Math.min(lastCenter.current, rows.length - 1);
  lastCenter.current = center;

  return (
    <div className="screen rows-screen" data-nav-scope={`rows-${tab}-${getProfileId() ?? 0}`}>
      <SideNav active={tab} />
      <Hero featured={featured} kicker={kicker} inRows={inRows} onOpen={openTitle} onButtonsFocus={toHero} />
      <div
        className={`rows-viewport ${inRows || !featured ? 'in-rows' : ''}`}
        data-scroll-y="start"
        data-scroll-pad="0"
        onFocus={toRows}
      >
        <div className="rows-content" data-scroll-content>
          {rows.map((r, i) => (i >= center - ROWS_BEFORE && i <= center + ROWS_AFTER
            ? <Row key={r.id} row={r} index={i} onSelectTitle={openTitle} onSelectProgress={selectProgress} />
            : <RowPlaceholder key={r.id} row={r} index={i} />
          ))}
          {surprise && <SurpriseFooter />}
          <div className="rows-end" />
        </div>
      </div>
    </div>
  );
}

// Reached with Down from the last row; the hero explains it while it has focus
function SurpriseFooter() {
  const { t } = useI18n();
  const router = useRouter();
  const explain = () => setHero({ key: 'surprise', title: t('nav.surprise'), overview: t('surprise.hint'), backdrop: '', facts: [] }, true);
  return (
    <div className="rows-footer" data-scroll-anchor>
      <button
        type="button"
        className="btn btn-secondary"
        data-nav
        data-nav-id="home-surprise"
        onFocus={explain}
        onClick={() => router.push({ name: 'surprise' })}
      >
        <Shuffle className="btn-icon" /> {t('nav.surprise')}
      </button>
    </div>
  );
}

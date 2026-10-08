import { memo, useCallback, useEffect, useRef, useState } from 'react';
import { Info, Play } from 'lucide-react';
import { img } from '../lib/http';
import { useI18n } from '../lib/i18n';
import type { Title } from '../lib/catalog';
import type { StreamProgress } from '../lib/stream';
import { useFocusRestore, useRouter, type Tab } from '../nav/router';
import { usePlayer } from '../nav/player';
import { progressToRequest } from '../lib/stream';
import SideNav from './SideNav';
import Row, { heroOfTitle, type RowDef } from './Row';
import { setHero, useHero, type HeroItem } from './heroStore';

interface RowsScreenProps {
  tab: Tab;
  /** Billboard title (Play / More Info); null while loading or when there is none */
  featured: Title | null;
  kicker?: string;
  rows: RowDef[];
  /** First data are in: focus can be restored */
  ready: boolean;
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
export default function RowsScreen({ tab, featured, kicker, rows, ready, onPlayed }: RowsScreenProps) {
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
  const playProgress = useCallback((p: StreamProgress) => {
    const { player: pl, onPlayed: done } = latest.current;
    pl.open(progressToRequest(p), done);
  }, []);
  const toHero = useCallback(() => setInRows(false), []);
  const toRows = useCallback(() => setInRows(true), []);

  return (
    <div className="screen rows-screen">
      <SideNav active={tab} />
      <Hero featured={featured} kicker={kicker} inRows={inRows} onOpen={openTitle} onButtonsFocus={toHero} />
      <div
        className={`rows-viewport ${inRows || !featured ? 'in-rows' : ''}`}
        data-scroll-y="start"
        data-scroll-pad="0"
        onFocus={toRows}
      >
        <div className="rows-content" data-scroll-content>
          {rows.map(r => (
            <Row key={r.id} row={r} onSelectTitle={openTitle} onSelectProgress={playProgress} />
          ))}
          <div className="rows-end" />
        </div>
      </div>
    </div>
  );
}
